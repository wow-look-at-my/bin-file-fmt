// Compression Envelope parse and encode; no codec code lives here.

import {
	BinpazerError,
	CODEC_BY_GUID,
	CODEC_INVALID,
	ENVELOPE_FIXED_SIZE,
	ENVELOPE_GUID_SIZE,
	checkGuid,
	checkU16,
	getU16,
	getU32,
	getU64,
	putU16,
	putU64,
} from "./format.ts";
import type { Guid } from "./format.ts";

export interface Compression {
	codecId: number;
	codecGuid: Guid | null;
	uncompressedLength: number;
	storedLength: number;
	headerSize: number;
}

// parseEnvelope reads the envelope at the front of a block's whole stored payload.
export function parseEnvelope(payload: Uint8Array): Compression {
	if (payload.length < ENVELOPE_FIXED_SIZE) {
		throw new BinpazerError("bad_envelope", `payload holds ${payload.length} bytes, the envelope header needs ${ENVELOPE_FIXED_SIZE}`);
	}
	const codecId = getU16(payload, 0);
	if (codecId === CODEC_INVALID) {
		throw new BinpazerError("bad_envelope", "codec id 0 is invalid");
	}
	if (getU16(payload, 2) !== 0 || getU32(payload, 4) !== 0) {
		throw new BinpazerError("bad_envelope", "reserved envelope field set");
	}
	const uncompressedLength = getU64(payload, 8, "uncompressed_length");
	if (uncompressedLength === null) {
		throw new BinpazerError("range", "uncompressed_length does not fit a safe integer");
	}
	let headerSize = ENVELOPE_FIXED_SIZE;
	let codecGuid: Guid | null = null;
	if (codecId === CODEC_BY_GUID) {
		if (payload.length < ENVELOPE_GUID_SIZE) {
			throw new BinpazerError("bad_envelope", `payload holds ${payload.length} bytes, the GUID envelope header needs ${ENVELOPE_GUID_SIZE}`);
		}
		codecGuid = payload.slice(16, 32);
		headerSize = ENVELOPE_GUID_SIZE;
	}
	return { codecId, codecGuid, uncompressedLength, storedLength: payload.length - headerSize, headerSize };
}

export function encodeEnvelopeHeader(codecId: number, codecGuid: Guid | null, uncompressedLength: number): Uint8Array {
	checkU16(codecId, "codec id");
	if (codecId === CODEC_INVALID) {
		throw new BinpazerError("range", "codec id 0 is invalid");
	}
	if (!Number.isSafeInteger(uncompressedLength) || uncompressedLength < 0) {
		throw new BinpazerError("range", `uncompressed length ${uncompressedLength} is not a safe non-negative integer`);
	}
	const byGuid = codecId === CODEC_BY_GUID;
	const hdr = new Uint8Array(byGuid ? ENVELOPE_GUID_SIZE : ENVELOPE_FIXED_SIZE);
	putU16(hdr, 0, codecId);
	putU64(hdr, 8, uncompressedLength);
	if (byGuid) {
		if (codecGuid === null) {
			throw new BinpazerError("range", "codec id 65535 needs a codec GUID");
		}
		checkGuid(codecGuid, "codec GUID");
		hdr.set(codecGuid, 16);
	}
	return hdr;
}
