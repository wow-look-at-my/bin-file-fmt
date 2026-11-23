"""The Python bench program: fixed workloads whose one-line output the cross-language driver compares.

usage: bench.py [--cpuprofile <file>] write <out> <payload> <blocks> <codec> | read <in> | stream <in> | find <in> <lookups>
"""

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import binpazer as bp  # noqa: E402

TYPE_PAYLOAD = 1
STREAM_CHUNK = 65536
FILE_BUFFER = 65536
CODECS = {"none": 0, "stored": bp.CODEC_STORED, "deflate": bp.CODEC_DEFLATE, "zstd": bp.CODEC_ZSTD, "lz4": bp.CODEC_LZ4}
USAGE = "usage: bench [--cpuprofile <file>] write <out> <payload> <blocks> <codec> | read <in> | stream <in> | find <in> <lookups>"


class Sum:
	"""The FNV-1a checksum over the length and three sample bytes of each block."""

	def __init__(self):
		self.h = 2166136261

	def block(self, length: int, first: int, mid: int, last: int) -> None:
		h = self.h
		n = length & 0xFFFFFFFF
		for b in (n & 255, (n >> 8) & 255, (n >> 16) & 255, n >> 24):
			h = ((h ^ b) * 16777619) & 0xFFFFFFFF
		if length > 0:
			for b in (first, mid, last):
				h = ((h ^ b) * 16777619) & 0xFFFFFFFF
		self.h = h

	def payload(self, p) -> None:
		n = len(p)
		if n == 0:
			self.block(0, 0, 0, 0)
		else:
			self.block(n, p[0], p[n >> 1], p[n - 1])


def count(text: str, what: str) -> int:
	if not text.isascii() or not text.isdigit():
		raise ValueError(f'bench: {what} must be a non-negative integer, got "{text}"')
	return int(text)


def guid(first: int) -> bytes:
	return bytes([first]) + bytes(15)


def write(out: str, payload_path: str, blocks_arg: str, codec: str):
	blocks = count(blocks_arg, "block count")
	if codec not in CODECS:
		raise ValueError(f'bench: unknown codec "{codec}" (want none, stored, deflate, zstd or lz4)')
	codec_id = CODECS[codec]
	payload = Path(payload_path).read_bytes()
	with open(out, "wb", buffering=FILE_BUFFER) as f:
		w = bp.Writer(f, guid(0xB0), "bench", [bp.TypeDef(TYPE_PAYLOAD, guid(0x11), "Payload")])
		if codec_id == 0:
			for _ in range(blocks):
				w.block(TYPE_PAYLOAD, bp.FLAG_HAS_CRC, payload)
		else:
			for _ in range(blocks):
				bp.compress_block(w, TYPE_PAYLOAD, bp.FLAG_HAS_CRC, codec_id, None, payload)
		w.write_index()
		w.end()
	return "write", blocks, blocks * len(payload), None


def read(path: str):
	total = Sum()
	blocks = 0
	size = 0
	with open(path, "rb", buffering=FILE_BUFFER) as f:
		r = bp.Reader(f)
		while (b := r.next()) is not None:
			if b.type_id != TYPE_PAYLOAD:
				r.skip_payload(b)
				continue
			p = bp.read_payload_decompressed(r, b)
			blocks += 1
			size += len(p)
			total.payload(p)
	return "read", blocks, size, total


def stream(path: str):
	total = Sum()
	blocks = 0
	size = 0
	with open(path, "rb", buffering=FILE_BUFFER) as f:
		r = bp.Reader(f)
		while (b := r.next()) is not None:
			if b.type_id != TYPE_PAYLOAD:
				r.skip_payload(b)
				continue
			mid = b.payload_length >> 1
			seen = [0, 0, 0, 0]

			def sink(chunk, seen=seen, mid=mid):
				n = seen[0]
				if n == 0:
					seen[1] = chunk[0]
				if 0 <= mid - n < len(chunk):
					seen[2] = chunk[mid - n]
				seen[3] = chunk[-1]
				seen[0] = n + len(chunk)

			r.stream_payload(b, sink, STREAM_CHUNK)
			blocks += 1
			size += seen[0]
			total.block(*seen)
	return "stream", blocks, size, total


def find(path: str, lookups_arg: str):
	lookups = count(lookups_arg, "lookup count")
	total = Sum()
	size = 0
	with open(path, "rb", buffering=FILE_BUFFER) as f:
		r = bp.Reader(f)
		offsets = r.find(TYPE_PAYLOAD)
		if not offsets:
			raise ValueError("bench: the input has no type-1 blocks")
		for k in range(lookups):
			idx = ((k * 2654435761) & 0xFFFFFFFF) % len(offsets)
			p = bp.read_payload_decompressed(r, r.at(offsets[idx]))
			size += len(p)
			total.payload(p)
	return "find", lookups, size, total


def run(argv: list[str]):
	op, args = (argv[0], argv[1:]) if argv else ("", [])
	if op == "write" and len(args) == 4:
		return write(*args)
	if op == "read" and len(args) == 1:
		return read(args[0])
	if op == "stream" and len(args) == 1:
		return stream(args[0])
	if op == "find" and len(args) == 2:
		return find(*args)
	raise ValueError(USAGE)


def profiled(argv: list[str], out: str):
	"""Runs the operation under cProfile and writes the self time of each function, in milliseconds, as JSON."""
	import cProfile
	import pstats

	prof = cProfile.Profile()
	res = prof.runcall(run, argv)
	self_ms = {}
	for (file, line, name), row in pstats.Stats(prof).stats.items():
		label = name.strip("<>") if file == "~" else f"{Path(file).name}:{name}"
		self_ms[label] = self_ms.get(label, 0.0) + row[2] * 1000
	Path(out).write_text(json.dumps(self_ms))
	return res


def main(argv: list[str]) -> int:
	try:
		if len(argv) >= 2 and argv[0] == "--cpuprofile":
			op, blocks, size, total = profiled(argv[2:], argv[1])
		else:
			op, blocks, size, total = run(argv)
	except bp.BinpazerError as e:
		sys.stderr.write(f"{e} [{e.code}]\n")
		return 1
	except (ValueError, OSError) as e:
		sys.stderr.write(f"{e}\n")
		return 1
	checksum = 0 if total is None else total.h
	sys.stdout.write(f'{{"op":"{op}","blocks":{blocks},"bytes":{size},"checksum":"{checksum:08x}"}}\n')
	return 0


if __name__ == "__main__":
	sys.exit(main(sys.argv[1:]))
