"""Hostile input: truncation, corrupt CRCs and oversized length fields fail with typed errors before any allocation."""

from struct import pack_into

from . import support as s
from .support import bp, io, unittest

OFF_FILE_LENGTH = 32
OFF_NAME_LEN = 56
OFF_BTT_COUNT = 80
OFF_ENTRY0_NAME = 108
OFF_BLOCK1 = 192
OFF_PAYLOAD1 = 200
OFF_CRC1 = 216

MAKERS = (("seekable", s.seekable), ("streaming", s.streaming))


def huge_payload_sample() -> bytearray:
	b = s.sample_bytes()
	pack_into("<HI", b, OFF_BLOCK1 + 2, bp.FLAG_CRITICAL | bp.FLAG_HAS_CRC | bp.FLAG_LARGE_MODE, 0xFFFFFFFF)
	return b


def patched(offset: int, value: int) -> bytearray:
	b = s.sample_bytes()
	pack_into("<I", b, offset, value)
	return b


def corrupt_crc_sample() -> bytearray:
	b = s.sample_bytes()
	b[OFF_PAYLOAD1] ^= 0xFF
	return b


def read_all(src, max_alloc: int = 0) -> None:
	r = bp.Reader(src, max_alloc)
	while (b := r.next()) is not None:
		r.read_payload(b)


def walk_count(r: bp.Reader) -> int:
	n = 0
	while (b := r.next()) is not None:
		r.skip_payload(b)
		n += 1
	return n


def exercise(src) -> None:
	"""Drives the whole read surface, like the Go fuzz target."""
	r = bp.Reader(src, 1 << 20)
	i = 0
	while (b := r.next()) is not None:
		if i % 2 == 0:
			r.read_payload(b)
		else:
			r.skip_payload(b)
		i += 1
	if not src.seekable():
		return
	for o in r.find(1):
		r.read_payload(r.at(o))


def indexed_file() -> bytearray:
	f = io.BytesIO()
	w = bp.Writer(f, s.SAMPLE_WRITER, "idx", [])
	w.block(1, 0, b"x")
	w.write_index()
	w.end()
	return bytearray(f.getvalue())


