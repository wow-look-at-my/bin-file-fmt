/* Benchmark program for the C binpazer library: write, read, stream and find workloads. */
#define _FILE_OFFSET_BITS 64
#define _POSIX_C_SOURCE 200809L

#include "binpazer_codec.h"

#include <inttypes.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>

#define TYPE_PAYLOAD 1
#define IO_BUF_SIZE 65536
#define STREAM_CHUNK 65536
#define CODEC_NONE BINPAZER_CODEC_INVALID

static int fail(const char *fmt, ...) {
	va_list ap;
	va_start(ap, fmt);
	fputs("bench_c: ", stderr);
	vfprintf(stderr, fmt, ap);
	fputc('\n', stderr);
	va_end(ap);
	return 1;
}

static uint32_t mix(uint32_t h, uint8_t b) {
	return (uint32_t)((h ^ b) * 16777619u);
}

static uint32_t sum_parts(uint32_t h, uint64_t len, uint8_t first, uint8_t mid, uint8_t last) {
	uint32_t l = (uint32_t)len;
	for (int i = 0; i < 4; i++) h = mix(h, (uint8_t)(l >> (8 * i)));
	if (len > 0) {
		h = mix(h, first);
		h = mix(h, mid);
		h = mix(h, last);
	}
	return h;
}

static uint32_t sum_block(uint32_t h, const uint8_t *p, uint64_t len) {
	if (len == 0) return sum_parts(h, 0, 0, 0, 0);
	return sum_parts(h, len, p[0], p[len >> 1], p[len - 1]);
}

static int print_result(const char *op, uint64_t blocks, uint64_t bytes, uint32_t h) {
	if (printf("{\"op\":\"%s\",\"blocks\":%" PRIu64 ",\"bytes\":%" PRIu64 ",\"checksum\":\"%08" PRIx32 "\"}\n",
		   op, blocks, bytes, h) < 0 || fflush(stdout) != 0)
		return fail("writing the result line");
	return 0;
}

static size_t f_read(void *ctx, void *buf, size_t n) { return fread(buf, 1, n, (FILE *)ctx); }
static size_t f_write(void *ctx, const void *buf, size_t n) { return fwrite(buf, 1, n, (FILE *)ctx); }
static int f_seek(void *ctx, uint64_t off) {
	if (off > (uint64_t)INT64_MAX) return -1;
	return fseeko((FILE *)ctx, (off_t)off, SEEK_SET);
}

static FILE *open_file(const char *path, const char *mode) {
	FILE *f = fopen(path, mode);
	if (!f) {
		fail("cannot open %s", path);
		return NULL;
	}
	if (setvbuf(f, NULL, _IOFBF, IO_BUF_SIZE) != 0) {
		fclose(f);
		fail("setvbuf on %s", path);
		return NULL;
	}
	return f;
}

static int parse_count(const char *s, const char *what, uint64_t *out) {
	char *end = NULL;
	if (s[0] < '0' || s[0] > '9') return fail("bad %s: %s", what, s);
	unsigned long long v = strtoull(s, &end, 10);
	if (*end != '\0') return fail("bad %s: %s", what, s);
	*out = (uint64_t)v;
	return 0;
}

static int parse_codec(const char *s, uint16_t *out) {
	static const struct { const char *name; uint16_t id; } codecs[] = {
		{ "none", CODEC_NONE },
		{ "stored", BINPAZER_CODEC_STORED },
		{ "deflate", BINPAZER_CODEC_DEFLATE },
		{ "zstd", BINPAZER_CODEC_ZSTD },
		{ "lz4", BINPAZER_CODEC_LZ4 },
	};
	for (size_t i = 0; i < sizeof codecs / sizeof codecs[0]; i++) {
		if (strcmp(s, codecs[i].name) == 0) {
			*out = codecs[i].id;
			return 0;
		}
	}
	return fail("unknown codec: %s", s);
}

typedef struct {
	uint8_t *p;
	size_t cap;
} buf_t;

static int grow(buf_t *b, uint64_t need, const char *what) {
	if (need <= b->cap) return 0;
	if (need > SIZE_MAX) return fail("%s buffer of %" PRIu64 " bytes exceeds the address space", what, need);
	void *np = realloc(b->p, (size_t)need);
	if (!np) return fail("allocating %" PRIu64 " bytes for the %s buffer", need, what);
	b->p = np;
	b->cap = (size_t)need;
	return 0;
}

