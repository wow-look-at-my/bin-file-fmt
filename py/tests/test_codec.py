"""The codec layer, and live C <-> Python interop for every registry codec through c/interop.c."""

import shutil
import subprocess
import tempfile
from pathlib import Path

from .support import REPO_ROOT, bp, code_of, guid_of, io, pattern, unittest

CODECS = (("stored", bp.CODEC_STORED), ("deflate", bp.CODEC_DEFLATE), ("zstd", bp.CODEC_ZSTD), ("lz4", bp.CODEC_LZ4))
TEXT = b"the quick brown fox jumps over the lazy dog. " * 3000
INPUTS = (b"", b"a", b"abc" * 5, TEXT, pattern(100_000, 11), TEXT[:70_000] + pattern(70_000, 4))


def envelope_for(codec_id: int, n: int) -> bp.Compression:
	return bp.Compression(codec_id, None, n, 0, 16)


class CodecLayer(unittest.TestCase):
	def test_every_registry_codec_is_available(self):
		for name, codec_id in CODECS:
			self.assertIsNotNone(bp.lookup_codec(codec_id), name)
		self.assertIsNone(bp.lookup_codec(5))
		self.assertIsNone(bp.lookup_codec(bp.CODEC_BY_GUID, guid_of(9)))
		self.assertEqual(code_of(self, lambda: bp.compress(5, None, b"x")), "codec_unavailable")

	def test_round_trip_through_the_caller_buffer_api(self):
		for name, codec_id in CODECS:
			for data in INPUTS:
				label = f"{name} {len(data)}"
				dst = bytearray(bp.compress_bound(codec_id, None, len(data)))
				n = bp.compress_into(codec_id, None, data, dst)
				self.assertLessEqual(n, len(dst), label)
				out = bytearray(len(data) + 7)
				bp.decompress_into(envelope_for(codec_id, len(data)), memoryview(dst)[:n], out)
				self.assertEqual(bytes(out[: len(data)]), data, label)
				self.assertEqual(bytes(out[len(data) :]), bytes(7), f"{label}: nothing lands past the declared length")
				self.assertEqual(bytes(bp.decompress(envelope_for(codec_id, len(data)), bp.compress(codec_id, None, bytearray(data)))), data, label)

	def test_real_codecs_shrink_text(self):
		for name, codec_id in CODECS[1:]:
			self.assertLess(len(bp.compress(codec_id, None, TEXT)), len(TEXT) // 10, name)
		self.assertEqual(bytes(bp.compress(bp.CODEC_STORED, None, TEXT)), TEXT)

	def test_the_bound_covers_incompressible_input(self):
		for name, codec_id in CODECS:
			for n in (0, 1, 100, 4096, 65_537, 300_000):
				data = pattern(n, n + 1)
				self.assertLessEqual(len(bp.compress(codec_id, None, data)), bp.compress_bound(codec_id, None, n), f"{name} {n}")

	def test_a_destination_that_is_too_small_gives_short_buffer(self):
		data = pattern(5000, 2)
		for name, codec_id in CODECS:
			self.assertEqual(code_of(self, lambda: bp.compress_into(codec_id, None, data, bytearray(100))), "short_buffer", name)
			self.assertEqual(code_of(self, lambda: bp.compress_into(codec_id, None, data, bytearray(0))), "short_buffer", name)
			packed = bp.compress(codec_id, None, data)
			self.assertEqual(code_of(self, lambda: bp.decompress_into(envelope_for(codec_id, 5000), packed, bytearray(4999))), "short_buffer", name)
		self.assertEqual(code_of(self, lambda: bp.compress_into(bp.CODEC_STORED, None, data, bytes(5000))), "range")

	def test_a_decode_that_differs_from_the_declared_length_is_corruption(self):
		data = TEXT[:9000]
		for name, codec_id in CODECS:
			packed = bytes(bp.compress(codec_id, None, data))
			for declared in (0, 1, 8999, 9001, 20_000):
				self.assertEqual(code_of(self, lambda: bp.decompress(envelope_for(codec_id, declared), packed)), "format", f"{name} declared {declared}")

	def test_a_corrupt_stream_gives_format(self):
		data = TEXT[:9000]
		for name, codec_id in CODECS[1:]:
			packed = bytes(bp.compress(codec_id, None, data))
			for bad in (packed[: len(packed) // 2], b"", b"\x00" * 40, b"\xff" * 40):
				self.assertEqual(code_of(self, lambda: bp.decompress(envelope_for(codec_id, 9000), bad)), "format", f"{name} {len(bad)}")

	def test_a_bomb_does_not_allocate_past_the_reader_gate(self):
		mem = io.BytesIO()
		w = bp.Writer(mem, guid_of(1), "bomb", [])
		w.block_compressed(1, 0, bp.CODEC_DEFLATE, None, bp.compress(bp.CODEC_DEFLATE, None, bytes(1000)), 1 << 50)
		w.end()
		rd = bp.Reader(io.BytesIO(mem.getvalue()), 1 << 20)
		blk = rd.next()
		self.assertEqual(code_of(self, lambda: bp.read_payload_decompressed(rd, blk)), "capacity")

	def test_a_damaged_envelope_gives_bad_envelope(self):
		good = bp.encode_envelope_header(bp.CODEC_ZSTD, None, 5)
		self.assertEqual(bp.parse_envelope(good + b"12345"), bp.Compression(bp.CODEC_ZSTD, None, 5, 5, 16))
		for bad in (good[:15], bytes(16), good[:2] + b"\x01" + good[3:], good[:4] + b"\x01" + good[5:]):
			self.assertEqual(code_of(self, lambda: bp.parse_envelope(bad)), "bad_envelope")
		by_guid = bp.encode_envelope_header(bp.CODEC_BY_GUID, guid_of(7), 0)
		self.assertEqual(len(by_guid), 32)
		self.assertEqual(code_of(self, lambda: bp.parse_envelope(by_guid[:31])), "bad_envelope")
		self.assertEqual(code_of(self, lambda: bp.encode_envelope_header(0, None, 1)), "range")
		self.assertEqual(code_of(self, lambda: bp.encode_envelope_header(bp.CODEC_BY_GUID, None, 1)), "range")
		self.assertEqual(code_of(self, lambda: bp.encode_envelope_header(1, None, -1)), "range")

	def test_a_registered_codec_serves_its_id_and_its_guid(self):
		class Reverse(bp.Codec):
			def compress_into(self, src, dst):
				dst[: len(src)] = bytes(src)[::-1]
				return len(src)

			def decompress_into(self, src, dst):
				dst[:] = bytes(src)[::-1]

			def bound(self, src_len):
				return src_len

		custom = guid_of(0xC0, 0xDE)
		bp.register_codec(900, Reverse())
		bp.register_codec_guid(custom, Reverse())
		self.assertEqual(code_of(self, lambda: bp.register_codec(0, Reverse())), "range")
		self.assertEqual(code_of(self, lambda: bp.register_codec(bp.CODEC_BY_GUID, Reverse())), "range")
		mem = io.BytesIO()
		w = bp.Writer(mem, guid_of(1), "custom", [])
		bp.compress_block(w, 1, bp.FLAG_HAS_CRC, 900, None, b"forward")
		bp.compress_block(w, 1, 0, bp.CODEC_BY_GUID, custom, b"by guid")
		self.assertEqual(code_of(self, lambda: bp.compress_block(w, 1, 0, bp.CODEC_BY_GUID, None, b"x")), "range")
		w.end()
		rd = bp.Reader(io.BytesIO(mem.getvalue()))
		first = rd.next()
		raw = rd.read_payload(first)
		self.assertEqual(raw[16:], b"drawrof")
		self.assertEqual(bytes(bp.read_payload_decompressed(rd, rd.next())), b"by guid")


class Interop(unittest.TestCase):
	@classmethod
	def setUpClass(cls):
		cls.dir = Path(tempfile.mkdtemp(prefix="binpazer-interop-"))
		cls.harness = str(cls.dir / "interop")
		c = REPO_ROOT / "c"
		build = ["cc", "-std=c11", "-O2", str(c / "binpazer.c"), str(c / "binpazer_codec.c"), str(c / "interop.c"), "-o", cls.harness, "-lz", "-lzstd", "-llz4"]
		subprocess.run(build, check=True, capture_output=True)
		subprocess.run([cls.harness, "payload", str(cls.dir / "payload.bin")], check=True, capture_output=True)
		subprocess.run([cls.harness, "write", str(cls.dir)], check=True, capture_output=True)
		cls.payload = (cls.dir / "payload.bin").read_bytes()

	@classmethod
	def tearDownClass(cls):
		shutil.rmtree(cls.dir, ignore_errors=True)

	def test_the_c_harness_produced_the_canonical_payload(self):
		self.assertEqual(len(self.payload), 8192)

	def test_python_decodes_the_c_files(self):
		for name, _ in CODECS:
			with open(self.dir / f"c-{name}.binpazer", "rb") as src:
				rd = bp.Reader(src)
				self.assertEqual(rd.writer_name, "interop")
				blk = rd.next()
				self.assertEqual(blk.type_id, 1)
				self.assertEqual(bytes(bp.read_payload_decompressed(rd, blk)), self.payload, name)

	def c_reads(self, path: Path) -> str:
		return subprocess.run([self.harness, "read", str(path)], check=True, capture_output=True, text=True).stdout

	def test_c_decodes_the_python_files(self):
		defs = [bp.TypeDef(1, guid_of(0x11), "Payload")]
		for name, codec_id in CODECS:
			path = self.dir / f"py-{name}.binpazer"
			with open(path, "wb") as out:
				w = bp.Writer(out, guid_of(0x01), "interop", defs)
				bp.compress_block(w, 1, bp.FLAG_HAS_CRC, codec_id, None, self.payload)
				w.end()
			self.assertRegex(self.c_reads(path), r"^ok ", name)

			piped = self.dir / f"py-pipe-{name}.binpazer"
			with open(piped, "wb", buffering=0) as raw:
				raw.seekable = lambda: False
				w = bp.Writer(raw, guid_of(0x01), "interop", defs, version_minor=bp.VERSION_MINOR_COMPRESSION)
				bp.compress_block(w, 1, bp.FLAG_HAS_CRC, codec_id, None, self.payload)
				w.end()
			self.assertRegex(self.c_reads(piped), r"^ok ", f"{name} through an output that cannot seek")
