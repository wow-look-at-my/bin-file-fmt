"""Appendable files (spec 1.2): the commit seal, the follower, and resume. The fixtures come from c/gen.c."""

from struct import pack, pack_into

from . import support as s
from .support import NOTE_DEFS, SAMPLE_WRITER, bp, code_of, io, unittest

RECORDS = [b"first record", b"second record, a bit longer", b""]
PENDING = bp.PENDING
TORN_21 = pack("<HHI", 2, bp.FLAG_HAS_CRC, 0x100000) + b"\xab" * 13


def file_of(data: bytes = b"") -> io.BytesIO:
	"""Returns a file that holds data, with its position at the end."""
	mem = io.BytesIO()
	mem.write(data)
	return mem


def reader_of(data) -> bp.Reader:
	return bp.Reader(io.BytesIO(data))


def start_sample(mem, end: bool) -> bp.Writer:
	"""Writes the records of gen_appendable in c/gen.c."""
	aw = bp.Writer(mem, SAMPLE_WRITER, "demo", NOTE_DEFS, appendable=True)
	for rec in RECORDS:
		aw.block(2, 0, rec)
	if end:
		aw.end()
	return aw


def append_sample(end: bool) -> bytes:
	mem = file_of()
	start_sample(mem, end)
	return mem.getvalue()


def follow_all(ar: bp.Reader):
	"""Reads committed blocks until next_committed returns anything but a block."""
	got = []
	while True:
		blk = ar.next_committed()
		if blk is None or blk is PENDING:
			return got, blk
		p = ar.read_payload(blk)
		if blk.type_id != bp.TYPE_END:
			got.append(p)


def walk(ar: bp.Reader):
	"""Yields each block of a plain walk. The caller reads the payload, or the walk skips it."""
	while (blk := ar.next()) is not None:
		yield blk
		if ar.offset == blk.payload_offset:
			ar.skip_payload(blk)


def block_ends(data: bytes) -> list[int]:
	"""Returns the offset past each top-level block of data."""
	ar = reader_of(data)
	ends = []
	for blk in walk(ar):
		ar.skip_payload(blk)
		ends.append(ar.offset)
	return ends


def follow_skipping_padding(test, data: bytes):
	"""Returns the non-Padding payloads and checks that Padding is zeros."""
	ar = reader_of(data)
	got = []
	while True:
		blk = ar.next_committed()
		if blk is None or blk is PENDING:
			test.assertFalse(ar.torn)
			return got, ar
		p = ar.read_payload(blk)
		if blk.type_id == bp.TYPE_PADDING:
			test.assertEqual(p, bytes(len(p)), "padding is zeros")
		else:
			got.append(p)


def torn_header(n: int) -> bytes:
	if n < 8:
		return bytes(n)
	return pack("<HHI", 2, bp.FLAG_HAS_CRC, 1 << 20) + b"\xab" * (n - 8)


