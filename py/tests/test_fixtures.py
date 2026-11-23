"""The Python writer reproduces the C fixtures byte for byte, and the Python reader reads them back."""

import io
import unittest

import binpazer as bp

from . import support as s


class Fixtures(unittest.TestCase):
	def test_writer_reproduces_sample(self):
		f = io.BytesIO()
		s.write_sample(f)
		want = s.fixture("sample.binpazer")
		self.assertEqual(len(want), 280)
		self.assertEqual(f.getvalue(), want)

	def test_writer_reproduces_sample_compressed(self):
		f = io.BytesIO()
		s.write_compressed_sample(f)
		self.assertEqual(f.getvalue(), s.fixture("sample-compressed.binpazer"))

	def test_a_pipe_gets_the_same_bytes_with_the_streaming_sentinel(self):
		pipe = s.PipeSink()
		s.write_sample(pipe)
		want = bytearray(s.fixture("sample.binpazer"))
		want[32:40] = b"\xff" * 8
		self.assertEqual(pipe.mem.getvalue(), bytes(want))

	def verify_sample(self, src):
		r = bp.Reader(src)
		self.assertEqual((r.version_major, r.version_minor), (1, 0))
		self.assertEqual(r.writer_name, "demo")
		self.assertEqual(r.writer_guid, s.SAMPLE_WRITER)
		self.assertEqual(r.file_length, 280)
		self.assertEqual(r.first_block_offset, 72)
		self.assertFalse(r.has_index)
		self.assertEqual(r.types, [
			bp.TypeEntry(bp.TYPE_BLOCK_TYPE_TABLE, bp.BLOCK_TYPE_TABLE_GUID, "BlockTypeTable"),
			bp.TypeEntry(1, s.SAMPLE_MESH, "Mesh"),
			bp.TypeEntry(2, s.SAMPLE_NOTE, "Note"),
		])
		want = [
			(1, bp.FLAG_CRITICAL | bp.FLAG_HAS_CRC, s.SAMPLE_MESH, "Mesh", s.MESH_PAYLOAD, 192),
			(2, 0, s.SAMPLE_NOTE, "Note", s.NOTE_PAYLOAD, 224),
			(1, 0, s.SAMPLE_MESH, "Mesh", s.BIG_PAYLOAD, 248),
		]
		for type_id, flags, guid, name, payload, offset in want:
			b = r.next()
			self.assertEqual(b, bp.Block(type_id, flags, len(payload), offset + 8, offset, guid, True, name))
			self.assertEqual(r.read_payload(b), payload)
		self.assertIsNone(r.next())

	def test_reader_reads_sample_seekable_and_streaming(self):
		data = s.fixture("sample.binpazer")
		self.verify_sample(s.seekable(data))
		self.verify_sample(s.streaming(data))
		self.verify_sample(s.Pipe(data, step=3))

	def test_reader_reads_sample_compressed(self):
		data = s.fixture("sample-compressed.binpazer")
		r = bp.Reader(s.seekable(data))
		self.assertEqual(r.version_minor, 1)
		self.assertEqual(r.file_length, len(data))
		self.assertEqual(len(r.types), 2)

		plain = r.next()
		self.assertEqual(plain.flags, 0)
		self.assertEqual(bp.read_payload_decompressed(r, plain), b"plain block")

		stored = r.next()
		self.assertEqual(stored.flags, bp.FLAG_HAS_CRC | bp.FLAG_COMPRESSED)
		raw = r.read_payload(stored)
		env = bp.parse_envelope(raw)
		self.assertEqual((env.codec_id, env.uncompressed_length, env.stored_length), (bp.CODEC_STORED, 19, 19))
		self.assertEqual(raw[env.header_size :], b"compressed, sort of")

		by_guid = r.next()
		self.assertEqual(by_guid.flags, bp.FLAG_COMPRESSED)
		raw2 = r.read_payload(by_guid)
		env2 = bp.parse_envelope(raw2)
		self.assertEqual((env2.codec_id, env2.codec_guid, env2.header_size), (bp.CODEC_BY_GUID, s.SAMPLE_CODEC, 32))
		self.assertEqual(raw2[32:], b"guid-codec payload")
		self.assertIsNone(r.next())

	def test_a_stored_block_decodes_through_the_codec_layer(self):
		r = bp.Reader(s.seekable(s.fixture("sample-compressed.binpazer")))
		r.skip_payload(r.next())
		self.assertEqual(bytes(bp.read_payload_decompressed(r, r.next())), b"compressed, sort of")
		by_guid = r.next()
		self.assertEqual(s.code_of(self, lambda: bp.read_payload_decompressed(r, by_guid)), "codec_unavailable")


