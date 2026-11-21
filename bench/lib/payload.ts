// Deterministic, roughly half-compressible benchmark payload.

export const PAYLOAD_SEED = 0x9e3779b9;
export const LINE_BYTES = 64;
export const RANDOM_BYTES = 24;

// The 40-byte text tail of each line: the phrase, space-padded or trimmed, then a newline.
export function lineText(): Buffer {
	const phrase = "binpazer benchmark payload, repeated...";
	const body = phrase.padEnd(LINE_BYTES - RANDOM_BYTES - 1, " ").slice(0, LINE_BYTES - RANDOM_BYTES - 1);
	return Buffer.from(body + "\n", "ascii");
}

// Each 64-byte line holds many xorshift32 bytes (states, little-endian) and the text tail.
export function generatePayload(size: number): Buffer {
	if (!Number.isInteger(size) || size < 0) throw new Error(`payload size must be a non-negative integer, got ${size}`);
	const lines = Math.ceil(size / LINE_BYTES);
	const out = Buffer.alloc(lines * LINE_BYTES);
	const text = lineText();
	let x = PAYLOAD_SEED;
	for (let l = 0; l < lines; l++) {
		const base = l * LINE_BYTES;
		for (let j = 0; j < RANDOM_BYTES; j += 4) {
			x = (x ^ (x << 13)) >>> 0;
			x = (x ^ (x >>> 17)) >>> 0;
			x = (x ^ (x << 5)) >>> 0;
			out.writeUInt32LE(x, base + j);
		}
		text.copy(out, base + RANDOM_BYTES);
	}
	return out.subarray(0, size);
}
