"""parse_group walks the nested blocks of the payload of a Group block."""

from __future__ import annotations

from dataclasses import dataclass
from struct import unpack_from

from .format import FLAG_HAS_CRC, BinpazerError, align8, crc32c, decode_payload_length, payload_claim


@dataclass(frozen=True, slots=True)
class GroupChild:
	type_id: int
	flags: int
	# offset is the header position of the child, relative to the payload start.
	offset: int
	payload: bytes
	payload_length: int
	# crc_ok is False only when FLAG_HAS_CRC is set and the stored CRC does not match.
	crc_ok: bool


@dataclass(frozen=True, slots=True)
class GroupResult:
	children: list[GroupChild]
	error: BinpazerError | None


def parse_group(payload) -> GroupResult:
	"""Returns the children of a Group payload. A damaged group gives its intact prefix together with the error."""
	data = bytes(payload)
	children: list[GroupChild] = []
	total = len(data)
	pos = 0
	while pos < total:
		rem = total - pos
		if rem < 8:
			if any(data[pos:]):
				return GroupResult(children, BinpazerError("truncated", f"group payload: {rem} trailing byte(s) at offset {pos} are not a block"))
			break
		type_id, flags, length_field = unpack_from("<HHI", data, pos)
		plen = decode_payload_length(length_field, flags)
		claim = payload_claim(plen, flags)
		if claim > rem - 8:
			return GroupResult(children, BinpazerError("truncated", f"group child at offset {pos} claims {claim} bytes but only {rem - 8} remain in the group"))
		body = data[pos + 8 : pos + 8 + plen]
		crc_ok = True
		if flags & FLAG_HAS_CRC:
			crc_ok = crc32c(crc32c(0, data[pos : pos + 8]), body) == unpack_from("<I", data, pos + 8 + plen)[0]
		children.append(GroupChild(type_id, flags, pos, body, plen, crc_ok))
		nxt = align8(pos + 8 + claim)
		if nxt > total:
			break
		pos = nxt
	return GroupResult(children, None)
