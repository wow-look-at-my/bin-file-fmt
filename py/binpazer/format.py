"""Shared binpazer constants, GUID helpers, CRC-32C and the error type."""

from __future__ import annotations

from dataclasses import dataclass
from struct import iter_unpack

MAGIC = b"binpazer"
SIGNATURE = b"\r\n\x1a\n"
FOOTER_MAGIC = b"rezapnib"
VERSION_MAJOR = 1
VERSION_MINOR = 0
VERSION_MINOR_COMPRESSION = 1
VERSION_MINOR_APPENDABLE = 2
HEADER_FLAG_APPENDABLE = 1
LARGE_UNIT = 4096
FILE_LENGTH_UNKNOWN = 0xFFFFFFFFFFFFFFFF

TYPE_INVALID = 0
TYPE_INVALID_MAX = 65535
TYPE_BLOCK_TYPE_TABLE = 65534
TYPE_BLOCK_INDEX = 65533
TYPE_WRITER_INFO = 65532
TYPE_PADDING = 65531
TYPE_GROUP = 65530
TYPE_END = 65529

FLAG_CRITICAL = 1
FLAG_SAFE_TO_COPY = 2
FLAG_COMPRESSED = 4
FLAG_HAS_CRC = 8
FLAG_LARGE_MODE = 16

CODEC_INVALID = 0
CODEC_STORED = 1
CODEC_DEFLATE = 2
CODEC_ZSTD = 3
CODEC_LZ4 = 4
CODEC_BY_GUID = 65535

ENVELOPE_FIXED_SIZE = 16
ENVELOPE_GUID_SIZE = 32

# IO_BUF_SIZE is the default streaming chunk.
IO_BUF_SIZE = 64 * 1024

# DEFAULT_MAX_ALLOC caps one untrusted length field when the input size is unknown.
DEFAULT_MAX_ALLOC = 1024 * 1024 * 1024

# PENDING is what Reader.next_committed returns when no committed block is at the boundary yet.
PENDING = "pending"


class BinpazerError(Exception):
	"""Every failure of this package. `code` is one of the names below.

	bad_magic, version, format, range, crc, truncated, capacity, noseek, bad_envelope,
	short_buffer, codec_unavailable, io, not_appendable, ended.
	"""

	def __init__(self, code: str, message: str):
		super().__init__(f"binpazer: {message}")
		self.code = code


@dataclass(frozen=True, slots=True)
class TypeEntry:
	"""One Block Type Table entry. `guid` is 16 bytes in canonical text order."""

	type_id: int
	guid: bytes
	name: str


# A writer takes the same shape for the types it defines.
TypeDef = TypeEntry


def _well_known(last: int) -> bytes:
	return b"binpazer" + bytes(7) + bytes([last])


BLOCK_TYPE_TABLE_GUID = _well_known(1)
BLOCK_INDEX_GUID = _well_known(2)
WRITER_INFO_GUID = _well_known(3)
PADDING_GUID = _well_known(4)
GROUP_GUID = _well_known(5)
END_GUID = _well_known(6)


def format_guid(g: bytes) -> str:
	if len(g) != 16:
		raise BinpazerError("range", f"a GUID is 16 bytes, got {len(g)}")
	h = bytes(g).hex()
	return f"{h[0:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:32]}"


_HEX_DIGITS = frozenset("0123456789abcdefABCDEF")


def parse_guid(text: str) -> bytes:
	"""Parses canonical GUID text. Hyphens are separators and can be anywhere."""
	clean = text.replace("-", "")
	if len(clean) != 32 or not _HEX_DIGITS.issuperset(clean):
		raise BinpazerError("format", f"invalid GUID {text!r} (need 32 hex digits)")
	return bytes.fromhex(clean)


def _byte_tables() -> list[list[int]]:
	t0 = []
	for i in range(256):
		c = i
		for _ in range(8):
			c = (c >> 1) ^ 0x82F63B78 if c & 1 else c >> 1
		t0.append(c)
	tables = [t0]
	for _ in range(7):
		prev = tables[-1]
		tables.append([(p >> 8) ^ t0[p & 255] for p in prev])
	return tables


_T = _byte_tables()
_T0 = _T[0]
# Tables indexed by a little-endian u16; crc32c builds them at the first long input.
_W: list[list[int]] = []
_WORD_TABLE_MIN = 2048


def _word_tables() -> None:
	for hi, lo in ((7, 6), (5, 4), (3, 2), (1, 0)):
		a = _T[hi]
		b = _T[lo]
		_W.append([x ^ y for y in b for x in a])


def crc32c(crc: int, data) -> int:
	"""CRC-32C (Castagnoli) of data, continued from crc. Start with 0."""
	n = len(data)
	c = crc ^ 0xFFFFFFFF
	t0 = _T0
	tail = n & 7
	if n >= 8:
		view = memoryview(data)
		if n < _WORD_TABLE_MIN and not _W:
			t7, t6, t5, t4, t3, t2, t1 = _T[7], _T[6], _T[5], _T[4], _T[3], _T[2], _T[1]
			for b0, b1, b2, b3, b4, b5, b6, b7 in iter_unpack("8B", view[: n - tail]):
				c = (
					t7[b0 ^ (c & 255)]
					^ t6[b1 ^ ((c >> 8) & 255)]
					^ t5[b2 ^ ((c >> 16) & 255)]
					^ t4[b3 ^ (c >> 24)]
					^ t3[b4]
					^ t2[b5]
					^ t1[b6]
					^ t0[b7]
				)
		else:
			if not _W:
				_word_tables()
			w3, w2, w1, w0 = _W
			for a, b, d, e in iter_unpack("<4H", view[: n - tail]):
				c = w3[a ^ (c & 0xFFFF)] ^ w2[b ^ (c >> 16)] ^ w1[d] ^ w0[e]
		if tail:
			data = view[n - tail :]
	if tail:
		for b in bytes(data):
			c = t0[(c ^ b) & 255] ^ (c >> 8)
	return c ^ 0xFFFFFFFF


def align8(x: int) -> int:
	return (x + 7) & ~7


def decode_payload_length(length_field: int, flags: int) -> int:
	"""Converts the on-disk length field to a byte count."""
	return length_field * LARGE_UNIT if flags & FLAG_LARGE_MODE else length_field


def payload_claim(payload_length: int, flags: int) -> int:
	"""The payload plus its CRC trailer: the bytes a block header promises."""
	return payload_length + 4 if flags & FLAG_HAS_CRC else payload_length


def check_u16(v: int, what: str) -> None:
	if not isinstance(v, int) or v < 0 or v > 0xFFFF:
		raise BinpazerError("range", f"{what} {v!r} is not a u16")


def check_guid(g, what: str) -> None:
	if len(g) != 16:
		raise BinpazerError("range", f"{what} must be 16 bytes, got {len(g)}")


__all__ = [n for n in dir() if n[0] != "_" and n not in ("annotations", "dataclass", "iter_unpack")]
