// LZ4 frame format codec (encoder and safe decoder) with xxHash32, no dependencies.

const MAGIC = 0x184d2204;
const BLOCK_MAX = 4 << 20;
const HEADER_LEN = 15;
const MIN_MATCH = 4;
const LAST_LITERALS = 5;
const MF_LIMIT = 12;
const MAX_OFFSET = 65535;
const HASH_LOG = 16;

const P1 = 0x9e3779b1;
const P2 = 0x85ebca77;
const P3 = 0xc2b2ae3d;
const P4 = 0x27d4eb2f;
const P5 = 0x165667b1;

const hashTable = new Int32Array(1 << HASH_LOG);

function fail(msg: string): never {
	throw new Error("lz4: " + msg);
}

/** xxHash32 of data with the given seed, as an unsigned 32-bit number. */
export function xxh32(data: Uint8Array, seed = 0): number {
	const len = data.length;
	let p = 0;
	let h: number;
	if (len >= 16) {
		let v1 = (seed + P1 + P2) | 0;
		let v2 = (seed + P2) | 0;
		let v3 = seed | 0;
		let v4 = (seed - P1) | 0;
		const limit = len - 16;
		do {
			let x = data[p] | (data[p + 1] << 8) | (data[p + 2] << 16) | (data[p + 3] << 24);
			v1 = (v1 + Math.imul(x, P2)) | 0;
			v1 = Math.imul((v1 << 13) | (v1 >>> 19), P1);
			x = data[p + 4] | (data[p + 5] << 8) | (data[p + 6] << 16) | (data[p + 7] << 24);
			v2 = (v2 + Math.imul(x, P2)) | 0;
			v2 = Math.imul((v2 << 13) | (v2 >>> 19), P1);
			x = data[p + 8] | (data[p + 9] << 8) | (data[p + 10] << 16) | (data[p + 11] << 24);
			v3 = (v3 + Math.imul(x, P2)) | 0;
			v3 = Math.imul((v3 << 13) | (v3 >>> 19), P1);
			x = data[p + 12] | (data[p + 13] << 8) | (data[p + 14] << 16) | (data[p + 15] << 24);
			v4 = (v4 + Math.imul(x, P2)) | 0;
			v4 = Math.imul((v4 << 13) | (v4 >>> 19), P1);
			p += 16;
		} while (p <= limit);
		h = (((v1 << 1) | (v1 >>> 31)) + ((v2 << 7) | (v2 >>> 25)) + ((v3 << 12) | (v3 >>> 20)) + ((v4 << 18) | (v4 >>> 14))) | 0;
	} else {
		h = (seed + P5) | 0;
	}
	h = (h + len) | 0;
	while (p + 4 <= len) {
		const x = data[p] | (data[p + 1] << 8) | (data[p + 2] << 16) | (data[p + 3] << 24);
		h = (h + Math.imul(x, P3)) | 0;
		h = Math.imul((h << 17) | (h >>> 15), P4);
		p += 4;
	}
	while (p < len) {
		h = (h + Math.imul(data[p], P5)) | 0;
		h = Math.imul((h << 11) | (h >>> 21), P1);
		p++;
	}
	h ^= h >>> 15;
	h = Math.imul(h, P2);
	h ^= h >>> 13;
	h = Math.imul(h, P3);
	h ^= h >>> 16;
	return h >>> 0;
}

/** Worst-case size of a frame that holds srcLen bytes. */
export function lz4FrameBound(srcLen: number): number {
	const blocks = Math.ceil(srcLen / BLOCK_MAX);
	return HEADER_LEN + srcLen + 4 * blocks + 4;
}

function writeU32(dst: Uint8Array, p: number, v: number): void {
	dst[p] = v;
	dst[p + 1] = v >>> 8;
	dst[p + 2] = v >>> 16;
	dst[p + 3] = v >>> 24;
}

function copyBytes(src: Uint8Array, s: number, dst: Uint8Array, d: number, n: number): void {
	if (n < 32) {
		for (let i = 0; i < n; i++) dst[d + i] = src[s + i];
	} else {
		dst.set(src.subarray(s, s + n), d);
	}
}