class Roundtrip(unittest.TestCase):
	def build(self, f, index=True):
		w = bp.Writer(f, s.SAMPLE_WRITER, "roundtrip", [bp.TypeDef(1, s.SAMPLE_MESH, "Mesh"), bp.TypeDef(7, s.SAMPLE_NOTE, "")])
		big = s.pattern(200_000, 5)
		w.block(1, bp.FLAG_HAS_CRC, big)
		w.block(7, 0, b"")
		w.block(1, bp.FLAG_LARGE_MODE | bp.FLAG_HAS_CRC, bytes(8192))
		w.block(9, bp.FLAG_SAFE_TO_COPY, memoryview(b"unlisted type"))
		w.block(1, 0, bytearray(b"last"))
		if index:
			w.write_index()
		w.end()
		return big

	def test_index_find_at_and_streaming_agree(self):
		f = io.BytesIO()
		big = self.build(f)
		data = f.getvalue()
		r = bp.Reader(s.seekable(data))
		self.assertTrue(r.has_index)
		self.assertEqual(r.file_length, len(data))
		offsets = r.find(1)
		self.assertEqual(len(offsets), 3)
		self.assertEqual(r.read_payload(r.at(offsets[2])), b"last")
		self.assertEqual(r.read_payload(r.at(offsets[0])), big)
		self.assertEqual(r.find(9), [r.find(1)[1] + bp.align8(8 + 8192 + 4)])
		self.assertEqual(r.find(1234), [])

		for src in (s.seekable(data), s.streaming(data), s.Pipe(data, step=1000)):
			r = bp.Reader(src)
			seen = []
			while (b := r.next()) is not None:
				if b.type_id == 9:
					self.assertFalse(b.known)
					self.assertIsNone(b.guid)
				chunks = []
				if len(seen) % 2 == 0:
					r.stream_payload(b, chunks.append, 4096)
					seen.append((b.type_id, b"".join(chunks)))
				else:
					dst = bytearray(b.payload_length + 3)
					n = r.read_payload_into(b, dst)
					seen.append((b.type_id, bytes(dst[:n])))
			self.assertEqual(seen, [(1, big), (7, b""), (1, bytes(8192)), (9, b"unlisted type"), (1, b"last"), (bp.TYPE_BLOCK_INDEX, seen[5][1])])
			self.assertEqual(len(seen[5][1]), 8 + 5 * 16)
			self.assertEqual(r.has_index, True)

	def test_find_walks_when_there_is_no_index(self):
		f = io.BytesIO()
		self.build(f, index=False)
		r = bp.Reader(s.seekable(f.getvalue()))
		self.assertFalse(r.has_index)
		first = r.next()
		self.assertEqual(len(r.find(1)), 3)
		self.assertEqual(r.offset, first.payload_offset)
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.streaming(f.getvalue())).find(1)), "noseek")

	def test_writer_argument_checks(self):
		w = bp.Writer(io.BytesIO(), s.SAMPLE_WRITER, "x", [])
		self.assertEqual(s.code_of(self, lambda: w.block(70000, 0, b"")), "range")
		self.assertEqual(s.code_of(self, lambda: bp.Writer(io.BytesIO(), b"short", "x", [])), "range")
		self.assertEqual(s.code_of(self, lambda: bp.Writer(io.BytesIO(), s.SAMPLE_WRITER, "x", [bp.TypeDef(1, b"bad", "n")])), "range")
		w2 = bp.Writer(io.BytesIO(), s.SAMPLE_WRITER, "x", [])
		self.assertEqual(s.code_of(self, lambda: w2.block(1, bp.FLAG_LARGE_MODE, b"abc")), "range")
		self.assertEqual(s.code_of(self, lambda: w2.block(1, 0, b"abc")), "range", "the error is sticky")
		w3 = bp.Writer(io.BytesIO(), s.SAMPLE_WRITER, "x", [])
		w3.end()
		self.assertEqual(s.code_of(self, lambda: w3.block(1, 0, b"")), "format")

	def test_a_failing_output_gives_a_sticky_io_error(self):
		class Broken:
			def write(self, data):
				raise OSError("disk full")

		self.assertEqual(s.code_of(self, lambda: bp.Writer(Broken(), s.SAMPLE_WRITER, "x", [])), "io")

	def test_a_pipe_refuses_compression_unless_declared(self):
		w = bp.Writer(s.PipeSink(), s.SAMPLE_WRITER, "x", [])
		self.assertEqual(s.code_of(self, lambda: bp.compress_block(w, 1, 0, bp.CODEC_STORED, None, b"abc")), "noseek")
		pipe = s.PipeSink()
		w = bp.Writer(pipe, s.SAMPLE_WRITER, "x", [], version_minor=bp.VERSION_MINOR_COMPRESSION)
		bp.compress_block(w, 1, 0, bp.CODEC_DEFLATE, None, b"abc" * 100)
		w.end()
		r = bp.Reader(s.streaming(pipe.mem.getvalue()))
		self.assertEqual(r.version_minor, 1)
		self.assertEqual(bytes(bp.read_payload_decompressed(r, r.next())), b"abc" * 100)

	def test_guid_text(self):
		g = bp.parse_guid("62696e70-617a-6572-0000-000000000002")
		self.assertEqual(g, bp.BLOCK_INDEX_GUID)
		self.assertEqual(bp.format_guid(g), "62696e70-617a-6572-0000-000000000002")
		self.assertEqual(bp.parse_guid("62696E70617A65720000000000000002"), g)
		for bad in ("", "62696e70", "zz696e70-617a-6572-0000-000000000002", "62696e70-617a-6572-0000-0000000000021"):
			self.assertEqual(s.code_of(self, lambda: bp.parse_guid(bad)), "format")
		self.assertEqual(s.code_of(self, lambda: bp.format_guid(b"abc")), "range")

	def test_an_import_builds_the_native_library_when_it_is_absent(self):
		import os
		import subprocess
		import sys

		from binpazer import _native

		py_root = str(s.REPO_ROOT / "py")
		probe = "import binpazer; print(binpazer.crc32c(0, b'123456789'))"
		_native.LIBRARY.unlink()
		try:
			out = subprocess.run([sys.executable, "-c", probe], cwd=py_root, capture_output=True, text=True, check=True)
			self.assertEqual(out.stdout.strip(), str(0xE3069283))
			self.assertTrue(_native.LIBRARY.exists())

			_native.LIBRARY.unlink()
			env = {**os.environ, "CC": "/nonexistent/cc"}
			failed = subprocess.run([sys.executable, "-c", probe], cwd=py_root, capture_output=True, text=True, env=env)
			self.assertNotEqual(failed.returncode, 0)
			self.assertIn("cannot build", failed.stderr)
			self.assertIn("/nonexistent/cc -std=c11 -O2 -shared -fPIC", failed.stderr, "the error names the command to run")
		finally:
			if not _native.LIBRARY.exists():
				_native._build()

	def test_crc32c_takes_every_buffer_kind(self):
		data = s.pattern(1000, 3)
		want = bp.crc32c(0, data)
		buf = bytearray(data)
		for form in (buf, memoryview(buf), memoryview(data), memoryview(buf)[10:], bytearray()):
			if len(form) == 990:
				self.assertEqual(bp.crc32c(bp.crc32c(0, data[:10]), form), want)
			elif len(form):
				self.assertEqual(bp.crc32c(0, form), want)
			else:
				self.assertEqual(bp.crc32c(7, form), 7)
		self.assertEqual(buf, data, "the buffer is free again after the call")
		buf.append(1)

	def test_crc32c_matches_the_standard_check_values(self):
		self.assertEqual(bp.crc32c(0, b"123456789"), 0xE3069283)
		self.assertEqual(bp.crc32c(0, b""), 0)
		self.assertEqual(bp.crc32c(0, bytes(32)), 0x8A9136AA)
		data = s.pattern(70_001, 9)
		whole = bp.crc32c(0, data)
		for cut in (0, 1, 7, 8, 9, 2047, 2048, 4097, 70_000):
			self.assertEqual(bp.crc32c(bp.crc32c(0, data[:cut]), memoryview(data)[cut:]), whole, cut)
		slow = 0xFFFFFFFF
		for byte in data[:5000]:
			slow ^= byte
			for _ in range(8):
				slow = (slow >> 1) ^ 0x82F63B78 if slow & 1 else slow >> 1
		self.assertEqual(bp.crc32c(0, bytearray(data[:5000])), slow ^ 0xFFFFFFFF)


