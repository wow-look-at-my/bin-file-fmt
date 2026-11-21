// parseGroup walks the nested blocks of a Group block's payload.

import { BinpazerError, FLAG_HAS_CRC, align8, blockHeaderBytes, crc32c, decodePayloadLength, getU16, getU32, payloadClaim } from "./format.ts";

export interface GroupChild {
	typeId: number;
	flags: number;
	// offset is the child's header position relative to the payload start.
	offset: number;
	// payload is a view into the parsed buffer, not a copy.
	payload: Uint8Array;
	payloadLength: number;
	// crcOk is false only when FLAG_HAS_CRC is set and the stored CRC mismatches.
	crcOk: boolean;
}

export interface GroupResult {
	children: GroupChild[];
	error: BinpazerError | null;
}

// parseGroup returns the intact prefix of a damaged group together with the error.
export function parseGroup(payload: Uint8Array): GroupResult {
	const children: GroupChild[] = [];
	const total = payload.length;
	let pos = 0;
	while (pos < total) {
		const rem = total - pos;
		if (rem < 8) {
			for (let i = pos; i < total; i++) {
				if (payload[i] !== 0) {
					return { children, error: new BinpazerError("truncated", `group payload: ${rem} trailing byte(s) at offset ${pos} are not a block`) };
				}
			}
			break;
		}
		const typeId = getU16(payload, pos);
		const flags = getU16(payload, pos + 2);
		const plen = decodePayloadLength(getU32(payload, pos + 4), flags);
		const claim = payloadClaim(plen, flags);
		if (claim > rem - 8) {
			return {
				children,
				error: new BinpazerError("truncated", `group child at offset ${pos} claims ${claim} bytes but only ${rem - 8} remain in the group`),
			};
		}
		const body = payload.subarray(pos + 8, pos + 8 + plen);
		let crcOk = true;
		if (flags & FLAG_HAS_CRC) {
			crcOk = crc32c(crc32c(0, blockHeaderBytes(typeId, flags, plen)), body) === getU32(payload, pos + 8 + plen);
		}
		children.push({ typeId, flags, offset: pos, payload: body, payloadLength: plen, crcOk });
		const next = align8(pos + 8 + claim);
		if (next > total) {
			break;
		}
		pos = next;
	}
	return { children, error: null };
}