// Writes the block src[start, end) into dst at op as one LZ4 block.
function compressBlock(src: Uint8Array, start: number, end: number, dst: Uint8Array, op: number, cap: number): number {
	let anchor = start;
	const len = end - start;
	if (len >= MF_LIMIT + 1) {
		hashTable.fill(-1);
		const mflimit = end - MF_LIMIT;
		const matchlimit = end - LAST_LITERALS;
		let ip = start;
		hashTable[Math.imul(src[ip] | (src[ip + 1] << 8) | (src[ip + 2] << 16) | (src[ip + 3] << 24), 2654435761) >>> 16] = ip;
		ip++;
		outer: for (;;) {
			let ref = 0;
			let seq = ip;
			let attempts = 1 << 6;
			for (;;) {
				if (ip > mflimit) break outer;
				seq = src[ip] | (src[ip + 1] << 8) | (src[ip + 2] << 16) | (src[ip + 3] << 24);
				const h = Math.imul(seq, 2654435761) >>> 16;
				ref = hashTable[h];
				hashTable[h] = ip;
				if (ref >= start && ip - ref <= MAX_OFFSET && (src[ref] | (src[ref + 1] << 8) | (src[ref + 2] << 16) | (src[ref + 3] << 24)) === seq) break;
				ip += attempts++ >> 6;
			}
			while (ip > anchor && ref > start && src[ip - 1] === src[ref - 1]) {
				ip--;
				ref--;
			}
			let mlen = MIN_MATCH;
			while (ip + mlen + 4 <= matchlimit) {
				const a = src[ip + mlen] | (src[ip + mlen + 1] << 8) | (src[ip + mlen + 2] << 16) | (src[ip + mlen + 3] << 24);
				const b = src[ref + mlen] | (src[ref + mlen + 1] << 8) | (src[ref + mlen + 2] << 16) | (src[ref + mlen + 3] << 24);
				if (a !== b) break;
				mlen += 4;
			}
			while (ip + mlen < matchlimit && src[ip + mlen] === src[ref + mlen]) mlen++;

			const litLen = ip - anchor;
			const litExtra = litLen >= 15 ? ((litLen - 15) / 255 | 0) + 1 : 0;
			const ml = mlen - MIN_MATCH;
			const mlExtra = ml >= 15 ? ((ml - 15) / 255 | 0) + 1 : 0;
			if (op + 1 + litExtra + litLen + 2 + mlExtra > cap) return -1;
			dst[op++] = ((litLen >= 15 ? 15 : litLen) << 4) | (ml >= 15 ? 15 : ml);
			if (litLen >= 15) {
				let r = litLen - 15;
				for (; r >= 255; r -= 255) dst[op++] = 255;
				dst[op++] = r;
			}
			copyBytes(src, anchor, dst, op, litLen);
			op += litLen;
			const off = ip - ref;
			dst[op++] = off;
			dst[op++] = off >>> 8;
			if (ml >= 15) {
				let r = ml - 15;
				for (; r >= 255; r -= 255) dst[op++] = 255;
				dst[op++] = r;
			}
			ip += mlen;
			anchor = ip;
			if (ip > mflimit) break;
			const p2 = ip - 2;
			hashTable[Math.imul(src[p2] | (src[p2 + 1] << 8) | (src[p2 + 2] << 16) | (src[p2 + 3] << 24), 2654435761) >>> 16] = p2;
		}
	}
	const litLen = end - anchor;
	const litExtra = litLen >= 15 ? ((litLen - 15) / 255 | 0) + 1 : 0;
	if (op + 1 + litExtra + litLen > cap) return -1;
	dst[op++] = (litLen >= 15 ? 15 : litLen) << 4;
	if (litLen >= 15) {
		let r = litLen - 15;
		for (; r >= 255; r -= 255) dst[op++] = 255;
		dst[op++] = r;
	}
	copyBytes(src, anchor, dst, op, litLen);
	return op + litLen;
}

/** Writes one LZ4 frame for src into dst and returns the bytes written. */
export function lz4FrameCompress(src: Uint8Array, dst: Uint8Array): number {
	const n = src.length;
	if (dst.length < HEADER_LEN + 4) throw new RangeError("lz4: destination too small");
	writeU32(dst, 0, MAGIC);
	dst[4] = 0x68; // version 01, independent blocks, content size present
	dst[5] = 0x70; // block max a later size MiB
	writeU32(dst, 6, n >>> 0);
	writeU32(dst, 10, Math.floor(n / 0x100000000));
	dst[14] = (xxh32(dst.subarray(4, 14)) >>> 8) & 0xff;
	let op = HEADER_LEN;
	for (let s = 0; s < n; s += BLOCK_MAX) {
		const e = Math.min(n, s + BLOCK_MAX);
		const blockLen = e - s;
		// Room left for this block's data, keeping the EndMark.
		const avail = dst.length - op - 8;
		if (avail < 0) throw new RangeError("lz4: destination too small");
		const end = compressBlock(src, s, e, dst, op + 4, op + 4 + Math.min(blockLen - 1, avail));
		if (end >= 0) {
			writeU32(dst, op, end - op - 4);
			op = end;
		} else {
			if (blockLen > avail) throw new RangeError("lz4: destination too small");
			writeU32(dst, op, (blockLen | 0x80000000) >>> 0);
			dst.set(src.subarray(s, e), op + 4);
			op += 4 + blockLen;
		}
	}
	if (dst.length - op < 4) throw new RangeError("lz4: destination too small");
	writeU32(dst, op, 0);
	return op + 4;
}