static int load_payload(const char *path, buf_t *out) {
	FILE *f = open_file(path, "rb");
	if (!f) return 1;
	int rc = 1;
	off_t size;
	if (fseeko(f, 0, SEEK_END) != 0 || (size = ftello(f)) < 0 || fseeko(f, 0, SEEK_SET) != 0) {
		fail("sizing %s", path);
		goto done;
	}
	if (grow(out, size > 0 ? (uint64_t)size : 1, "payload")) goto done;
	if (fread(out->p, 1, (size_t)size, f) != (size_t)size) {
		fail("reading %s", path);
		goto done;
	}
	out->cap = (size_t)size;
	rc = 0;
done:
	fclose(f);
	return rc;
}

static int cmd_write(const char *out_path, const char *payload_path, const char *blocks_s, const char *codec_s) {
	uint64_t blocks = 0;
	uint16_t codec = CODEC_NONE;
	if (parse_count(blocks_s, "block count", &blocks) || parse_codec(codec_s, &codec)) return 1;
	if (blocks > SIZE_MAX / sizeof(binpazer_index_entry)) return fail("block count too large: %s", blocks_s);

	buf_t payload = { NULL, 0 };
	if (load_payload(payload_path, &payload)) return 1;
	uint64_t plen = payload.cap;

	int rc = 1;
	FILE *f = NULL;
	uint8_t *scratch = NULL;
	uint64_t scratch_cap = 0;
	binpazer_index_entry *idx = malloc(blocks ? (size_t)blocks * sizeof *idx : 1);
	if (!idx) {
		fail("allocating the block index entries");
		goto done;
	}
	if (codec != CODEC_NONE) {
		scratch_cap = binpazer_compress_bound(codec, NULL, plen);
		if (scratch_cap == 0 && plen != 0) {
			fail("compress bound for codec %s", codec_s);
			goto done;
		}
		if (scratch_cap > SIZE_MAX || !(scratch = malloc(scratch_cap ? (size_t)scratch_cap : 1))) {
			fail("allocating the %" PRIu64 "-byte compression scratch buffer", scratch_cap);
			goto done;
		}
	}

	f = open_file(out_path, "wb");
	if (!f) goto done;

	binpazer_guid wg = { { 0xB0 } };
	binpazer_type_def defs[1] = { { TYPE_PAYLOAD, { { 0x11 } }, "Payload" } };
	binpazer_writer w;
	binpazer_status st = binpazer_writer_begin(&w, f_write, f_seek, f, &wg, "bench", defs, 1);
	if (st != BINPAZER_OK) {
		fail("writer begin (status %d)", (int)st);
		goto done;
	}
	for (uint64_t i = 0; i < blocks; i++) {
		idx[i].type_id = TYPE_PAYLOAD;
		idx[i].offset = w.offset;
		if (codec == CODEC_NONE)
			st = binpazer_writer_block(&w, TYPE_PAYLOAD, BINPAZER_FLAG_HAS_CRC, payload.p, plen);
		else
			st = binpazer_writer_block_compress(&w, TYPE_PAYLOAD, BINPAZER_FLAG_HAS_CRC, codec, NULL,
							    payload.p, plen, scratch, scratch_cap);
		if (st != BINPAZER_OK) {
			fail("writing block %" PRIu64 " (status %d)", i, (int)st);
			goto done;
		}
	}
	if ((st = binpazer_writer_index(&w, idx, (size_t)blocks)) != BINPAZER_OK) {
		fail("writing the block index (status %d)", (int)st);
		goto done;
	}
	if ((st = binpazer_writer_end(&w)) != BINPAZER_OK) {
		fail("ending the file (status %d)", (int)st);
		goto done;
	}
	FILE *closing = f;
	f = NULL;
	if (fclose(closing) != 0) {
		fail("closing %s", out_path);
		goto done;
	}
	rc = print_result("write", blocks, blocks * plen, 0);
done:
	if (f) fclose(f);
	free(idx);
	free(scratch);
	free(payload.p);
	return rc;
}

typedef struct {
	FILE *f;
	binpazer_reader r;
	binpazer_type_entry types[256];
	size_t ntypes;
} reader_t;

static int open_reader(const char *path, reader_t *rd) {
	rd->f = open_file(path, "rb");
	if (!rd->f) return 1;
	binpazer_status st = binpazer_reader_begin(&rd->r, f_read, f_seek, rd->f, NULL, 0,
						   rd->types, sizeof rd->types / sizeof rd->types[0], &rd->ntypes);
	if (st != BINPAZER_OK) {
		fclose(rd->f);
		return fail("reader begin on %s (status %d)", path, (int)st);
	}
	return 0;
}

