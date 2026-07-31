/* Codec-layer tests: every registry codec must round-trip through a real file.
 * Separate from test.c so the core keeps a test binary that links no codec
 * library at all. */
#include "binpazer_codec.h"
#include <stdio.h>
#include <string.h>

typedef struct { uint8_t *data; size_t cap, len, pos; } membuf;

static size_t mem_write(void *ctx, const void *buf, size_t n) {
    membuf *m = (membuf *)ctx;
    if (m->pos + n > m->cap) return 0;
    memcpy(m->data + m->pos, buf, n);
    m->pos += n;
    if (m->pos > m->len) m->len = m->pos;
    return n;
}
static size_t mem_read(void *ctx, void *buf, size_t n) {
    membuf *m = (membuf *)ctx;
    size_t avail = m->len - m->pos;
    size_t c = n < avail ? n : avail;
    memcpy(buf, m->data + m->pos, c);
    m->pos += c;
    return c;
}
static int mem_seek(void *ctx, uint64_t off) {
    membuf *m = (membuf *)ctx;
    if (off > m->cap) return -1;
    m->pos = (size_t)off;
    return 0;
}

static int failures = 0;
#define CHECK(cond, msg) do { if (!(cond)) { printf("FAIL: %s\n", msg); failures++; } } while (0)

/* Compressible on purpose: a real codec must visibly shrink it, which is what
 * proves the bytes went through the codec and not past it. */
static void fill_payload(uint8_t *p, size_t n) {
    static const char *line = "binpazer compression parity across implementations. ";
    size_t len = strlen(line);
    for (size_t i = 0; i < n; i++) p[i] = (uint8_t)line[i % len];
}

static uint8_t payload[8192];
static uint8_t filebuf[65536];
static uint8_t scratch[65536];
static uint8_t decoded[16384];

/* Writes a one-block file compressed with `codec_id`, reads it back through
 * the decompressing reader, and checks the logical bytes survived. */
static void roundtrip(uint16_t codec_id, const binpazer_guid *codec_guid,
                      const char *name, int expect_shrink) {
    membuf m = { filebuf, sizeof filebuf, 0, 0 };
    binpazer_guid wg = {{0x01}};
    binpazer_type_def defs[1] = { {1, {{0x11}}, "Payload"} };

    uint64_t bound = binpazer_compress_bound(codec_id, codec_guid, sizeof payload);
    CHECK(bound >= 1, name);
    CHECK(bound <= sizeof scratch, "codec: bound fits scratch");

    binpazer_writer w;
    CHECK(binpazer_writer_begin(&w, mem_write, mem_seek, &m, &wg, "codec", defs, 1) == BINPAZER_OK,
          "codec: writer begin");
    CHECK(binpazer_writer_block_compress(&w, 1, BINPAZER_FLAG_HAS_CRC, codec_id, codec_guid,
                                         payload, sizeof payload,
                                         scratch, sizeof scratch) == BINPAZER_OK,
          name);
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "codec: writer end");

    m.pos = 0;
    binpazer_reader r; char nm[32]; binpazer_type_entry te[8]; size_t nte = 0;
    CHECK(binpazer_reader_begin(&r, mem_read, mem_seek, &m, nm, sizeof nm, te, 8, &nte) == BINPAZER_OK,
          "codec: reader begin");
    /* A compressed block makes the file 1.1 -- back-patched by writer_end. */
    CHECK(r.version_minor == BINPAZER_VERSION_MINOR_COMPRESSION, "codec: file declares 1.1");

    binpazer_block blk;
    CHECK(binpazer_reader_next(&r, te, nte, &blk) == BINPAZER_OK, "codec: next block");
    CHECK((blk.flags & BINPAZER_FLAG_COMPRESSED) != 0, "codec: compressed flag set");
    if (expect_shrink)
        CHECK(blk.payload_len < sizeof payload, "codec: stored payload is smaller than the logical bytes");

    binpazer_compression c;
    memset(&c, 0, sizeof c);
    uint64_t got = 0;
    CHECK(binpazer_reader_read_payload_decompressed(&r, &blk, scratch, sizeof scratch,
                                                    decoded, sizeof decoded, &got) == BINPAZER_OK,
          name);
    CHECK(got == sizeof payload, "codec: decoded length matches");
    CHECK(memcmp(decoded, payload, sizeof payload) == 0, "codec: decoded bytes match");
}

/* An uncompressed block must pass through the decompressing reader unchanged,
 * so a caller can use one read path for a whole file. */
static void test_uncompressed_passthrough(void) {
    membuf m = { filebuf, sizeof filebuf, 0, 0 };
    binpazer_guid wg = {{0x01}};
    binpazer_type_def defs[1] = { {1, {{0x11}}, "Payload"} };

    binpazer_writer w;
    binpazer_writer_begin(&w, mem_write, mem_seek, &m, &wg, "codec", defs, 1);
    binpazer_writer_block(&w, 1, BINPAZER_FLAG_HAS_CRC, "plain bytes", 11);
    binpazer_writer_end(&w);

    m.pos = 0;
    binpazer_reader r; char nm[32]; binpazer_type_entry te[8]; size_t nte = 0;
    binpazer_reader_begin(&r, mem_read, mem_seek, &m, nm, sizeof nm, te, 8, &nte);
    CHECK(r.version_minor == 0, "passthrough: no compressed block keeps the file 1.0");

    binpazer_block blk;
    binpazer_reader_next(&r, te, nte, &blk);
    uint64_t got = 0;
    CHECK(binpazer_reader_read_payload_decompressed(&r, &blk, NULL, 0,
                                                    decoded, sizeof decoded, &got) == BINPAZER_OK,
          "passthrough: read uncompressed block");
    CHECK(got == 11 && memcmp(decoded, "plain bytes", 11) == 0, "passthrough: bytes match");
}