// Decodes the LZ4 block src[sp, send) into dst at dp. Matches may not reach below low.
// Output past blim is malformed; output past dst.length is a RangeError.
function decodeBlock(src: Uint8Array, sp: number, send: number, dst: Uint8Array, dp: number, low: number, blim: number): number {
	const dlen = dst.length;
	for (;;) {
		if (sp >= send) fail("truncated block");
		const token = src[sp++];
		let litLen = token >>> 4;
		if (litLen === 15) {
			let b = 255;
			while (b === 255) {
				if (sp >= send) fail("truncated literal length");
				b = src[sp++];
				litLen += b;
			}
		}
		if (litLen > send - sp) fail("literals run past block end");
		if (dp + litLen > blim) fail("block decodes past block max size");
		if (dp + litLen > dlen) throw new RangeError("lz4: destination too small");
		copyBytes(src, sp, dst, dp, litLen);
		sp += litLen;
		dp += litLen;
		if (sp === send) return dp;
		if (send - sp < 2) fail("truncated match offset");
		const off = src[sp] | (src[sp + 1] << 8);
		sp += 2;
		if (off === 0) fail("match offset 0");
		if (off > dp - low) fail("match offset before start of data");
		let mlen = token & 15;
		if (mlen === 15) {
			let b = 255;
			while (b === 255) {
				if (sp >= send) fail("truncated match length");
				b = src[sp++];
				mlen += b;
			}
		}
		mlen += MIN_MATCH;
		if (dp + mlen > blim) fail("block decodes past block max size");
		if (dp + mlen > dlen) throw new RangeError("lz4: destination too small");
		let m = dp - off;
		if (off >= mlen && mlen >= 32) {
			dst.copyWithin(dp, m, m + mlen);
			dp += mlen;
		} else {
			const e = dp + mlen;
			while (dp < e) dst[dp++] = dst[m++];
		}
	}
}

function readU32(src: Uint8Array, p: number): number {
	return (src[p] | (src[p + 1] << 8) | (src[p + 2] << 16) | (src[p + 3] << 24)) >>> 0;
}

/** Decodes exactly one LZ4 frame that spans all of src into dst; returns the bytes written. */
export function lz4FrameDecompress(src: Uint8Array, dst: Uint8Array): number {
	const len = src.length;
	if (len < 7) fail("truncated frame header");
	const magic = readU32(src, 0);
	if ((magic & 0xfffffff0) >>> 0 === 0x184d2a50) fail("skippable frames are not supported");
	if (magic !== MAGIC) fail("bad magic");
	const flg = src[4];
	const bd = src[5];
	if (flg >>> 6 !== 1) fail("unsupported frame version");
	if (flg & 0x02) fail("reserved FLG bit set");
	if (bd & 0x8f) fail("reserved BD bits set");
	const bsid = (bd >>> 4) & 7;
	if (bsid < 4) fail("invalid block max size id");
	const blockMax = 1 << (8 + 2 * bsid);
	const independent = (flg & 0x20) !== 0;
	const blockSum = (flg & 0x10) !== 0;
	const hasSize = (flg & 0x08) !== 0;
	const contentSum = (flg & 0x04) !== 0;
	if (flg & 0x01) fail("dictionaries are not supported");
	let pos = 6;
	let contentSize = -1;
	if (hasSize) {
		if (len < pos + 9) fail("truncated frame header");
		const hi = readU32(src, pos + 4);
		if (hi >= 0x200000) fail("content size too large");
		contentSize = hi * 0x100000000 + readU32(src, pos);
		pos += 8;
	}
	if (src[pos] !== ((xxh32(src.subarray(4, pos)) >>> 8) & 0xff)) fail("header checksum mismatch");
	pos++;
	let dp = 0;
	for (;;) {
		if (len - pos < 4) fail("truncated block size");
		const word = readU32(src, pos);
		pos += 4;
		if (word === 0) break;
		const size = word & 0x7fffffff;
		if (size > blockMax) fail("block size exceeds block max size");
		if (size > len - pos) fail("truncated block data");
		if (word & 0x80000000) {
			if (dp + size > dst.length) throw new RangeError("lz4: destination too small");
			dst.set(src.subarray(pos, pos + size), dp);
			dp += size;
		} else {
			dp = decodeBlock(src, pos, pos + size, dst, dp, independent ? dp : 0, dp + blockMax);
		}
		if (blockSum) {
			if (len - pos - size < 4) fail("truncated block checksum");
			if (readU32(src, pos + size) !== xxh32(src.subarray(pos, pos + size))) fail("block checksum mismatch");
			pos += 4;
		}
		pos += size;
	}
	if (contentSum) {
		if (len - pos < 4) fail("truncated content checksum");
		if (readU32(src, pos) !== xxh32(dst.subarray(0, dp))) fail("content checksum mismatch");
		pos += 4;
	}
	if (pos !== len) fail("trailing bytes after frame");
	if (hasSize && dp !== contentSize) fail("content size mismatch");
	return dp;
}
