"""Compression Envelope parse and encode. No codec code lives here."""

from __future__ import annotations

from dataclasses import dataclass
from struct import pack, unpack_from

from .format import CODEC_BY_GUID, CODEC_INVALID, ENVELOPE_FIXED_SIZE, ENVELOPE_GUID_SIZE, BinpazerError, check_guid, check_u16


@dataclass(frozen=True, slots=True)
class Compression:
	codec_id: int
	codec_guid: bytes | None
	uncompressed_length: int
	stored_length: int
	header_size: int


def parse_envelope(payload) -> Compression:
	"""Reads the envelope at the front of the whole stored payload of a block."""
	n = len(payload)
	if n < ENVELOPE_FIXED_SIZE:
		raise BinpazerError("bad_envelope", f"payload holds {n} bytes, the envelope header needs {ENVELOPE_FIXED_SIZE}")
	codec_id, codec_flags, reserved, uncompressed_length = unpack_from("<HHIQ", payload)
	if codec_id == CODEC_INVALID:
		raise BinpazerError("bad_envelope", "codec id 0 is invalid")
	if codec_flags != 0 or reserved != 0:
		raise BinpazerError("bad_envelope", "reserved envelope field set")
	if codec_id != CODEC_BY_GUID:
		return Compression(codec_id, None, uncompressed_length, n - ENVELOPE_FIXED_SIZE, ENVELOPE_FIXED_SIZE)
	if n < ENVELOPE_GUID_SIZE:
		raise BinpazerError("bad_envelope", f"payload holds {n} bytes, the GUID envelope header needs {ENVELOPE_GUID_SIZE}")
	return Compression(codec_id, bytes(payload[16:32]), uncompressed_length, n - ENVELOPE_GUID_SIZE, ENVELOPE_GUID_SIZE)


def encode_envelope_header(codec_id: int, codec_guid: bytes | None, uncompressed_length: int) -> bytes:
	check_u16(codec_id, "codec id")
	if codec_id == CODEC_INVALID:
		raise BinpazerError("range", "codec id 0 is invalid")
	if not isinstance(uncompressed_length, int) or not 0 <= uncompressed_length < 1 << 64:
		raise BinpazerError("range", f"uncompressed length {uncompressed_length!r} is not a u64")
	hdr = pack("<HHIQ", codec_id, 0, 0, uncompressed_length)
	if codec_id != CODEC_BY_GUID:
		return hdr
	if codec_guid is None:
		raise BinpazerError("range", "codec id 65535 needs a codec GUID")
	check_guid(codec_guid, "codec GUID")
	return hdr + bytes(codec_guid)
