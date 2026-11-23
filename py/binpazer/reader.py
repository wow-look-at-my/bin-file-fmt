"""Reader parses a binpazer file from a binary file object and gates every untrusted length before it allocates."""

from __future__ import annotations

from dataclasses import dataclass
from struct import pack, unpack_from

from .format import (
	DEFAULT_MAX_ALLOC,
	FLAG_HAS_CRC,
	FOOTER_MAGIC,
	HEADER_FLAG_APPENDABLE,
	IO_BUF_SIZE,
	MAGIC,
	PENDING,
	SIGNATURE,
	TYPE_BLOCK_TYPE_TABLE,
	TYPE_END,
	VERSION_MAJOR,
	FILE_LENGTH_UNKNOWN,
	FLAG_LARGE_MODE,
	LARGE_UNIT,
	BinpazerError,
	TypeEntry,
	align8,
	crc32c,
	decode_payload_length,
	payload_claim,
)

_MIN_BTT_ENTRY_SIZE = 32
_SKIP_CHUNK = 1 << 20
# A skip this short reads through, which costs less than a seek.
_SKIP_READ_MAX = 4096
_ONE_READ_MAX = 64 * 1024


@dataclass(slots=True)
class Block:
	"""One block header. `guid` is None and `known` is False when the type table does not list `type_id`."""

	type_id: int
	flags: int
	payload_length: int
	payload_offset: int
	offset: int
	guid: bytes | None
	known: bool
	name: str


def block_header_bytes(type_id: int, flags: int, payload_length: int) -> bytes:
	"""Rebuilds a block's 8-byte header, the first bytes its CRC covers."""
	field = payload_length // LARGE_UNIT if flags & FLAG_LARGE_MODE else payload_length
	return pack("<HHI", type_id, flags, field & 0xFFFFFFFF)


def _can_seek(f) -> bool:
	probe = getattr(f, "seekable", None)
	if probe is None:
		return hasattr(f, "seek")
	return bool(probe())