class Hardening(unittest.TestCase):
	def only_typed_errors(self, call, label: str) -> None:
		try:
			call()
		except bp.BinpazerError:
			pass
		except Exception as e:
			self.fail(f"{label}: {e!r}")

	def test_the_sample_offsets_are_where_the_helpers_patch(self):
		b = s.sample_bytes()
		self.assertEqual((len(b), b[OFF_BTT_COUNT], b[OFF_ENTRY0_NAME], b[OFF_BLOCK1], b[OFF_BLOCK1 + 4]), (280, 3, len("BlockTypeTable"), 1, 16))

	def test_every_prefix_ends_cleanly_or_with_a_typed_error(self):
		full = bytes(s.sample_bytes())
		mid_structure = {0, 4, 55, 58, 90, 150, 199, 219, 279}
		for n in range(len(full) + 1):
			for name, mk in MAKERS:
				label = f"prefix {n} ({name})"
				if n in mid_structure:
					self.assertEqual(s.code_of(self, lambda: read_all(mk(full[:n]))), "truncated", label)
				else:
					self.only_typed_errors(lambda: read_all(mk(full[:n])), label)
		for _, mk in MAKERS:
			r = bp.Reader(mk(full[:224]))
			r.read_payload(r.next())
			self.assertIsNone(r.next(), "a cut at a block boundary ends the walk cleanly")

	def test_a_flipped_payload_or_crc_byte_gives_crc(self):
		for at in (OFF_PAYLOAD1, OFF_CRC1):
			b = s.sample_bytes()
			b[at] ^= 1
			r = bp.Reader(s.seekable(b))
			blk = r.next()
			self.assertEqual(s.code_of(self, lambda: r.read_payload(blk)), "crc")
			r = bp.Reader(s.seekable(b))
			blk = r.next()
			self.assertEqual(s.code_of(self, lambda: r.stream_payload(blk, lambda chunk: None)), "crc")
			r = bp.Reader(s.seekable(b))
			blk = r.next()
			self.assertEqual(s.code_of(self, lambda: r.read_payload_into(blk, bytearray(64))), "crc")

	def test_bad_magic_bad_signature_and_a_later_major_version(self):
		b = s.sample_bytes()
		b[0] = 0x58
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.seekable(b))), "bad_magic")
		sig = s.sample_bytes()
		sig[8] = 0
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.seekable(sig))), "bad_magic")
		ver = s.sample_bytes()
		ver[12] = 2
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.seekable(ver))), "version")

	def test_a_huge_payload_claim_is_refused_before_the_payload_is_allocated(self):
		r = bp.Reader(s.seekable(huge_payload_sample()))
		self.assertEqual(s.code_of(self, r.next), "truncated")
		capped = bp.Reader(s.streaming(huge_payload_sample()), 4096)
		self.assertEqual(s.code_of(self, capped.next), "capacity")

	def test_a_huge_table_entry_count_gives_capacity(self):
		for _, mk in MAKERS:
			self.assertEqual(s.code_of(self, lambda: bp.Reader(mk(patched(OFF_BTT_COUNT, 0xFFFFFFFF)))), "capacity")

	def test_a_huge_writer_name_length_is_bounded(self):
		huge = patched(OFF_NAME_LEN, 0xFFFFFFFF)
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.seekable(huge))), "truncated")
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.streaming(huge))), "capacity")
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.streaming(patched(OFF_NAME_LEN, 100_000)), 1024)), "capacity")

	def test_a_table_name_that_overruns_the_table_gives_capacity(self):
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.seekable(patched(OFF_ENTRY0_NAME, 0xFFFF)))), "capacity")

	def test_max_alloc_caps_a_streaming_input_and_the_default_admits_normal_files(self):
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.streaming(s.sample_bytes()), 64)), "capacity")
		r = bp.Reader(s.streaming(s.sample_bytes()))
		self.assertEqual(r.input_size, -1)
		self.assertEqual(walk_count(r), 3)

	def test_a_lying_file_length_degrades_to_a_walk_that_ends_at_the_input(self):
		for label, value in (("296", 296), ("1<<62", 1 << 62), ("sentinel - 1", (1 << 64) - 2)):
			for _, mk in MAKERS:
				b = s.sample_bytes()
				pack_into("<Q", b, OFF_FILE_LENGTH, value)
				r = bp.Reader(mk(b))
				self.assertFalse(r.has_index, label)
				self.assertEqual(r.file_length, value, label)
				self.assertEqual(walk_count(r), 3, label)
				self.assertEqual(r.offset, len(b), label)

	def test_a_footer_that_points_past_the_input_gives_truncated_from_find(self):
		for target in (1_000_000, 1 << 62, (1 << 64) - 1):
			b = indexed_file()
			pack_into("<Q", b, len(b) - 16, target)
			r = bp.Reader(s.seekable(b))
			self.assertTrue(r.has_index)
			self.assertEqual(s.code_of(self, lambda: r.find(1)), "truncated")
			self.assertEqual(s.code_of(self, lambda: r.at(target)), "truncated")
			self.assertEqual(walk_count(bp.Reader(s.seekable(b))), 2)
		self.assertEqual(s.code_of(self, lambda: bp.Reader(s.seekable(indexed_file())).at(-1)), "range")

	def test_a_streaming_reader_finds_the_footer_at_the_end(self):
		b = indexed_file()
		r = bp.Reader(s.streaming(b))
		self.assertFalse(r.has_index)
		self.assertEqual(walk_count(r), 2)
		self.assertTrue(r.has_index)
		self.assertEqual(r.offset, len(b))

	def test_a_caller_made_block_cannot_drive_an_unbounded_allocation(self):
		fake = bp.Block(1, 0, 2**40, 0, 0, None, False, "")
		r = bp.Reader(s.seekable(s.sample_bytes()))
		self.assertEqual(s.code_of(self, lambda: r.read_payload(fake)), "truncated")
		capped = bp.Reader(s.streaming(s.sample_bytes()), 1024)
		self.assertEqual(s.code_of(self, lambda: capped.read_payload(fake)), "capacity")
		small = bp.Reader(s.seekable(s.sample_bytes()))
		blk = small.next()
		self.assertEqual(s.code_of(self, lambda: small.read_payload_into(blk, bytearray(3))), "short_buffer")

	def test_a_file_cut_inside_the_padding_of_a_block_reports_truncated(self):
		pipe = s.PipeSink()
		w = bp.Writer(pipe, s.SAMPLE_WRITER, "demo", s.NOTE_DEFS)
		w.block(2, 0, b"abc")
		w.end()
		cut = pipe.mem.getvalue()[:-5]
		for _, mk in MAKERS:
			for use in ("skip_payload", "read_payload"):
				r = bp.Reader(mk(cut))
				b = r.next()
				self.assertEqual(s.code_of(self, lambda: getattr(r, use)(b)), "truncated")
				self.assertEqual(r.offset, len(cut))

	def test_a_failing_input_gives_io(self):
		class Broken:
			def read(self, n):
				raise OSError("gone")

			def seekable(self):
				return False

		self.assertEqual(s.code_of(self, lambda: bp.Reader(Broken())), "io")

	def test_the_fuzz_seeds_raise_only_typed_errors(self):
		sample = bytes(s.sample_bytes())
		seeds = [sample, b"binpazer", huge_payload_sample(), patched(OFF_BTT_COUNT, 0xFFFFFFFF), patched(OFF_NAME_LEN, 0xFFFFFFFF), corrupt_crc_sample()]
		seeds += [sample[:n] for n in (4, 56, 60, 100, 150, 224, 279)]
		for i, seed in enumerate(seeds):
			for name, mk in MAKERS:
				self.only_typed_errors(lambda: exercise(mk(seed)), f"seed {i} {name}")
		exercise(s.seekable(sample))

	def test_random_mutations_of_an_indexed_file_raise_only_typed_errors(self):
		f = io.BytesIO()
		w = bp.Writer(f, s.SAMPLE_WRITER, "fuzz", [bp.TypeDef(1, s.SAMPLE_NOTE, "Note")])
		w.block(1, bp.FLAG_HAS_CRC, b"first payload")
		w.block(2, 0, b"second")
		w.block(1, bp.FLAG_LARGE_MODE, bytes(4096))
		w.write_index()
		w.end()
		base = f.getvalue()
		rnd = s.rng(0x9E3779B9)
		for it in range(3000):
			b = bytearray(base[: rnd(len(base))] if it % 7 == 0 else base)
			for _ in range(1 + rnd(4)):
				if b:
					b[rnd(len(b))] = rnd(256)
			for name, mk in MAKERS:
				self.only_typed_errors(lambda: exercise(mk(bytes(b))), f"iteration {it} {name}")