class Appendable(unittest.TestCase):
	def committed(self, ar: bp.Reader) -> bp.Block:
		blk = ar.next_committed()
		self.assertIsInstance(blk, bp.Block)
		return blk

	def test_the_appendable_writer_reproduces_the_fixture(self):
		want = s.fixture("sample-appendable.binpazer")
		self.assertEqual(append_sample(True), want)
		ar = reader_of(want)
		self.assertTrue(ar.appendable)
		self.assertEqual((ar.version_minor, ar.file_length), (2, len(want)))
		self.assertEqual(ar.types[-1], bp.TypeEntry(bp.TYPE_END, bp.END_GUID, "End"))
		for rec in RECORDS:
			blk = self.committed(ar)
			self.assertEqual((blk.type_id, blk.flags, blk.name), (2, bp.FLAG_HAS_CRC, "Note"))
			self.assertEqual(ar.read_payload(blk), rec)
		end = self.committed(ar)
		self.assertEqual((end.type_id, end.flags, end.payload_length), (bp.TYPE_END, bp.FLAG_HAS_CRC, 0))
		self.assertTrue(ar.ended)
		ar.skip_payload(end)
		self.assertIsNone(ar.next_committed())
		self.assertIsNone(ar.next(), "no block follows the End block")

	def test_resume_reproduces_the_fixture(self):
		want = s.fixture("sample-resumed.binpazer")
		opened = append_sample(False)
		mem = file_of(opened + TORN_21)
		writer, types = bp.resume_append_writer(mem)
		self.assertEqual(writer.sealed_tail, 21)
		self.assertEqual(types[1].name, "Note")
		writer.block(2, 0, b"after the crash")
		writer.end()
		self.assertEqual(mem.getvalue(), want)

		ar = reader_of(want)
		seen = []
		while isinstance(blk := ar.next_committed(), bp.Block):
			p = ar.read_payload(blk)
			if blk.type_id == bp.TYPE_PADDING:
				self.assertEqual(blk.offset, len(opened))
				self.assertEqual(p, bytes(len(p)))
				seen.append(b"<pad>")
			elif blk.type_id != bp.TYPE_END:
				seen.append(p)
		self.assertEqual(seen, [*RECORDS, b"<pad>", b"after the crash"])
		self.assertTrue(ar.ended)

	def test_an_open_file_declares_1_2_keeps_the_sentinel_and_seals_every_block(self):
		ar = reader_of(append_sample(False))
		self.assertEqual((ar.appendable, ar.header_flags, ar.version_minor, ar.file_length), (True, 1, 2, None))
		sealed = [blk.flags & bp.FLAG_HAS_CRC for blk in walk(ar)]
		self.assertEqual(sealed, [bp.FLAG_HAS_CRC] * len(RECORDS))
		self.assertFalse(ar.ended)
		self.assertFalse(reader_of(s.sample_bytes()).appendable)

	def test_end_closes_the_file(self):
		mem = file_of()
		aw = start_sample(mem, True)
		self.assertEqual(code_of(self, lambda: aw.block(2, 0, b"late")), "ended")
		self.assertEqual(code_of(self, lambda: aw.block_compressed(2, 0, bp.CODEC_STORED, None, b"late", 4)), "ended")
		aw.end()
		data = mem.getvalue()
		ar = reader_of(data)
		self.assertEqual(ar.file_length, len(data))
		self.assertEqual(follow_all(ar), (RECORDS, None))
		self.assertTrue(ar.ended)

	def test_a_pipe_gets_a_valid_streaming_appendable_file(self):
		out = s.PipeSink()
		aw = bp.Writer(out, SAMPLE_WRITER, "demo", NOTE_DEFS, appendable=True)
		self.assertEqual(out.flushes, 1, "the table reaches the output before the first block")
		aw.block_compressed(2, 0, bp.CODEC_STORED, None, b"abc", 3)
		self.assertEqual(out.flushes, 2, "each block reaches the output when it is written")
		aw.end()
		ar = bp.Reader(s.streaming(out.mem.getvalue()))
		self.assertIsNone(ar.file_length)
		flags = []
		for blk in walk(ar):
			flags.append(blk.flags & bp.FLAG_HAS_CRC)
			ar.read_payload(blk)
		self.assertEqual(flags, [bp.FLAG_HAS_CRC] * 2, "the compressed block and the End block")
		self.assertTrue(ar.ended)
		self.assertEqual(code_of(self, ar.next_committed), "noseek")

	def test_write_index_is_refused_and_the_writer_stays_usable(self):
		mem = file_of()
		aw = start_sample(mem, False)
		self.assertEqual(code_of(self, aw.write_index), "range")
		self.assertEqual(code_of(self, lambda: aw.write_index([(2, 0)])), "range")
		aw.block(2, 0, b"still usable")
		aw.end()
		self.assertEqual(follow_all(reader_of(mem.getvalue()))[0], [*RECORDS, b"still usable"])

	def test_every_block_is_sealed_and_compressed_blocks_round_trip(self):
		mem = file_of()
		aw = bp.Writer(mem, SAMPLE_WRITER, "demo", NOTE_DEFS, appendable=True)
		data = s.pattern(20_000, 3)
		for codec in (bp.CODEC_STORED, bp.CODEC_DEFLATE, bp.CODEC_ZSTD, bp.CODEC_LZ4):
			bp.compress_block(aw, 2, 0, codec, None, data)
		aw.block(2, bp.FLAG_LARGE_MODE, bytes(4096))
		aw.end()
		ar = bp.Reader(s.LiveView(mem))
		for _ in range(4):
			blk = self.committed(ar)
			self.assertEqual(blk.flags, bp.FLAG_HAS_CRC | bp.FLAG_COMPRESSED)
			self.assertEqual(bytes(bp.read_payload_decompressed(ar, blk)), data)
		big = self.committed(ar)
		self.assertEqual(big.flags, bp.FLAG_HAS_CRC | bp.FLAG_LARGE_MODE)
		ar.skip_payload(big)
		self.assertEqual(self.committed(ar).type_id, bp.TYPE_END)

	def test_at_every_prefix_a_follower_sees_exactly_the_whole_blocks(self):
		data = append_sample(True)
		ends = block_ends(data)
		self.assertEqual(len(ends), len(RECORDS) + 1)
		first = reader_of(data).offset
		for n in range(first, len(data) + 1):
			ar = reader_of(data[:n])
			got, last = follow_all(ar)
			whole = sum(1 for e in ends if e <= n)
			if whole == len(ends):
				self.assertEqual((got, last), (RECORDS, None), f"prefix {n}")
				continue
			self.assertIs(last, PENDING, f"prefix {n}")
			self.assertFalse(ar.torn, f"prefix {n}: a short file is in progress, not torn")
			self.assertEqual(got, RECORDS[:whole], f"prefix {n}")
			self.assertEqual(ar.offset, first if whole == 0 else ends[whole - 1], f"prefix {n}")

	def test_a_follower_tracks_a_file_that_grows(self):
		mem = file_of()
		aw = bp.Writer(mem, SAMPLE_WRITER, "demo", [bp.TypeDef(1, s.SAMPLE_NOTE, "Note")], appendable=True)
		ar = bp.Reader(s.LiveView(mem))
		self.assertIs(ar.next_committed(), PENDING)
		at = ar.offset
		self.assertIs(ar.next_committed(), PENDING)
		self.assertEqual(ar.offset, at, "a pending reader does not move")

		aw.block(1, 0, b"one")
		one = self.committed(ar)
		self.assertTrue(one.known)
		self.assertEqual(ar.read_payload(one), b"one")
		self.assertIs(ar.next_committed(), PENDING)

		big = b"x" * (3 * 65536 + 5)
		bp.compress_block(aw, 1, 0, bp.CODEC_STORED, None, big)
		self.assertEqual(bytes(bp.read_payload_decompressed(ar, self.committed(ar))), big)

		aw.end()
		end = self.committed(ar)
		self.assertEqual(end.type_id, bp.TYPE_END)
		ar.skip_payload(end)
		self.assertIsNone(ar.next_committed())

		whole = append_sample(True)
		ends = block_ends(whole)
		grow = file_of(whole[: ends[0] + 5])
		gr = bp.Reader(s.LiveView(grow))
		self.assertEqual(gr.read_payload(self.committed(gr)), RECORDS[0])
		self.assertIs(gr.next_committed(), PENDING)
		grow.write(whole[ends[0] + 5 : ends[1]])
		self.assertEqual(gr.read_payload(self.committed(gr)), RECORDS[1])

	def state_row(self, data: bytes, torn: bool, repair, label: str) -> None:
		"""Reads the first record, then checks the pending state at the next boundary until repair lands the bytes."""
		mem = file_of(data)
		ar = bp.Reader(s.LiveView(mem))
		self.assertEqual(ar.read_payload(self.committed(ar)), RECORDS[0], label)
		at = ar.offset
		for _ in range(2):
			self.assertIs(ar.next_committed(), PENDING, label)
			self.assertEqual(ar.torn, torn, label)
			self.assertEqual(ar.offset, at, f"{label}: the reader stays at the block start")
		repair(mem)
		blk = self.committed(ar)
		self.assertEqual(blk.offset, at, label)
		self.assertEqual(ar.read_payload(blk), RECORDS[1], label)

	def test_every_pending_row_of_the_state_table_leaves_the_reader_in_place(self):
		data = append_sample(False)
		ends = block_ends(data)
		b1 = ends[0]

		def patch(at: int, upto: int | None = None):
			def repair(mem):
				mem.seek(at)
				mem.write(data[at:upto])

			return repair

		self.state_row(data[:b1], False, patch(b1), "end of input")
		self.state_row(data[: b1 + 5], False, patch(b1 + 5), "partial header")
		self.state_row(data[: ends[1] - 1], False, patch(ends[1] - 1), "block runs past the end")
		self.state_row(data[:b1] + bytes(64), False, patch(b1), "type_id 0")

		unsealed = bytearray(data)
		pack_into("<H", unsealed, b1 + 2, 0)
		self.state_row(bytes(unsealed), True, patch(b1 + 2, b1 + 4), "has_crc clear")

		bad_seal = bytearray(data)
		bad_seal[b1 + 8] ^= 0xFF
		self.state_row(bytes(bad_seal), True, patch(b1 + 8, b1 + 9), "CRC mismatch")

	def test_next_committed_refuses_a_file_that_is_not_appendable(self):
		self.assertEqual(code_of(self, reader_of(s.sample_bytes()).next_committed), "not_appendable")

	def test_find_lists_committed_blocks_only_and_keeps_the_walk(self):
		data = append_sample(True)
		ends = block_ends(data)
		ar = reader_of(data[: ends[2] - 3])
		at = ar.offset
		self.assertEqual(ar.find(2), [at, ends[0]])
		self.assertEqual(ar.offset, at)
		self.assertEqual(ar.find(bp.TYPE_BLOCK_TYPE_TABLE), [ar.first_block_offset])

		full = reader_of(data)
		self.assertEqual(full.find(bp.TYPE_END), [ends[2]])
		self.assertFalse(full.ended, "a find that passes the End block does not end the walk of the caller")
		self.assertEqual(full.at(ends[0]).type_id, 2)

	def test_resume_reports_the_sealed_tail(self):
		clean = append_sample(False)
		w0, _ = bp.resume_append_writer(file_of(clean))
		self.assertEqual((w0.sealed_tail, w0.offset), (0, len(clean)))
		w1, _ = bp.resume_append_writer(file_of(clean + b"\xab" * 24))
		self.assertEqual((w1.sealed_tail, w1.offset), (24, len(clean) + 24))

	def test_resume_appends_after_the_committed_blocks_of_a_clean_file(self):
		clean = append_sample(False)
		mem = file_of(clean)
		writer, types = bp.resume_append_writer(mem)
		self.assertEqual(writer.offset, len(clean))
		self.assertEqual(len(mem.getvalue()), len(clean), "a clean file gets no padding")
		self.assertEqual(types[1].name, "Note")
		writer.block(2, 0, b"after restart")
		writer.end()
		self.assertEqual(follow_all(reader_of(mem.getvalue())), ([*RECORDS, b"after restart"], None))

	def test_resume_seals_a_torn_tail_of_any_length(self):
		clean = append_sample(False)
		for tail in (1, 7, 8, 9, 15, 16, 17, 100, 4096 + 3):
			mem = file_of(clean + torn_header(tail))
			writer, _ = bp.resume_append_writer(mem)
			self.assertGreaterEqual(writer.offset, len(clean) + tail, f"tail {tail}: padding covers every stale byte")
			self.assertEqual(writer.offset % 8, 0)
			self.assertEqual(mem.getvalue()[: len(clean)], clean, f"tail {tail}: committed bytes are untouched")
			writer.block(2, 0, b"recovered")
			got, reader = follow_skipping_padding(self, mem.getvalue())
			self.assertEqual(got, [*RECORDS, b"recovered"], f"tail {tail}")
			self.assertEqual(reader.offset, len(mem.getvalue()), f"tail {tail}: no stale byte is left behind")

	def test_resume_hides_a_sealed_block_inside_a_torn_tail(self):
		clean = append_sample(False)
		inner = file_of()
		iw = bp.Writer(inner, SAMPLE_WRITER, "demo", NOTE_DEFS, appendable=True)
		start = iw.offset
		iw.block(2, 0, b"ghost")
		ghost = inner.getvalue()[start:]
		mem = file_of(clean + torn_header(8) + bytes(16) + ghost)
		writer, _ = bp.resume_append_writer(mem)
		writer.block(2, 0, b"real")
		self.assertEqual(follow_skipping_padding(self, mem.getvalue())[0], [*RECORDS, b"real"])

	def test_resume_pads_a_long_tail_in_several_padding_blocks(self):
		clean = append_sample(False)
		tail = 2 * (1 << 20) + 100
		mem = file_of(clean + b"\xab" * tail)
		bp.resume_append_writer(mem)
		ar = reader_of(mem.getvalue())
		pads = 0
		while isinstance(blk := ar.next_committed(), bp.Block):
			pads += blk.type_id == bp.TYPE_PADDING
			ar.skip_payload(blk)
		self.assertEqual(pads, 3)
		self.assertEqual(ar.offset, len(mem.getvalue()))
		self.assertGreaterEqual(len(mem.getvalue()), len(clean) + tail)

	def test_resume_refuses_what_it_cannot_continue(self):
		self.assertEqual(code_of(self, lambda: bp.resume_append_writer(file_of(append_sample(True)))), "ended")
		self.assertEqual(code_of(self, lambda: bp.resume_append_writer(file_of(bytes(s.sample_bytes())))), "not_appendable")
		self.assertEqual(code_of(self, lambda: bp.resume_append_writer(file_of(b"not binpazer " * 8))), "bad_magic")
		self.assertEqual(code_of(self, lambda: bp.resume_append_writer(s.Pipe(append_sample(False)))), "noseek")

	def test_a_hostile_length_is_pending_and_never_an_allocation_past_the_gate(self):
		data = append_sample(False)
		b1 = block_ends(data)[0]
		hostile = bytearray(data)
		pack_into("<HI", hostile, b1 + 2, bp.FLAG_HAS_CRC | bp.FLAG_LARGE_MODE, 0xFFFFFFFF)
		ar = reader_of(hostile)
		ar.skip_payload(self.committed(ar))
		self.assertIs(ar.next_committed(), PENDING)
		self.assertFalse(ar.torn)
		st = bp.Reader(s.streaming(bytes(hostile)), 4096)
		st.skip_payload(st.next())
		self.assertEqual(code_of(self, st.next), "capacity")

	def test_random_mutations_give_only_blocks_pending_none_or_typed_errors(self):
		base = append_sample(True)
		rnd = s.rng(0x2545F491)
		for it in range(2000):
			mutated = bytearray(base[: rnd(len(base))] if it % 5 == 0 else base)
			for _ in range(1 + rnd(3)):
				if mutated:
					mutated[rnd(len(mutated))] ^= 1 + rnd(255)
			try:
				ar = bp.Reader(io.BytesIO(mutated), 1 << 20)
				for _ in range(64):
					blk = ar.next_committed()
					if blk is None or blk is PENDING:
						break
					ar.read_payload(blk)
				ar.find(2)
			except bp.BinpazerError:
				pass
			except Exception as e:
				self.fail(f"iteration {it}: {e!r}")
