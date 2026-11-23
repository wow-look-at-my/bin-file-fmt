"""Shared binpazer constants, GUID helpers, CRC-32C and the error type."""

from __future__ import annotations

from dataclasses import dataclass

from ._native import crc32c

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


__all__ = [n for n in dir() if n[0] != "_" and n not in ("annotations", "dataclass")]