/* Fails when the walk stopped short of the block stream end the header declares. */
static int check_walk_complete(const binpazer_reader *r) {
	if (r->blocks_end != BINPAZER_FILE_LENGTH_UNKNOWN && r->offset < r->blocks_end)
		return fail("block walk ended at offset %" PRIu64 ", before the stream end %" PRIu64,
			    r->offset, r->blocks_end);
	return 0;
}

static int read_decoded(binpazer_reader *r, const binpazer_block *blk, buf_t *scratch, buf_t *dst, uint64_t *len) {
	binpazer_status st;
	if ((blk->flags & BINPAZER_FLAG_COMPRESSED) == 0) {
		if (grow(dst, blk->payload_len, "payload")) return 1;
		st = binpazer_reader_read_payload(r, blk, dst->p, dst->cap);
		if (st != BINPAZER_OK)
			return fail("reading the payload at offset %" PRIu64 " (status %d)", blk->payload_offset, (int)st);
		*len = blk->payload_len;
		return 0;
	}
	if (grow(scratch, blk->payload_len, "stored payload")) return 1;
	st = binpazer_reader_read_payload(r, blk, scratch->p, scratch->cap);
	if (st != BINPAZER_OK)
		return fail("reading the stored payload at offset %" PRIu64 " (status %d)", blk->payload_offset, (int)st);
	binpazer_compression c;
	st = binpazer_compression_parse(scratch->p, blk->payload_len, &c);
	if (st != BINPAZER_OK)
		return fail("parsing the envelope at offset %" PRIu64 " (status %d)", blk->payload_offset, (int)st);
	if (grow(dst, c.uncompressed_length, "decoded payload")) return 1;
	st = binpazer_decompress(&c, scratch->p + c.header_size, dst->p, dst->cap);
	if (st != BINPAZER_OK)
		return fail("decompressing the payload at offset %" PRIu64 " (status %d)", blk->payload_offset, (int)st);
	*len = c.uncompressed_length;
	return 0;
}

static int cmd_read(const char *path) {
	reader_t rd;
	if (open_reader(path, &rd)) return 1;
	buf_t scratch = { NULL, 0 }, dst = { NULL, 0 };
	uint64_t blocks = 0, bytes = 0;
	uint32_t h = 2166136261u;
	int rc = 1;
	for (;;) {
		binpazer_block blk;
		binpazer_status st = binpazer_reader_next(&rd.r, rd.types, rd.ntypes, &blk);
		if (st == BINPAZER_END) break;
		if (st != BINPAZER_OK) {
			fail("reading a block header (status %d)", (int)st);
			goto done;
		}
		if (blk.type_id != TYPE_PAYLOAD) {
			if ((st = binpazer_reader_skip_payload(&rd.r, &blk)) != BINPAZER_OK) {
				fail("skipping a block (status %d)", (int)st);
				goto done;
			}
			continue;
		}
		uint64_t len;
		if (read_decoded(&rd.r, &blk, &scratch, &dst, &len)) goto done;
		h = sum_block(h, dst.p, len);
		blocks++;
		bytes += len;
	}
	if (check_walk_complete(&rd.r)) goto done;
	rc = print_result("read", blocks, bytes, h);
done:
	fclose(rd.f);
	free(scratch.p);
	free(dst.p);
	return rc;
}

typedef struct {
	uint64_t len, pos;
	uint8_t first, mid, last;
} stream_state;

static binpazer_status stream_sink(void *ctx, const void *buf, size_t n) {
	stream_state *s = ctx;
	const uint8_t *p = buf;
	if (n == 0) return BINPAZER_OK;
	uint64_t mid = s->len >> 1;
	if (s->pos == 0) s->first = p[0];
	if (mid >= s->pos && mid < s->pos + n) s->mid = p[mid - s->pos];
	s->last = p[n - 1];
	s->pos += n;
	return BINPAZER_OK;
}