/* The GUID escape: a codec outside the registry, resolved by GUID. */
static binpazer_status xor_compress(void *u, const void *src, uint64_t n, void *dst, uint64_t *dst_len) {
    (void)u;
    if (*dst_len < n) return BINPAZER_ERR_CAPACITY;
    for (uint64_t i = 0; i < n; i++) ((uint8_t *)dst)[i] = ((const uint8_t *)src)[i] ^ 0x5Au;
    *dst_len = n;
    return BINPAZER_OK;
}
static binpazer_status xor_decompress(void *u, const void *src, uint64_t n, void *dst, uint64_t dst_len) {
    (void)u;
    if (n != dst_len) return BINPAZER_ERR_FORMAT;
    for (uint64_t i = 0; i < n; i++) ((uint8_t *)dst)[i] = ((const uint8_t *)src)[i] ^ 0x5Au;
    return BINPAZER_OK;
}
static uint64_t xor_bound(void *u, uint64_t n) { (void)u; return n; }

static void test_guid_codec(void) {
    static const binpazer_codec xor_codec = { xor_compress, xor_decompress, xor_bound, NULL };
    binpazer_guid g = {{0xAA,0xBB,0xCC,0xDD}};
    CHECK(binpazer_codec_register_guid(&g, &xor_codec) == BINPAZER_OK, "guid codec: register");
    CHECK(binpazer_codec_lookup(BINPAZER_CODEC_BY_GUID, &g) == &xor_codec ||
          binpazer_codec_lookup(BINPAZER_CODEC_BY_GUID, &g) != NULL, "guid codec: lookup");
    roundtrip(BINPAZER_CODEC_BY_GUID, &g, "guid codec: round-trip", 0);

    binpazer_guid unknown = {{0xFF}};
    CHECK(binpazer_codec_lookup(BINPAZER_CODEC_BY_GUID, &unknown) == NULL, "guid codec: unknown stays unavailable");
}

/* A codec id the registry does not name has no implementation, and must be
 * reported rather than guessed at. */
static void test_unavailable_codec(void) {
    uint64_t n = sizeof scratch;
    CHECK(binpazer_compress(9, NULL, payload, 16, scratch, &n) == BINPAZER_ERR_RANGE,
          "unavailable: unknown registry id refused");
    CHECK(binpazer_compress_bound(9, NULL, 16) == 0, "unavailable: no bound");
}

/* A declared uncompressed_length that disagrees with what the codec produces
 * is corruption -- the check that keeps a decompression bomb bounded. */
static void test_length_mismatch(void) {
    uint64_t stored_len = sizeof scratch;
    CHECK(binpazer_compress(BINPAZER_CODEC_ZSTD, NULL, payload, 1024, scratch, &stored_len) == BINPAZER_OK,
          "mismatch: compress");
    binpazer_compression c;
    memset(&c, 0, sizeof c);
    c.codec_id = BINPAZER_CODEC_ZSTD;
    c.stored_length = stored_len;
    c.uncompressed_length = 999; /* the truth is 1024 */
    CHECK(binpazer_decompress(&c, scratch, decoded, sizeof decoded) == BINPAZER_ERR_FORMAT,
          "mismatch: short declared length rejected");
    c.uncompressed_length = 4096;
    CHECK(binpazer_decompress(&c, scratch, decoded, sizeof decoded) == BINPAZER_ERR_FORMAT,
          "mismatch: long declared length rejected");
}

/* Truncated codec bytes must fail, not return whatever decoded so far. */
static void test_truncated(void) {
    uint64_t stored_len = sizeof scratch;
    CHECK(binpazer_compress(BINPAZER_CODEC_DEFLATE, NULL, payload, sizeof payload,
                            scratch, &stored_len) == BINPAZER_OK, "truncated: compress");
    binpazer_compression c;
    memset(&c, 0, sizeof c);
    c.codec_id = BINPAZER_CODEC_DEFLATE;
    c.stored_length = stored_len / 2;
    c.uncompressed_length = sizeof payload;
    CHECK(binpazer_decompress(&c, scratch, decoded, sizeof decoded) == BINPAZER_ERR_FORMAT,
          "truncated: half a deflate stream is rejected");
}

int main(void) {
    fill_payload(payload, sizeof payload);

    roundtrip(BINPAZER_CODEC_STORED,  NULL, "codec stored: round-trip",  0);
    roundtrip(BINPAZER_CODEC_DEFLATE, NULL, "codec deflate: round-trip", 1);
    roundtrip(BINPAZER_CODEC_ZSTD,    NULL, "codec zstd: round-trip",    1);
    roundtrip(BINPAZER_CODEC_LZ4,     NULL, "codec lz4: round-trip",     1);

    test_uncompressed_passthrough();
    test_guid_codec();
    test_unavailable_codec();
    test_length_mismatch();
    test_truncated();

    if (failures == 0) { printf("OK: all C codec checks passed\n"); return 0; }
    printf("%d FAILURE(S)\n", failures);
    return 1;
}
