"""Test support: stream wrappers, the shared fixtures, and the sample writers of c/gen.c."""

from __future__ import annotations

import io
import unittest  # noqa: F401 -- the test modules import it from here
from pathlib import Path

import binpazer as bp

REPO_ROOT = Path(__file__).resolve().parents[2]
TESTDATA = REPO_ROOT / "go" / "testdata"


def guid_of(*head: int) -> bytes:
	return bytes(head) + bytes(16 - len(head))


SAMPLE_WRITER = guid_of(0xAA, 0xBB, 0xCC, 0xDD, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1)
SAMPLE_MESH = guid_of(0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00)
SAMPLE_NOTE = guid_of(0xDE, 0xAD, 0xBE, 0xEF)
SAMPLE_CODEC = guid_of(0xC0, 0xDE, 0xC0, 0xDE, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7)
MESH_PAYLOAD = b"vertices-go-here"
NOTE_PAYLOAD = b"hello, file!"
BIG_PAYLOAD = b"0123456789ABCDEF01234567"
NOTE_DEFS = [bp.TypeDef(2, SAMPLE_NOTE, "Note")]


def fixture(name: str) -> bytes:
	return (TESTDATA / name).read_bytes()


class Pipe:
	"""A reader with no seek, like a pipe. It hands out at most `step` bytes per read."""

	def __init__(self, data: bytes, step: int = 1 << 30):
		self._f = io.BytesIO(data)
		self._step = step

	def read(self, n: int = -1) -> bytes:
		return self._f.read(min(n, self._step) if n >= 0 else self._step)

	def seekable(self) -> bool:
		return False


class PipeSink:
	"""A writer with no seek, like a pipe. It counts the flush calls it receives."""

	def __init__(self):
		self.mem = io.BytesIO()
		self.flushes = 0

	def write(self, data) -> int:
		return self.mem.write(data)

	def flush(self) -> None:
		self.flushes += 1

	def seekable(self) -> bool:
		return False


class LiveView:
	"""A reader with its own position over the live bytes of a BytesIO, as a second process sees a file."""

	def __init__(self, f: io.BytesIO):
		self._f = f
		self._at = 0

	def read(self, n: int = -1) -> bytes:
		with self._f.getbuffer() as v:
			data = bytes(v[self._at :] if n < 0 else v[self._at : self._at + n])
		self._at += len(data)
		return data

	def seek(self, to: int, whence: int = 0) -> int:
		self._at = to if whence == 0 else len(self._f.getbuffer()) + to
		return self._at

	def tell(self) -> int:
		return self._at

	def seekable(self) -> bool:
		return True


def seekable(data: bytes) -> io.BytesIO:
	return io.BytesIO(data)


def streaming(data: bytes) -> Pipe:
	return Pipe(data)


def write_sample(f) -> None:
	"""Reproduces the main fixture of c/gen.c."""
	w = bp.Writer(f, SAMPLE_WRITER, "demo", [bp.TypeDef(1, SAMPLE_MESH, "Mesh"), bp.TypeDef(2, SAMPLE_NOTE, "Note")])
	w.block(1, bp.FLAG_CRITICAL | bp.FLAG_HAS_CRC, MESH_PAYLOAD)
	w.block(2, 0, NOTE_PAYLOAD)
	w.block(1, 0, BIG_PAYLOAD)
	w.end()


def write_compressed_sample(f) -> None:
	"""Reproduces the compressed fixture of c/gen.c."""
	w = bp.Writer(f, SAMPLE_WRITER, "demo", NOTE_DEFS)
	w.block(2, 0, b"plain block")
	w.block_compressed(2, bp.FLAG_HAS_CRC, bp.CODEC_STORED, None, b"compressed, sort of", 19)
	w.block_compressed(2, 0, bp.CODEC_BY_GUID, SAMPLE_CODEC, b"guid-codec payload", 18)
	w.end()


def sample_bytes() -> bytearray:
	f = io.BytesIO()
	write_sample(f)
	return bytearray(f.getvalue())


def pattern(n: int, seed: int = 1) -> bytes:
	"""Returns deterministic pseudo-random bytes."""
	out = bytearray(n)
	x = seed
	for i in range(n):
		x = (x * 1103515245 + 12345) & 0xFFFFFFFF
		out[i] = x >> 24
	return bytes(out)


def rng(seed: int):
	state = [seed & 0xFFFFFFFF or 1]

	def nxt(n: int) -> int:
		s = state[0]
		s ^= (s << 13) & 0xFFFFFFFF
		s ^= s >> 17
		s ^= (s << 5) & 0xFFFFFFFF
		state[0] = s
		return s % n

	return nxt


def code_of(test, call) -> str:
	"""Runs call, requires a BinpazerError, and returns its code."""
	with test.assertRaises(bp.BinpazerError) as caught:
		call()
	return caught.exception.code