static int cmd_stream(const char *path) {
	reader_t rd;
	if (open_reader(path, &rd)) return 1;
	int rc = 1;
	uint64_t blocks = 0, bytes = 0;
	uint32_t h = 2166136261u;
	uint8_t *scratch = malloc(STREAM_CHUNK);
	if (!scratch) {
		fail("allocating the stream scratch buffer");
		goto done;
	}
	for (;;) {
		binpazer_block blk;
		binpazer_status st = binpazer_reader_next(&rd.r, rd.types, rd.ntypes, &blk);
		if (st == BINPAZER_END) break;
		if (st != BINPAZER_OK) {
			fail("reading a block header (status %d)", (int)st);
			goto done;
		}
		if (blk.type_id != TYPE_PAYLOAD) {
			if ((st = binpazer_reader_skip_payload(&rd.r, &blk)) != BINPAZER_OK) {
				fail("skipping a block (status %d)", (int)st);
				goto done;
			}
			continue;
		}
		stream_state s = { blk.payload_len, 0, 0, 0, 0 };
		st = binpazer_reader_stream_payload(&rd.r, &blk, scratch, STREAM_CHUNK, stream_sink, &s);
		if (st != BINPAZER_OK) {
			fail("streaming the payload at offset %" PRIu64 " (status %d)", blk.payload_offset, (int)st);
			goto done;
		}
		h = sum_parts(h, s.len, s.first, s.mid, s.last);
		blocks++;
		bytes += s.len;
	}
	if (check_walk_complete(&rd.r)) goto done;
	rc = print_result("stream", blocks, bytes, h);
done:
	fclose(rd.f);
	free(scratch);
	return rc;
}

static int cmd_find(const char *path, const char *lookups_s) {
	uint64_t lookups;
	if (parse_count(lookups_s, "lookup count", &lookups)) return 1;
	reader_t rd;
	if (open_reader(path, &rd)) return 1;
	int rc = 1;
	binpazer_index_entry *found = NULL;
	buf_t scratch = { NULL, 0 }, dst = { NULL, 0 };
	size_t m = 0;
	binpazer_status st = binpazer_reader_find(&rd.r, TYPE_PAYLOAD, NULL, 0, &m);
	if (st != BINPAZER_OK && st != BINPAZER_ERR_CAPACITY) {
		fail("find (status %d)", (int)st);
		goto done;
	}
	if (m == 0) {
		fail("find returned no type-%d blocks", TYPE_PAYLOAD);
		goto done;
	}
	if (m > SIZE_MAX / sizeof *found || !(found = malloc(m * sizeof *found))) {
		fail("allocating %zu find results", m);
		goto done;
	}
	size_t m2 = 0;
	if ((st = binpazer_reader_find(&rd.r, TYPE_PAYLOAD, found, m, &m2)) != BINPAZER_OK || m2 != m) {
		fail("find (status %d)", (int)st);
		goto done;
	}
	uint64_t bytes = 0;
	uint32_t h = 2166136261u;
	for (uint64_t k = 0; k < lookups; k++) {
		uint32_t mixed = (uint32_t)k * 2654435761u;
		size_t i = (size_t)(mixed % m);
		binpazer_block blk;
		if ((st = binpazer_reader_at(&rd.r, rd.types, rd.ntypes, found[i].offset, &blk)) != BINPAZER_OK) {
			fail("seeking to the block at offset %" PRIu64 " (status %d)", found[i].offset, (int)st);
			goto done;
		}
		if (blk.type_id != TYPE_PAYLOAD) {
			fail("block at offset %" PRIu64 " has type %u, not %d", found[i].offset, blk.type_id, TYPE_PAYLOAD);
			goto done;
		}
		uint64_t len;
		if (read_decoded(&rd.r, &blk, &scratch, &dst, &len)) goto done;
		h = sum_block(h, dst.p, len);
		bytes += len;
	}
	rc = print_result("find", lookups, bytes, h);
done:
	fclose(rd.f);
	free(found);
	free(scratch.p);
	free(dst.p);
	return rc;
}

int main(int argc, char **argv) {
	if (argc == 6 && strcmp(argv[1], "write") == 0) return cmd_write(argv[2], argv[3], argv[4], argv[5]);
	if (argc == 3 && strcmp(argv[1], "read") == 0) return cmd_read(argv[2]);
	if (argc == 3 && strcmp(argv[1], "stream") == 0) return cmd_stream(argv[2]);
	if (argc == 4 && strcmp(argv[1], "find") == 0) return cmd_find(argv[2], argv[3]);
	fputs("usage: bench_c write <out-file> <payload-file> <blocks> <codec>\n"
	      "       bench_c read <in-file>\n"
	      "       bench_c stream <in-file>\n"
	      "       bench_c find <in-file> <lookups>\n"
	      "codec: none stored deflate zstd lz4\n",
	      stderr);
	return 2;
}
