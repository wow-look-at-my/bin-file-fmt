// Derives the carry-less multiply constants of the CRC-32C fast paths in
// c/binpazer.c and checks each on random inputs.

const POLY = 0x82f63b78;
const TABLE = new Uint32Array(256);
for (let i = 0; i < 256; i++) {
	let c = i;
	for (let k = 0; k < 8; k++) c = c & 1 ? (c >>> 1) ^ POLY : c >>> 1;
	TABLE[i] = c >>> 0;
}

// The raw CRC update: no inversion of the state.
function crcBytes(c, bytes) {
	for (const b of bytes) c = (TABLE[(c ^ b) & 255] ^ (c >>> 8)) >>> 0;
	return c;
}

function le(v, n) {
	const out = new Uint8Array(n);
	for (let i = 0; i < n; i++) out[i] = Number((v >> BigInt(8 * i)) & 0xffn);
	return out;
}

function clmul(a, b) {
	let r = 0n;
	for (let i = 0n; i < 64n; i++) if ((b >> i) & 1n) r ^= a << i;
	return r;
}

// Solves f(k) = target for a 32-bit k, where f is linear over GF(2).
function solve(f, target) {
	const rows = [];
	const cols = [];
	for (let i = 0; i < 32; i++) cols.push(f(BigInt(2 ** i)));
	for (let bit = 0; bit < 32; bit++) {
		let coeff = 0;
		for (let i = 0; i < 32; i++) if ((cols[i] >>> bit) & 1) coeff += 2 ** i;
		rows.push({ coeff, rhs: (target >>> bit) & 1 });
	}
	const has = (v, i) => Math.floor(v / 2 ** i) % 2 === 1;
	const xor = (a, b) => Number(BigInt(a) ^ BigInt(b));
	const pivots = [];
	let r = 0;
	for (let col = 0; col < 32 && r < 32; col++) {
		const p = rows.findIndex((row, idx) => idx >= r && has(row.coeff, col));
		if (p < 0) continue;
		[rows[r], rows[p]] = [rows[p], rows[r]];
		for (let j = 0; j < 32; j++) {
			if (j !== r && has(rows[j].coeff, col)) rows[j] = { coeff: xor(rows[j].coeff, rows[r].coeff), rhs: rows[j].rhs ^ rows[r].rhs };
		}
		pivots.push(col);
		r++;
	}
	if (r !== 32) throw new Error("the map is singular");
	let k = 0;
	pivots.forEach((col, idx) => {
		if (rows[idx].rhs) k += 2 ** col;
	});
	return BigInt(k);
}

let seed = 0x1234567n;
function rand64() {
	seed = (seed * 6364136223846793005n + 1442695040888963407n) & 0xffffffffffffffffn;
	return seed ^ (seed >> 29n);
}

const hex32 = (k) => "0x" + k.toString(16).padStart(8, "0");

function shiftConstant(n) {
	const k = solve((v) => crcBytes(0, le(v, 8)), crcBytes(1, new Uint8Array(n)));
	for (let i = 0; i < 200; i++) {
		const s = Number(rand64() & 0xffffffffn);
		const want = crcBytes(s, new Uint8Array(n));
		const got = crcBytes(0, le(clmul(BigInt(s), k), 8));
		if (want !== got) throw new Error(`shift constant for ${n} bytes fails on state ${s}`);
	}
	return k;
}

function foldConstants(distance) {
	const tail = new Uint8Array(distance);
	const contribution = (lo, hi) => crcBytes(crcBytes(0, le(lo | (hi << 64n), 16)), tail);
	const f = (v) => crcBytes(0, le(v, 16));
	const kLo = solve(f, contribution(1n, 0n));
	const kHi = solve(f, contribution(0n, 1n));
	for (let i = 0; i < 200; i++) {
		const lo = rand64();
		const hi = rand64();
		const folded = clmul(lo, kLo) ^ clmul(hi, kHi);
		if (crcBytes(0, le(folded, 16)) !== contribution(lo, hi)) throw new Error(`fold constants for ${distance} bytes fail`);
	}
	return [kLo, kHi];
}

for (const n of [32, 128, 2048]) {
	console.log(`3-way, ${n} bytes per stream: past 2 streams ${hex32(shiftConstant(2 * n))}, past 1 stream ${hex32(shiftConstant(n))}`);
}
for (const d of [1024, 512, 256, 64]) {
	const [kLo, kHi] = foldConstants(d);
	console.log(`fold by ${d} bytes: low half ${hex32(kLo)}, high half ${hex32(kHi)}`);
}
