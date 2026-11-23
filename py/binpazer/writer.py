"""Writer serializes a binpazer file to a binary file object, byte-identical to the C reference."""

from __future__ import annotations

from struct import pack

from . import format as fmt
from .envelope import encode_envelope_header
from .format import FLAG_HAS_CRC, FLAG_LARGE_MODE, LARGE_UNIT, BinpazerError, TypeEntry, align8, check_guid, check_u16, crc32c
from .reader import Reader, _can_seek

# _MAX_PAD_EXTENT caps one resume Padding block, as C and Go do.
_MAX_PAD_EXTENT = 1 << 20
# A block this small goes to the file in one write.
_JOIN_MAX = 64 * 1024
_PAD = bytes(8)


def _btt_entry(type_id: int, guid: bytes, name: bytes) -> bytes:
	raw = 25 + len(name)
	return b"".join((pack("<HH", type_id, 0), guid, pack("<I", len(name)), name, bytes(1 + align8(raw) - raw)))


class Writer:
	"""Writes a binpazer file to `f`, a binary file object, from its position 0.

	The writer calls `f.write(data)`. When `f.seekable()` is true, `end()` also back-patches the header.
	`defs` lists the block types of the file. `appendable=True` starts an appendable file (spec 1.2).
	`version_minor=1` declares compression up front, which a file object that cannot seek needs.
	"""

	def __init__(self, f, writer_guid: bytes, writer_name: str, defs, version_minor: int | None = None, appendable: bool = False):
		self._setup(f, _can_seek(f), 0, 0)
		self._appendable = bool(appendable)
		if self._appendable:
			self._version_minor = fmt.VERSION_MINOR_APPENDABLE
		else:
			self._version_minor = fmt.VERSION_MINOR if version_minor is None else version_minor
		check_u16(self._version_minor, "version_minor")
		check_guid(writer_guid, "writer GUID")
		entries = [_btt_entry(fmt.TYPE_BLOCK_TYPE_TABLE, fmt.BLOCK_TYPE_TABLE_GUID, b"BlockTypeTable")]
		for d in defs:
			check_u16(d.type_id, "type id")
			check_guid(d.guid, "type GUID")
			entries.append(_btt_entry(d.type_id, bytes(d.guid), d.name.encode("utf-8")))
		if self._appendable:
			entries.append(_btt_entry(fmt.TYPE_END, fmt.END_GUID, b"End"))

		name = writer_name.encode("utf-8")
		first_block = align8(56 + 4 + len(name) + 1)
		flags = fmt.HEADER_FLAG_APPENDABLE if self._appendable else 0
		head = b"".join((
			fmt.MAGIC,
			fmt.SIGNATURE,
			pack("<HH", fmt.VERSION_MAJOR, self._version_minor),
			bytes(writer_guid),
			pack("<QQII", fmt.FILE_LENGTH_UNKNOWN, first_block, flags, 0),
			pack("<I", len(name)),
			name,
		))
		table = b"".join(entries)
		payload_len = 8 + len(table)
		if payload_len > 0xFFFFFFFF:
			raise BinpazerError("range", "block type table too large")
		self._write(b"".join((
			head,
			bytes(first_block - len(head)),
			pack("<HHIII", fmt.TYPE_BLOCK_TYPE_TABLE, fmt.FLAG_CRITICAL, payload_len, len(entries), 0),
			table,
		)))
		if self._appendable:
			self._flush_file()

	def _setup(self, f, seekable: bool, offset: int, sealed: int) -> None:
		self._f = f
		self._seekable = seekable
		self._flush = getattr(f, "flush", None)
		self._off = offset
		self._err: BinpazerError | None = None
		self._tracked: list[tuple[int, int]] = []
		self._index_offset = 0
		self._wrote_index = False
		self._ended = False
		self._used_compression = False
		self._sealed = sealed

	@classmethod
	def _resumed(cls, f, offset: int, version_minor: int, tail: int) -> Writer:
		w = cls.__new__(cls)
		w._setup(f, True, offset, tail)
		w._appendable = True
		w._version_minor = version_minor
		w._pad_over(tail)
		w._flush_file()
		return w

	@property
	def sealed_tail(self) -> int:
		"""The count of stale bytes that resume_append_writer covered with Padding blocks."""
		return self._sealed

	@property
	def offset(self) -> int:
		"""The file offset where the next block starts."""
		return self._off

	def block(self, type_id: int, flags: int, payload) -> None:
		"""Writes one block. `payload` is any bytes-like object."""
		self._check()
		self._write_block(type_id, flags, b"", payload)

	def block_compressed(self, type_id: int, flags: int, codec_id: int, codec_guid: bytes | None, stored, uncompressed_length: int) -> None:
		"""Wraps bytes that the caller already encoded with codec_id in an envelope."""
		self._check()
		if codec_id == fmt.CODEC_INVALID:
			self._fail(BinpazerError("range", "codec id 0 is invalid"))
		if codec_id == fmt.CODEC_BY_GUID and codec_guid is None:
			self._fail(BinpazerError("range", "codec id 65535 needs a codec GUID"))
		self.check_can_compress()
		try:
			env = encode_envelope_header(codec_id, codec_guid, uncompressed_length)
		except BinpazerError as e:
			self._fail(e)
		self._write_block(type_id, flags | fmt.FLAG_COMPRESSED, env, stored)
		self._used_compression = True

	def check_can_compress(self) -> None:
		"""Refuses a compressed block that a streaming writer cannot declare."""
		self._check()
		if not self._seekable and self._version_minor < fmt.VERSION_MINOR_COMPRESSION:
			self._fail(BinpazerError("noseek", "a compressed block on a file object that cannot seek needs version_minor >= 1 declared up front"))

	def write_index(self, entries=None) -> None:
		"""Writes the Block Index. With no argument it indexes every block this writer wrote.

		`entries` is a sequence of (type_id, offset) pairs.
		"""
		self._check()
		if self._appendable:
			raise BinpazerError("range", "an appendable file carries no Block Index")
		pairs = self._tracked if entries is None else list(entries)
		payload_len = 8 + len(pairs) * 16
		if payload_len > 0xFFFFFFFF:
			self._fail(BinpazerError("range", "block index too large"))
		parts = [pack("<HHIII", fmt.TYPE_BLOCK_INDEX, 0, payload_len, len(pairs), 0)]
		for type_id, offset in pairs:
			check_u16(type_id, "index type id")
			if not isinstance(offset, int) or not 0 <= offset < 1 << 64:
				self._fail(BinpazerError("range", f"index offset {offset!r} is invalid"))
			parts.append(pack("<HHIQ", type_id, 0, 0, offset))
		self._index_offset = self._off
		self._write(b"".join(parts))
		self._wrote_index = True

	def end(self) -> None:
		"""Writes the End block or the footer, flushes, and back-patches the header when the file object can seek."""
		if self._err is not None:
			raise self._err
		if self._appendable and self._ended:
			return
		self._check()
		if self._appendable:
			self._write_block(fmt.TYPE_END, 0, b"", b"")
		if self._wrote_index:
			self._write(pack("<Q", self._index_offset) + fmt.FOOTER_MAGIC)
		if self._seekable:
			f = self._f
			try:
				if self._used_compression and self._version_minor < fmt.VERSION_MINOR_COMPRESSION:
					f.seek(14)
					f.write(pack("<H", fmt.VERSION_MINOR_COMPRESSION))
				f.seek(32)
				f.write(pack("<Q", self._off))
				f.seek(self._off)
			except Exception as cause:
				self._fail(BinpazerError("io", "the output failed"), cause)
		self._flush_file()
		self._ended = True

	def _write_block(self, type_id: int, flags: int, prefix: bytes, body) -> None:
		check_u16(type_id, "type id")
		check_u16(flags, "flags")
		if self._appendable:
			flags |= FLAG_HAS_CRC
		body_len = len(body)
		payload_len = len(prefix) + body_len
		if flags & FLAG_LARGE_MODE:
			if payload_len % LARGE_UNIT != 0:
				self._fail(BinpazerError("range", "a large_mode payload must be a multiple of 4096 bytes"))
			length_field = payload_len // LARGE_UNIT
		else:
			length_field = payload_len
		if length_field > 0xFFFFFFFF:
			self._fail(BinpazerError("range", "payload too large for the length field (use FLAG_LARGE_MODE)"))

		if not self._appendable:
			self._tracked.append((type_id, self._off))
		head = pack("<HHI", type_id, flags, length_field) + prefix
		has_crc = flags & FLAG_HAS_CRC
		extent = 8 + payload_len + (4 if has_crc else 0)
		pad = _PAD[: align8(extent) - extent]
		if body_len <= _JOIN_MAX:
			sealed = head + body if type(body) is bytes else head + bytes(body)
			if has_crc:
				self._write(b"".join((sealed, pack("<I", crc32c(0, sealed)), pad)))
			else:
				self._write(sealed + pad)
		else:
			self._write(head)
			self._write(body)
			if has_crc:
				self._write(pack("<I", crc32c(crc32c(0, head), body)) + pad)
			elif pad:
				self._write(pad)
		if self._appendable:
			self._flush_file()

	def _pad_over(self, n: int) -> None:
		"""Covers the next n bytes with sealed zero Padding blocks, in the layout that C and Go use."""
		while n > 0:
			extent = min(max(align8(n), 16), _MAX_PAD_EXTENT)
			sealed = pack("<HHI", fmt.TYPE_PADDING, FLAG_HAS_CRC, extent - 12) + bytes(extent - 12)
			self._write(sealed + pack("<I", crc32c(0, sealed)))
			if extent >= n:
				break
			n -= extent

	def _write(self, data) -> None:
		try:
			self._f.write(data)
		except Exception as cause:
			self._fail(BinpazerError("io", "the output failed"), cause)
		self._off += len(data)

	def _flush_file(self) -> None:
		if self._flush is None:
			return
		try:
			self._flush()
		except Exception as cause:
			self._fail(BinpazerError("io", "the output failed"), cause)

	def _check(self) -> None:
		if self._err is not None:
			raise self._err
		if self._ended:
			if self._appendable:
				raise BinpazerError("ended", "the appendable file is ended")
			raise BinpazerError("format", "writer already ended")

	def _fail(self, e: BinpazerError, cause: BaseException | None = None):
		"""Makes e the sticky error of the writer and raises it."""
		self._err = e
		raise e from cause


def resume_append_writer(f, max_alloc: int = 0) -> tuple[Writer, list[TypeEntry]]:
	"""Continues an open appendable file. It walks the committed blocks and seals a torn tail.

	`f` reads, writes and seeks (mode "r+b"). The result is the writer and the block types of the file.
	"""
	if not _can_seek(f):
		raise BinpazerError("noseek", "resume needs a file object that can seek")
	r = Reader(f, max_alloc)
	while True:
		b = r.next_committed()
		if b is fmt.PENDING:
			break
		if b is None:
			raise BinpazerError("ended", "the appendable file is ended")
		r.skip_payload(b)
	end = r.offset
	tail = max(0, r.input_size - end)
	try:
		f.seek(end)
	except Exception as cause:
		raise BinpazerError("io", "the output failed") from cause
	return Writer._resumed(f, end, r.version_minor, tail), r.types