class Reader:
	"""Reads a binpazer file from `f`, a binary file object.

	The reader calls `f.read(n)`. It also seeks when `f.seekable()` is true, and it then learns the input size.
	`max_alloc` caps one length field of an input that cannot seek. The caller reads or skips each payload before the next `next()`.
	"""

	def __init__(self, f, max_alloc: int = 0):
		self._f = f
		self._max_alloc = max_alloc if max_alloc > 0 else DEFAULT_MAX_ALLOC
		self._seekable = _can_seek(f)
		self._cursor = 0
		self._pushback = b""
		self._size = -1
		self._saw_end = False
		self._torn = False
		self._index_found = False
		self._index_off = 0
		self._type_idx: dict[int, TypeEntry] = {}
		if self._seekable:
			self._size = self._measure()
			self._seek_to(0)

		fixed = self._read_exact(56)
		if fixed[0:8] != MAGIC:
			raise BinpazerError("bad_magic", "bad magic")
		if fixed[8:12] != SIGNATURE:
			raise BinpazerError("bad_magic", "bad signature")
		self.version_major, self.version_minor = unpack_from("<HH", fixed, 12)
		if self.version_major > VERSION_MAJOR:
			raise BinpazerError("version", f"unsupported major version {self.version_major}")
		self.writer_guid = fixed[16:32]
		file_length, self.first_block_offset, self.header_flags = unpack_from("<QQI", fixed, 32)
		# file_length is None for the streaming sentinel.
		self.file_length: int | None = None if file_length == FILE_LENGTH_UNKNOWN else file_length

		(name_len,) = unpack_from("<I", self._read_exact(4))
		self._claim(name_len, "writer name")
		self.writer_name = self._read_exact(name_len).decode("utf-8", "replace")
		self._skip(1)
		if self._cursor > self.first_block_offset:
			raise BinpazerError("format", "header overruns first_block_offset")
		self._skip(self.first_block_offset - self._cursor)

		self.types: list[TypeEntry] = self._read_btt()
		self._table_end = self._cursor

		fl = self.file_length
		if self._seekable and fl is not None and 16 <= fl <= self._size:
			saved = self._cursor
			self._seek_to(fl - 16)
			foot = self._read_up_to(16)
			if len(foot) == 16 and foot[8:] == FOOTER_MAGIC:
				self._record_index(foot)
			self._seek_to(saved)
		self._blocks_end = fl
		if fl is not None and self._index_found:
			self._blocks_end = fl - 16

	@property
	def has_index(self) -> bool:
		"""True when a Block Index was located through the footer."""
		return self._index_found

	@property
	def index_offset(self) -> int:
		return self._index_off

	@property
	def offset(self) -> int:
		"""The position of the next byte the reader consumes."""
		return self._cursor

	@property
	def input_size(self) -> int:
		"""The input size in bytes, or -1 when the input cannot seek."""
		return self._size

	@property
	def appendable(self) -> bool:
		return (self.header_flags & HEADER_FLAG_APPENDABLE) != 0

	@property
	def ended(self) -> bool:
		"""True when the walk reached the End block of an appendable file."""
		return self._saw_end

	@property
	def torn(self) -> bool:
		"""True when the last PENDING from next_committed was a whole block whose seal is absent or incorrect."""
		return self._torn

	def next(self) -> Block | None:
		"""Reads the next block header. Returns None after the last block."""
		if self._at_end():
			return None
		fl = self.file_length
		if not self._index_found and fl is not None and fl >= 16 and self._cursor == fl - 16:
			foot = self._read_up_to(16)
			if len(foot) == 16 and foot[8:] == FOOTER_MAGIC:
				self._record_index(foot)
				return None
			self._unread(foot)
		hdr = self._read_up_to(8)
		if not hdr:
			return None
		if len(hdr) < 8:
			raise self._truncated()
		return self._note_end(self._parse_header(hdr, self._cursor - 8))

	def next_committed(self) -> Block | str | None:
		"""Returns the next committed block, PENDING with the reader unmoved, or None after the End block.

		The input must seek, because the reader learns here how much of a file that grows is there.
		"""
		if not self._seekable:
			raise BinpazerError("noseek", "next_committed needs an input that can seek")
		if not self.appendable:
			raise BinpazerError("not_appendable", "the file does not set the appendable header flag")
		if self._at_end():
			return None
		start = self._cursor
		self._size = self._measure()
		self._seek_to(start)
		hdr = self._verify_committed(start)
		if hdr is None:
			self._seek_to(start)
			return PENDING
		self._seek_to(start + 8)
		return self._note_end(self._parse_header(hdr, start))

	def read_payload(self, b: Block) -> bytes:
		"""Reads the payload, verifies the CRC, and moves to the next block."""
		plen = b.payload_length
		has_crc = b.flags & FLAG_HAS_CRC
		claim = plen + 4 if has_crc else plen
		self._claim(claim, "block payload")
		if plen <= _ONE_READ_MAX and self._cursor == b.payload_offset:
			# One read takes the payload, the seal and the padding of a small block.
			extent = ((self._cursor + claim + 7) & ~7) - self._cursor
			buf = self._read_up_to(extent)
			if len(buf) < claim:
				raise self._truncated()
			data = buf if len(buf) == plen else buf[:plen]
			if has_crc and crc32c(0, block_header_bytes(b.type_id, b.flags, plen) + data) != unpack_from("<I", buf, plen)[0]:
				raise BinpazerError("crc", f"CRC mismatch in block at offset {b.offset}")
			if len(buf) < extent:
				raise self._truncated()
			return data
		data = self._read_exact(plen)
		if has_crc:
			self._verify_crc(b, crc32c(crc32c(0, block_header_bytes(b.type_id, b.flags, plen)), data))
		self._advance_past(b)
		return data

	def read_payload_into(self, b: Block, dst) -> int:
		"""Fills the front of the writable buffer dst with the payload, verifies the CRC, and returns the byte count."""
		view = memoryview(dst).cast("B")
		if len(view) < b.payload_length:
			raise BinpazerError("short_buffer", f"destination holds {len(view)} bytes, payload is {b.payload_length}")
		self._claim(payload_claim(b.payload_length, b.flags), "block payload")
		view = view[: b.payload_length]
		self._read_into(view)
		if b.flags & FLAG_HAS_CRC:
			self._verify_crc(b, crc32c(crc32c(0, block_header_bytes(b.type_id, b.flags, b.payload_length)), view))
		self._advance_past(b)
		return b.payload_length

	def stream_payload(self, b: Block, sink, chunk_size: int = IO_BUF_SIZE) -> None:
		"""Hands the payload to sink(chunk) in chunks. A CRC failure arrives after the last chunk."""
		self._claim(payload_claim(b.payload_length, b.flags), "block payload")
		if chunk_size <= 0:
			raise BinpazerError("range", "stream_payload needs a positive chunk size")
		has_crc = (b.flags & FLAG_HAS_CRC) != 0
		crc = crc32c(0, block_header_bytes(b.type_id, b.flags, b.payload_length)) if has_crc else 0
		rem = b.payload_length
		while rem > 0:
			chunk = self._read_exact(min(rem, chunk_size))
			if has_crc:
				crc = crc32c(crc, chunk)
			sink(chunk)
			rem -= len(chunk)
		if has_crc:
			self._verify_crc(b, crc)
		self._advance_past(b)

	def skip_payload(self, b: Block) -> None:
		self._advance_past(b)

	def find(self, type_id: int) -> list[int]:
		"""Returns the offsets of the blocks of type_id, through the Block Index when present. It keeps the position."""
		self._need_seek("find")
		saved = self._cursor
		try:
			if self._index_found:
				return self._find_indexed(type_id)
			if self.appendable:
				return self._find_committed(type_id)
			return self._find_walk(type_id)
		finally:
			self._seek_to(saved)

	def at(self, offset: int) -> Block:
		"""Reads the header of the block at offset. read_payload then reads that block."""
		self._need_seek("at")
		if not isinstance(offset, int) or offset < 0:
			raise BinpazerError("range", f"block offset {offset!r} is invalid")
		if offset > self._size:
			raise BinpazerError("truncated", f"block offset {offset} is beyond the {self._size}-byte input")
		self._seek_to(offset)
		return self._parse_header(self._read_exact(8), offset)

	def claim_decoded(self, n: int) -> None:
		"""The allocation gate for decompressed bytes."""
		if n > self._max_alloc:
			raise BinpazerError("capacity", f"decompressed payload claims {n} bytes, over the {self._max_alloc}-byte cap")

	def _at_end(self) -> bool:
		return self._saw_end or (self._blocks_end is not None and self._cursor >= self._blocks_end)

	def _verify_committed(self, start: int) -> bytes | None:
		"""Checks the seal of the block at start. Returns its header, or None when the block is not committed."""
		self._torn = False
		if self._size < start + 8:
			return None
		hdr = self._read_exact(8)
		type_id, flags, length_field = unpack_from("<HHI", hdr)
		payload_length = decode_payload_length(length_field, flags)
		if type_id == 0:
			return None
		if not flags & FLAG_HAS_CRC:
			self._torn = True
			return None
		if align8(start + 8 + payload_length + 4) > self._size:
			return None
		crc = crc32c(0, hdr)
		rem = payload_length
		while rem > 0:
			chunk = self._read_exact(min(rem, IO_BUF_SIZE))
			crc = crc32c(crc, chunk)
			rem -= len(chunk)
		(seal,) = unpack_from("<I", self._read_exact(4))
		if crc != seal:
			self._torn = True
			return None
		return hdr

	def _note_end(self, b: Block) -> Block:
		if b.type_id == TYPE_END and self.appendable:
			self._saw_end = True
		return b

	def _record_index(self, foot: bytes) -> None:
		self._index_found = True
		(self._index_off,) = unpack_from("<Q", foot)

	def _find_indexed(self, type_id: int) -> list[int]:
		if self._index_off > self._size:
			raise BinpazerError("truncated", f"footer index_block_offset {self._index_off} is beyond the {self._size}-byte input")
		self._seek_to(self._index_off)
		head = self._read_exact(16)
		(count,) = unpack_from("<I", head, 8)
		self._claim(count * 16, "block index")
		out = []
		rem = count
		while rem > 0:
			batch = self._read_exact(min(rem, 4096) * 16)
			for at in range(0, len(batch), 16):
				if batch[at] | (batch[at + 1] << 8) == type_id:
					out.append(unpack_from("<Q", batch, at + 8)[0])
			rem -= len(batch) // 16
		return out

	def _find_committed(self, type_id: int) -> list[int]:
		"""Walks the committed blocks of an appendable file. The table has no seal, so it counts by position."""
		saw_end = self._saw_end
		self._saw_end = False
		try:
			out = [self.first_block_offset] if type_id == TYPE_BLOCK_TYPE_TABLE else []
			self._seek_to(self._table_end)
			while True:
				b = self.next_committed()
				if b is None or b is PENDING:
					return out
				if b.type_id == type_id:
					out.append(b.offset)
				self.skip_payload(b)
		finally:
			self._saw_end = saw_end

	def _find_walk(self, type_id: int) -> list[int]:
		out = []
		self._seek_to(self.first_block_offset)
		while self._blocks_end is None or self._cursor < self._blocks_end:
			at = self._cursor
			hdr = self._read_up_to(8)
			if not hdr:
				break
			if len(hdr) < 8:
				raise self._truncated()
			tid, flags, length_field = unpack_from("<HHI", hdr)
			claim = payload_claim(decode_payload_length(length_field, flags), flags)
			self._claim(claim, "block payload")
			if tid == type_id:
				out.append(at)
			self._seek_to(align8(at + 8 + claim))
		return out

	def _parse_header(self, hdr: bytes, at: int) -> Block:
		type_id, flags, length_field = unpack_from("<HHI", hdr)
		payload_length = length_field * LARGE_UNIT if flags & FLAG_LARGE_MODE else length_field
		self._claim(payload_length + 4 if flags & FLAG_HAS_CRC else payload_length, "block payload")
		t = self._type_idx.get(type_id)
		if t is None:
			return Block(type_id, flags, payload_length, at + 8, at, None, False, "")
		return Block(type_id, flags, payload_length, at + 8, at, t.guid, True, t.name)

	def _read_btt(self) -> list[TypeEntry]:
		type_id, _, btt_len = unpack_from("<HHI", self._read_exact(8))
		if type_id != TYPE_BLOCK_TYPE_TABLE:
			raise BinpazerError("format", "first block is not the block type table")
		self._claim(btt_len, "block type table")
		btt_end = align8(self.first_block_offset + 8 + btt_len)
		(count,) = unpack_from("<I", self._read_exact(8))
		max_entries = (btt_len - 8) // _MIN_BTT_ENTRY_SIZE if btt_len >= 8 else 0
		if count > max_entries:
			raise BinpazerError("capacity", f"block type table claims {count} entries but its {btt_len}-byte payload holds at most {max_entries}")
		types = []
		for i in range(count):
			eh = self._read_exact(24)
			tid, nlen = unpack_from("<H18xI", eh)
			name = ""
			if nlen > 0:
				if self._cursor >= btt_end or nlen > btt_end - self._cursor:
					raise BinpazerError("capacity", f"block type table entry {i} name ({nlen} bytes) overruns the table")
				name = self._read_exact(nlen).decode("utf-8", "replace")
			self._skip(1)
			raw = 25 + nlen
			self._skip(align8(raw) - raw)
			entry = TypeEntry(tid, eh[4:20], name)
			self._type_idx.setdefault(tid, entry)
			types.append(entry)
		if self._cursor > btt_end:
			raise BinpazerError("format", "block type table overrun")
		self._skip(btt_end - self._cursor)
		return types

	def _verify_crc(self, b: Block, crc: int) -> None:
		(stored,) = unpack_from("<I", self._read_exact(4))
		if crc != stored:
			raise BinpazerError("crc", f"CRC mismatch in block at offset {b.offset}")

	def _advance_past(self, b: Block) -> None:
		nxt = align8(b.payload_offset + payload_claim(b.payload_length, b.flags))
		if nxt < self._cursor:
			raise BinpazerError("format", f"reader at offset {self._cursor} is past the block that ends at {nxt}")
		self._skip(nxt - self._cursor)

	def _claim(self, n: int, what: str) -> None:
		"""Checks that a length from the file can be satisfied before anything is sized from it."""
		if self._size >= 0:
			rem = max(0, self._size - self._cursor)
			if n > rem:
				raise BinpazerError("truncated", f"{what} claims {n} bytes but only {rem} remain in the input")
			return
		if n > self._max_alloc:
			raise BinpazerError("capacity", f"{what} claims {n} bytes, over the {self._max_alloc}-byte cap for an input of unknown size")

	def _need_seek(self, op: str) -> None:
		if not self._seekable:
			raise BinpazerError("noseek", f"{op} needs an input that can seek")

	def _truncated(self) -> BinpazerError:
		return BinpazerError("truncated", f"unexpected end of input at offset {self._cursor}")

	def _measure(self) -> int:
		"""Returns the input size. It leaves the file position at the end."""
		f = self._f
		end = self._io(lambda: f.seek(0, 2))
		return self._io(f.tell) if end is None else end

	def _seek_to(self, to: int) -> None:
		f = self._f
		self._io(lambda: f.seek(to))
		self._pushback = b""
		self._cursor = to

	def _skip(self, n: int) -> None:
		if n <= 0:
			return
		if self._seekable and not self._pushback and n > _SKIP_READ_MAX:
			target = self._cursor + n
			if target > self._size:
				self._seek_to(self._size)
				raise self._truncated()
			self._seek_to(target)
			return
		while n > 0:
			got = len(self._read_up_to(min(n, _SKIP_CHUNK)))
			if got == 0:
				raise self._truncated()
			n -= got

	def _unread(self, data: bytes) -> None:
		self._pushback = data + self._pushback
		self._cursor -= len(data)

	def _read_exact(self, n: int) -> bytes:
		data = self._read_up_to(n)
		if len(data) < n:
			raise self._truncated()
		return data

	def _read_up_to(self, n: int) -> bytes:
		"""Reads n bytes. A short result means end of input."""
		if n <= 0:
			return b""
		if self._pushback:
			head = self._pushback[:n]
			self._pushback = self._pushback[n:]
			self._cursor += len(head)
			if len(head) == n:
				return head
			return head + self._read_up_to(n - len(head))
		read = self._f.read
		try:
			data = read(n)
			if data is None:
				data = b""
			if 0 < len(data) < n:
				parts = [data]
				got = len(data)
				while got < n:
					more = read(n - got)
					if not more:
						break
					parts.append(more)
					got += len(more)
				data = b"".join(parts)
		except BinpazerError:
			raise
		except Exception as cause:
			raise BinpazerError("io", "the input failed") from cause
		if len(data) > n:
			raise BinpazerError("io", f"the input returned {len(data)} bytes for a read of {n}")
		self._cursor += len(data)
		return data

	def _read_into(self, view: memoryview) -> None:
		n = len(view)
		readinto = getattr(self._f, "readinto", None)
		if readinto is None or self._pushback:
			view[:] = self._read_exact(n)
			return
		got = 0
		while got < n:
			k = self._io(lambda: readinto(view[got:]))
			if not k:
				self._cursor += got
				raise self._truncated()
			got += k
		self._cursor += n

	def _io(self, call):
		try:
			return call()
		except BinpazerError:
			raise
		except Exception as cause:
			raise BinpazerError("io", "the input failed") from cause