class Group(unittest.TestCase):
	def group_payload(self) -> bytes:
		f = io.BytesIO()
		w = bp.Writer(f, s.SAMPLE_WRITER, "g", [], appendable=True)
		start = w.offset
		w.block(2, 0, b"child one")
		w.block(3, bp.FLAG_SAFE_TO_COPY, b"")
		return f.getvalue()[start:]

	def test_parse_group_returns_the_children(self):
		payload = self.group_payload()
		res = bp.parse_group(payload)
		self.assertIsNone(res.error)
		self.assertEqual([(c.type_id, c.payload, c.offset, c.crc_ok) for c in res.children], [(2, b"child one", 0, True), (3, b"", 24, True)])
		f = io.BytesIO()
		w = bp.Writer(f, s.SAMPLE_WRITER, "g", [])
		w.block(bp.TYPE_GROUP, 0, payload)
		w.end()
		r = bp.Reader(f)
		b = r.next()
		self.assertEqual(len(bp.parse_group(r.read_payload(b)).children), 2)

	def test_a_damaged_group_gives_its_intact_prefix_and_the_error(self):
		payload = bytearray(self.group_payload())
		payload[8] ^= 1
		self.assertEqual([c.crc_ok for c in bp.parse_group(payload).children], [False, True])
		cut = bp.parse_group(self.group_payload()[:30])
		self.assertEqual(len(cut.children), 1)
		self.assertEqual(cut.error.code, "truncated")
		junk = bp.parse_group(self.group_payload()[:24] + b"\x01\x02")
		self.assertEqual((len(junk.children), junk.error.code), (1, "truncated"))
		self.assertIsNone(bp.parse_group(self.group_payload() + bytes(5)).error)
