/* Round-trip test for the binpazer C reference implementation.
 * Uses an in-memory buffer as the IO target -- no file IO, no mmap. */
#include "binpazer.h"
#include <stdio.h>
#include <string.h>

/* ---- in-memory stream + callbacks ---- */
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

/* Exercises the Block Index: write with an index, discover it via the footer,
 * find blocks by type without walking. It confirm the no-index walk fallback. */
static void test_index(void) {
    static uint8_t bufA[65536], bufB[65536];
    membuf A = { bufA, sizeof bufA, 0, 0 };
    membuf B = { bufB, sizeof bufB, 0, 0 };

    binpazer_guid wg = {{0x01}};
    binpazer_guid g1 = {{0x11}}, g2 = {{0x22}};
    binpazer_type_def defs[2] = { {1, {{0}}, "A"}, {2, {{0}}, "B"} };
    defs[0].guid = g1; defs[1].guid = g2;

    /* file A: blocks + index + footer */
    binpazer_writer w;
    binpazer_writer_begin(&w, mem_write, mem_seek, &A, &wg, "demo", defs, 2);
    binpazer_index_entry idx[3];
    idx[0].type_id = 1; idx[0].offset = w.offset; binpazer_writer_block(&w, 1, 0, "aaaa", 4);
    idx[1].type_id = 2; idx[1].offset = w.offset; binpazer_writer_block(&w, 2, 0, "bb", 2);
    idx[2].type_id = 1; idx[2].offset = w.offset; binpazer_writer_block(&w, 1, 0, "cccccc", 6);
    CHECK(binpazer_writer_index(&w, idx, 3) == BINPAZER_OK, "index: write index");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK && w.err == BINPAZER_OK, "index: end ok");

    A.pos = 0;
    binpazer_reader r; char nm[32]; binpazer_type_entry te[8]; size_t nte = 0;
    binpazer_reader_begin(&r, mem_read, mem_seek, &A, nm, sizeof nm, te, 8, &nte);
    CHECK(r.has_index, "index: discovered via footer");

    binpazer_index_entry found[8]; size_t nf = 0;
    uint64_t pos_before = r.offset;
    CHECK(binpazer_reader_find(&r, 1, found, 8, &nf) == BINPAZER_OK, "index: find type1 ok");
    CHECK(nf == 2, "index: find type1 -> 2");
    CHECK(found[0].offset == idx[0].offset && found[1].offset == idx[2].offset, "index: find type1 offsets");
    CHECK(r.offset == pos_before, "index: find preserves position");

    binpazer_block blk; uint8_t pb[32];
    CHECK(binpazer_reader_at(&r, te, nte, found[0].offset, &blk) == BINPAZER_OK, "index: at ok");
    CHECK(blk.type_id == 1 && blk.known && blk.payload_len == 4, "index: at header");
    CHECK(binpazer_reader_read_payload(&r, &blk, pb, sizeof pb) == BINPAZER_OK && memcmp(pb, "aaaa", 4) == 0, "index: at read payload");

    CHECK(binpazer_reader_find(&r, 2, found, 8, &nf) == BINPAZER_OK && nf == 1 && found[0].offset == idx[1].offset, "index: find type2");

    /* a full walk must stop before the footer, not misparse it as a block */
    A.pos = 0;
    binpazer_reader rw; char nmw[16]; binpazer_type_entry tew[8]; size_t ntew = 0;
    binpazer_reader_begin(&rw, mem_read, mem_seek, &A, nmw, sizeof nmw, tew, 8, &ntew);
    int walked = 0; binpazer_block wb;
    while (binpazer_reader_next(&rw, tew, ntew, &wb) == BINPAZER_OK) {
        if (binpazer_reader_skip_payload(&rw, &wb)) break;
        walked++;
    }
    CHECK(walked == 4 && rw.err == BINPAZER_OK, "index: walk stops before footer (4 blocks)");

    /* file B: same blocks, no index -> find falls back to a walk */
    binpazer_writer w2;
    binpazer_writer_begin(&w2, mem_write, mem_seek, &B, &wg, "demo", defs, 2);
    binpazer_writer_block(&w2, 1, 0, "aaaa", 4);
    binpazer_writer_block(&w2, 2, 0, "bb", 2);
    binpazer_writer_block(&w2, 1, 0, "cccccc", 6);
    binpazer_writer_end(&w2);

    B.pos = 0;
    binpazer_reader r2; char nm2[32]; binpazer_type_entry te2[8]; size_t nte2 = 0;
    binpazer_reader_begin(&r2, mem_read, mem_seek, &B, nm2, sizeof nm2, te2, 8, &nte2);
    CHECK(!r2.has_index, "no-index: footer absent");
    binpazer_index_entry fb[8]; size_t nfb = 0;
    CHECK(binpazer_reader_find(&r2, 1, fb, 8, &nfb) == BINPAZER_OK && nfb == 2, "no-index: find via walk -> 2");
    CHECK(fb[0].offset == idx[0].offset && fb[1].offset == idx[2].offset, "no-index: walk offsets match");
}

/* Exercises large_mode with 4096-byte (page) units. */
static void test_large(void) {
    static uint8_t buf[8192];
    membuf m = { buf, sizeof buf, 0, 0 };
    binpazer_guid wg = {{0x07}};
    binpazer_type_def defs[1] = { {1, {{0}}, "Big"} };
    static unsigned char big[4096];
    for (int i = 0; i < 4096; i++) big[i] = (unsigned char)i;

    binpazer_writer w;
    binpazer_writer_begin(&w, mem_write, mem_seek, &m, &wg, "demo", defs, 1);
    CHECK(binpazer_writer_block(&w, 1, BINPAZER_FLAG_LARGE_MODE, big, 100) == BINPAZER_ERR_RANGE,
          "large: rejects non-4096 multiple");
    w.err = BINPAZER_OK; /* clear sticky error from the rejected write */
    CHECK(binpazer_writer_block(&w, 1, BINPAZER_FLAG_LARGE_MODE, big, 4096) == BINPAZER_OK, "large: write 4096");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "large: end");

    m.pos = 0;
    binpazer_reader r; char nm[16]; binpazer_type_entry te[4]; size_t nte = 0;
    binpazer_reader_begin(&r, mem_read, mem_seek, &m, nm, sizeof nm, te, 4, &nte);
    binpazer_block blk;
    CHECK(binpazer_reader_next(&r, te, nte, &blk) == BINPAZER_OK, "large: next");
    CHECK((blk.flags & BINPAZER_FLAG_LARGE_MODE) && blk.payload_len == 4096, "large: decoded 4096");
    static uint8_t rb[4096];
    CHECK(binpazer_reader_read_payload(&r, &blk, rb, sizeof rb) == BINPAZER_OK && memcmp(rb, big, 4096) == 0,
          "large: payload bytes");
}

/* An entries buffer smaller than the table must fail loudly (ERR_CAPACITY,
 * required count reported), never silently drop entries. */
static void test_btt_capacity(void) {
    static uint8_t buf[65536];
    membuf m = { buf, sizeof buf, 0, 0 };

    binpazer_guid wg = {{0x03}};
    binpazer_type_def defs[3] = { {1, {{0x31}}, "A"}, {2, {{0x32}}, "B"}, {3, {{0x33}}, "C"} };
    binpazer_writer w;
    binpazer_writer_begin(&w, mem_write, mem_seek, &m, &wg, "demo", defs, 3);
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "btt-cap: write file");

    m.pos = 0;
    binpazer_reader r; char nm[16]; binpazer_type_entry te[2]; size_t nte = 0;
    CHECK(binpazer_reader_begin(&r, mem_read, mem_seek, &m, nm, sizeof nm, te, 2, &nte)
              == BINPAZER_ERR_CAPACITY,
          "btt-cap: undersized buffer -> ERR_CAPACITY");
    CHECK(nte == 4, "btt-cap: required count reported");

    /* entries == NULL opts out of the table -- still OK */
    m.pos = 0;
    binpazer_reader r2; size_t n2 = 0;
    CHECK(binpazer_reader_begin(&r2, mem_read, mem_seek, &m, NULL, 0, NULL, 0, &n2)
              == BINPAZER_OK,
          "btt-cap: NULL entries opt-out is OK");

    /* an exactly-sized buffer stores everything */
    m.pos = 0;
    binpazer_reader r3; binpazer_type_entry te3[4]; size_t n3 = 0;
    CHECK(binpazer_reader_begin(&r3, mem_read, mem_seek, &m, NULL, 0, te3, 4, &n3)
              == BINPAZER_OK && n3 == 4,
          "btt-cap: exact-size buffer OK");
    CHECK(te3[0].type_id == BINPAZER_TYPE_BLOCK_TYPE_TABLE && te3[3].type_id == 3,
          "btt-cap: entries intact");
}

/* Group blocks: nested blocks parsed out of one payload, matching what the Go
 * library's ParseGroupPayload returns for the same bytes. */
static void put16(uint8_t *p, uint16_t v) { p[0]=(uint8_t)v; p[1]=(uint8_t)(v>>8); }
static void put32(uint8_t *p, uint32_t v) { for (int i=0;i<4;i++) p[i]=(uint8_t)(v>>(8*i)); }

/* CRC-32C one bit at a time, straight from the polynomial: the definition the
 * table-driven library function must match. */
static uint32_t crc32c_bitwise(uint32_t crc, const uint8_t *p, size_t n) {
    crc = ~crc;
    for (size_t i = 0; i < n; i++) {
        crc ^= p[i];
        for (int k = 0; k < 8; k++) crc = (crc >> 1) ^ (0x82F63B78u & ((uint32_t)0 - (crc & 1u)));
    }
    return ~crc;
}

static void test_crc32c(void) {
    CHECK(binpazer_crc32c(0, "123456789", 9) == 0xE3069283u, "crc: check value of the standard test string");
    CHECK(binpazer_crc32c(0, "", 0) == 0, "crc: empty input");

    uint8_t buf[320];
    uint32_t x = 0x9E3779B9u;
    for (size_t i = 0; i < sizeof buf; i++) {
        x ^= x << 13; x ^= x >> 17; x ^= x << 5;
        buf[i] = (uint8_t)x;
    }
    /* Every length across several word boundaries, at every start alignment. */
    int same = 1;
    for (size_t start = 0; start < 8; start++)
        for (size_t len = 0; start + len <= sizeof buf; len++)
            if (binpazer_crc32c(0, buf + start, len) != crc32c_bitwise(0, buf + start, len)) same = 0;
    CHECK(same, "crc: matches the bitwise definition at every length and alignment");

    /* Chaining over any split point gives the CRC of the whole. */
    uint32_t whole = crc32c_bitwise(0, buf, sizeof buf);
    int chained = 1;
    for (size_t cut = 0; cut <= sizeof buf; cut++) {
        uint32_t c = binpazer_crc32c(0, buf, cut);
        if (binpazer_crc32c(c, buf + cut, sizeof buf - cut) != whole) chained = 0;
    }
    CHECK(chained, "crc: chained calls match one call at every split");
}

static void test_group_parse(void) {
    /* Children: one type "abcd" with a CRC, a later type "xy" without. */
    uint8_t g[64];
    memset(g, 0, sizeof g);
    size_t off = 0;

    put16(g + off, 1); put16(g + off + 2, BINPAZER_FLAG_HAS_CRC); put32(g + off + 4, 4);
    memcpy(g + off + 8, "abcd", 4);
    uint32_t crc = binpazer_crc32c(0, g + off, 8);
    crc = binpazer_crc32c(crc, g + off + 8, 4);
    put32(g + off + 12, crc);
    off = 16;

    put16(g + off, 2); put16(g + off + 2, 0); put32(g + off + 4, 2);
    memcpy(g + off + 8, "xy", 2);
    off = 32; /* */

    binpazer_group_child kids[4];
    size_t n = 0;
    CHECK(binpazer_group_parse(g, off, kids, 4, &n) == BINPAZER_OK, "group: parse ok");
    CHECK(n == 2, "group: two children");
    CHECK(kids[0].type_id == 1 && kids[0].payload_len == 4, "group: child 0 header");
    CHECK(memcmp(kids[0].payload, "abcd", 4) == 0, "group: child 0 payload");
    CHECK(kids[0].crc_ok, "group: child 0 crc verified");
    CHECK(kids[1].type_id == 2 && kids[1].payload_len == 2, "group: child 1 header");
    CHECK(kids[1].offset == 16, "group: child 1 offset");

    /* Counting pass: no buffer, capacity reported rather than truncated. */
    size_t only = 0;
    CHECK(binpazer_group_parse(g, off, NULL, 0, &only) == BINPAZER_ERR_CAPACITY, "group: count needs capacity");
    CHECK(only == 2, "group: counted without a buffer");

    /* A corrupted CRC is reported per child, not as a parse failure. */
    g[12] ^= 0xFF;
    CHECK(binpazer_group_parse(g, off, kids, 4, &n) == BINPAZER_OK, "group: bad crc still parses");
    CHECK(!kids[0].crc_ok && kids[1].crc_ok, "group: bad crc flagged on the right child");
    g[12] ^= 0xFF;

    /* A child claiming more than the group holds stops the walk, keeping the children already parsed. */
    put32(g + 16 + 4, 999);
    CHECK(binpazer_group_parse(g, off, kids, 4, &n) == BINPAZER_ERR_FORMAT, "group: overlong child rejected");
    CHECK(n == 1, "group: intact prefix survives");
}

static void test_streaming_compressed(void) {
    static uint8_t buf[4096];
    membuf m = { buf, sizeof buf, 0, 0 };
    binpazer_guid wg = {{0x01}};
    binpazer_type_def defs[1] = { {1, {{0x11}}, "P"} };

    binpazer_writer w;
    CHECK(binpazer_writer_begin_version(&w, mem_write, NULL, &m, &wg, "stream", defs, 1,
                                        BINPAZER_VERSION_MINOR_COMPRESSION) == BINPAZER_OK,
          "streaming: begin at 1.1");
    /* stored needs no codec library, so the core test binary can do this. */
    CHECK(binpazer_writer_block_compressed(&w, 1, 0, BINPAZER_CODEC_STORED, NULL,
                                           "payload", 7, 7) == BINPAZER_OK,
          "streaming: compressed block without a seek callback");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "streaming: end");

    m.pos = 0;
    binpazer_reader r; char nm[32]; binpazer_type_entry te[4]; size_t nte = 0;
    CHECK(binpazer_reader_begin(&r, mem_read, mem_seek, &m, nm, sizeof nm, te, 4, &nte) == BINPAZER_OK,
          "streaming: reader begin");
    CHECK(r.version_minor == BINPAZER_VERSION_MINOR_COMPRESSION, "streaming: file declares 1.1");

    binpazer_block blk;
    CHECK(binpazer_reader_next(&r, te, nte, &blk) == BINPAZER_OK, "streaming: next");
    CHECK((blk.flags & BINPAZER_FLAG_COMPRESSED) != 0, "streaming: compressed flag");
    uint8_t pb[64];
    CHECK(binpazer_reader_read_payload(&r, &blk, pb, sizeof pb) == BINPAZER_OK, "streaming: read payload");
    binpazer_compression c;
    CHECK(binpazer_compression_parse(pb, blk.payload_len, &c) == BINPAZER_OK, "streaming: envelope parses");
    CHECK(c.codec_id == BINPAZER_CODEC_STORED && c.uncompressed_length == 7, "streaming: envelope fields");
    CHECK(memcmp(pb + c.header_size, "payload", 7) == 0, "streaming: stored bytes");

    /* Without the up-front declaration it is still refused: nothing can raise the version afterwards. */
    membuf m2 = { buf, sizeof buf, 0, 0 };
    binpazer_writer w2;
    binpazer_writer_begin(&w2, mem_write, NULL, &m2, &wg, "stream", defs, 1);
    CHECK(binpazer_writer_block_compressed(&w2, 1, 0, BINPAZER_CODEC_STORED, NULL,
                                           "payload", 7, 7) == BINPAZER_ERR_NOSEEK,
          "streaming: 1.0 header + no seek is still refused");
}

/* Streaming a payload: peak memory is the scratch buffer, not the payload, and the CRC is still verified. */
typedef struct { uint8_t *dst; size_t cap, len; size_t chunks; } sink_ctx;

static binpazer_status collect(void *ctx, const void *buf, size_t n) {
    sink_ctx *s = (sink_ctx *)ctx;
    if (s->len + n > s->cap) return BINPAZER_ERR_CAPACITY;
    memcpy(s->dst + s->len, buf, n);
    s->len += n;
    s->chunks++;
    return BINPAZER_OK;
}

static binpazer_status refuse(void *ctx, const void *buf, size_t n) {
    (void)ctx; (void)buf; (void)n;
    return BINPAZER_ERR_IO;
}

static void test_stream_payload(void) {
    static uint8_t fbuf[8192];
    membuf m = { fbuf, sizeof fbuf, 0, 0 };
    binpazer_guid wg = {{0x01}};
    binpazer_type_def defs[1] = { {1, {{0x11}}, "P"} };

    uint8_t big[1000];
    for (size_t i = 0; i < sizeof big; i++) big[i] = (uint8_t)(i * 7 + 3);

    binpazer_writer w;
    binpazer_writer_begin(&w, mem_write, mem_seek, &m, &wg, "stream", defs, 1);
    binpazer_writer_block(&w, 1, BINPAZER_FLAG_HAS_CRC, big, sizeof big);
    binpazer_writer_block(&w, 1, 0, "tail", 4);
    binpazer_writer_end(&w);

    m.pos = 0;
    binpazer_reader r; char nm[32]; binpazer_type_entry te[4]; size_t nte = 0;
    binpazer_reader_begin(&r, mem_read, mem_seek, &m, nm, sizeof nm, te, 4, &nte);

    binpazer_block blk;
    CHECK(binpazer_reader_next(&r, te, nte, &blk) == BINPAZER_OK, "stream: next");

    uint8_t out[2048];
    uint8_t scratch[64];   /* far smaller than the payload -- the whole point */
    sink_ctx sc = { out, sizeof out, 0, 0 };
    CHECK(binpazer_reader_stream_payload(&r, &blk, scratch, sizeof scratch, collect, &sc) == BINPAZER_OK,
          "stream: payload streamed");
    CHECK(sc.len == sizeof big && memcmp(out, big, sizeof big) == 0, "stream: bytes reassembled");
    CHECK(sc.chunks == (sizeof big + 63) / 64, "stream: chunked at the scratch size");

    /* The reader is positioned on the next block afterwards. */
    CHECK(binpazer_reader_next(&r, te, nte, &blk) == BINPAZER_OK, "stream: advanced past the block");
    CHECK(blk.payload_len == 4, "stream: next block reached");

    /* A sink that refuses aborts with its own status. */
    m.pos = 0;
    binpazer_reader r2; size_t nte2 = 0;
    binpazer_reader_begin(&r2, mem_read, mem_seek, &m, nm, sizeof nm, te, 4, &nte2);
    binpazer_reader_next(&r2, te, nte2, &blk);
    CHECK(binpazer_reader_stream_payload(&r2, &blk, scratch, sizeof scratch, refuse, NULL) == BINPAZER_ERR_IO,
          "stream: sink refusal propagates");

    /* A corrupted payload is caught by the CRC, after the fact. */
    fbuf[blk.payload_offset + 10] ^= 0xFF;
    m.pos = 0;
    binpazer_reader r3; size_t nte3 = 0;
    binpazer_reader_begin(&r3, mem_read, mem_seek, &m, nm, sizeof nm, te, 4, &nte3);
    binpazer_reader_next(&r3, te, nte3, &blk);
    sc.len = 0; sc.chunks = 0;
    CHECK(binpazer_reader_stream_payload(&r3, &blk, scratch, sizeof scratch, collect, &sc) == BINPAZER_ERR_CRC,
          "stream: corrupted payload fails the CRC");
}

static void test_guid_text(void) {
    binpazer_guid g = {{0xaa,0xbb,0xcc,0xdd, 0x00,0x01, 0x02,0x03, 0x04,0x05,
                        0x06,0x07,0x08,0x09,0x0a,0x0b}};
    char buf[BINPAZER_GUID_STRING_SIZE];
    CHECK(binpazer_guid_format(&g, buf, sizeof buf) == BINPAZER_OK, "guid: format");
    CHECK(strcmp(buf, "aabbccdd-0001-0203-0405-060708090a0b") == 0, "guid: canonical text");

    char small[8];
    CHECK(binpazer_guid_format(&g, small, sizeof small) == BINPAZER_ERR_CAPACITY, "guid: short buffer refused");

    binpazer_guid back;
    CHECK(binpazer_guid_parse(buf, &back) == BINPAZER_OK, "guid: parse");
    CHECK(memcmp(back.bytes, g.bytes, 16) == 0, "guid: round-trip");

    /* Hyphens are separators, not data. */
    CHECK(binpazer_guid_parse("aabbccdd0001020304050607" "08090a0b", &back) == BINPAZER_OK, "guid: parse unhyphenated");
    CHECK(memcmp(back.bytes, g.bytes, 16) == 0, "guid: unhyphenated round-trip");

    CHECK(binpazer_guid_parse("aabb", &back) == BINPAZER_ERR_FORMAT, "guid: too short rejected");
    CHECK(binpazer_guid_parse("zzbbccdd-0001-0203-0405-060708090a0b", &back) == BINPAZER_ERR_FORMAT,
          "guid: non-hex rejected");
    CHECK(binpazer_guid_parse("aabbccdd-0001-0203-0405-060708090a0b0", &back) == BINPAZER_ERR_FORMAT,
          "guid: too long rejected");
}

static void test_compression(void) {
    static uint8_t buf[65536];
    membuf m = { buf, sizeof buf, 0, 0 };

    binpazer_guid writer  = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_note  = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
    binpazer_guid g_codec = {{0xc0,0xde,0xc0,0xde,0,0,0,0,0,0,0,0,0,0,0,7}};
    binpazer_type_def defs[1] = { { 2, {{0}}, "Note" } };
    defs[0].guid = g_note;

    const char *plain      = "plain block";           /* Several bytes */
    const char *compressed = "compressed, sort of";   /* Several bytes */
    const char *guidcodec  = "guid-codec payload";    /* Several bytes */

    binpazer_writer w;
    CHECK(binpazer_writer_begin(&w, mem_write, mem_seek, &m, &writer, "demo",
                                defs, 1) == BINPAZER_OK, "compress: begin");
    CHECK(binpazer_writer_block(&w, 2, 0, plain, 11) == BINPAZER_OK, "compress: plain block");
    CHECK(binpazer_writer_block_compressed(&w, 2, BINPAZER_FLAG_HAS_CRC,
                                           BINPAZER_CODEC_STORED, NULL,
                                           compressed, 19, 19) == BINPAZER_OK,
          "compress: stored block");
    CHECK(binpazer_writer_block_compressed(&w, 2, 0, BINPAZER_CODEC_BY_GUID, &g_codec,
                                           guidcodec, 18, 18) == BINPAZER_OK,
          "compress: guid-codec block");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "compress: end");

    /* ---- read back ---- */
    m.pos = 0;
    binpazer_reader r;
    binpazer_type_entry entries[8];
    size_t nent = 0;
    CHECK(binpazer_reader_begin(&r, mem_read, mem_seek, &m, NULL, 0,
                                entries, 8, &nent) == BINPAZER_OK, "compress: reader_begin");
    CHECK(r.version_minor == BINPAZER_VERSION_MINOR_COMPRESSION, "compress: version_minor is 1.1");

    binpazer_block b;
    uint8_t payload[256];

    CHECK(binpazer_reader_next(&r, entries, nent, &b) == BINPAZER_OK, "compress: block 0");
    CHECK((b.flags & BINPAZER_FLAG_COMPRESSED) == 0, "compress: block 0 is not compressed");
    CHECK(binpazer_reader_read_payload(&r, &b, payload, sizeof payload) == BINPAZER_OK, "compress: read 0");
    CHECK(memcmp(payload, plain, 11) == 0, "compress: block 0 payload");

    binpazer_compression c;
    CHECK(binpazer_reader_next(&r, entries, nent, &b) == BINPAZER_OK, "compress: block 1");
    CHECK((b.flags & BINPAZER_FLAG_COMPRESSED) != 0, "compress: block 1 is compressed");
    CHECK(binpazer_reader_read_payload(&r, &b, payload, sizeof payload) == BINPAZER_OK, "compress: read 1");
    CHECK(binpazer_compression_parse(payload, b.payload_len, &c) == BINPAZER_OK, "compress: parse 1");
    CHECK(c.codec_id == BINPAZER_CODEC_STORED, "compress: block 1 codec is stored");
    CHECK(c.uncompressed_length == 19, "compress: block 1 uncompressed_length");
    CHECK(c.stored_length == 19, "compress: block 1 stored_length");
    CHECK(c.header_size == BINPAZER_ENVELOPE_FIXED_SIZE, "compress: block 1 header size");
    CHECK(memcmp(payload + c.header_size, compressed, 19) == 0, "compress: block 1 codec bytes");

    CHECK(binpazer_reader_next(&r, entries, nent, &b) == BINPAZER_OK, "compress: block 2");
    CHECK(binpazer_reader_read_payload(&r, &b, payload, sizeof payload) == BINPAZER_OK, "compress: read 2");
    CHECK(binpazer_compression_parse(payload, b.payload_len, &c) == BINPAZER_OK, "compress: parse 2");
    CHECK(c.codec_id == BINPAZER_CODEC_BY_GUID, "compress: block 2 codec is the GUID escape");
    CHECK(c.header_size == BINPAZER_ENVELOPE_GUID_SIZE, "compress: block 2 header size");
    CHECK(memcmp(c.codec_guid.bytes, g_codec.bytes, 16) == 0, "compress: block 2 codec guid");
    CHECK(memcmp(payload + c.header_size, guidcodec, 18) == 0, "compress: block 2 codec bytes");

    /* Envelope validation: codec one id. */
    uint8_t bad[BINPAZER_ENVELOPE_FIXED_SIZE] = {0};
    CHECK(binpazer_compression_parse(bad, sizeof bad, &c) == BINPAZER_ERR_FORMAT, "compress: codec id 0 rejected");
    bad[0] = BINPAZER_CODEC_STORED; bad[2] = 1;
    CHECK(binpazer_compression_parse(bad, sizeof bad, &c) == BINPAZER_ERR_FORMAT, "compress: codec_flags rejected");
    bad[2] = 0; bad[4] = 1;
    CHECK(binpazer_compression_parse(bad, sizeof bad, &c) == BINPAZER_ERR_FORMAT, "compress: reserved rejected");
    bad[4] = 0;
    CHECK(binpazer_compression_parse(bad, 8, &c) == BINPAZER_ERR_FORMAT, "compress: short payload rejected");

    /* A compressed block needs a seekable writer: version_minor is back-patched. */
    membuf m2 = { buf, sizeof buf, 0, 0 };
    binpazer_writer w2;
    CHECK(binpazer_writer_begin(&w2, mem_write, NULL, &m2, &writer, "demo",
                                defs, 1) == BINPAZER_OK, "compress: begin without seek");
    CHECK(binpazer_writer_block_compressed(&w2, 2, 0, BINPAZER_CODEC_STORED, NULL,
                                           compressed, 19, 19) == BINPAZER_ERR_NOSEEK,
          "compress: non-seekable writer refuses a compressed block");
}

int main(void) {
    static uint8_t storage[65536];
    membuf m = { storage, sizeof storage, 0, 0 };

    binpazer_guid writer = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_mesh = {{0x11,0x22,0x33,0x44,0x55,0x66,0x77,0x88,0x99,0xaa,0xbb,0xcc,0xdd,0xee,0xff,0x00}};
    binpazer_guid g_note = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};

    binpazer_type_def defs[] = {
        { 1, {{0}}, "Mesh" },
        { 2, {{0}}, "Note" },
    };
    defs[0].guid = g_mesh;
    defs[1].guid = g_note;

    const char *mesh_payload = "vertices-go-here";          /* Several bytes */
    const char *note_payload = "hello, file!";              /* Several bytes */
    const char *big_payload  = "0123456789ABCDEF01234567";  /* Many bytes */

    /* ---- write ---- */
    binpazer_writer w;
    CHECK(binpazer_writer_begin(&w, mem_write, mem_seek, &m, &writer, "demo",
                                defs, 2) == BINPAZER_OK, "writer_begin");
    /* a critical block with a CRC */
    CHECK(binpazer_writer_block(&w, 1, BINPAZER_FLAG_CRITICAL | BINPAZER_FLAG_HAS_CRC,
                                mesh_payload, 16) == BINPAZER_OK, "write mesh");
    /* an ancillary block */
    CHECK(binpazer_writer_block(&w, 2, 0, note_payload, 12) == BINPAZER_OK, "write note");
    /* a third (normal) block, repeating type one id */
    CHECK(binpazer_writer_block(&w, 1, 0,
                                big_payload, 24) == BINPAZER_OK, "write third");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "writer_end");
    CHECK(w.err == BINPAZER_OK, "writer no error");

    size_t written = m.len;
    printf("wrote %zu bytes\n", written);

    /* file_length must have been back-patched via seek */
    uint64_t recorded_len = (uint64_t)storage[32] | ((uint64_t)storage[33]<<8) |
                            ((uint64_t)storage[34]<<16) | ((uint64_t)storage[35]<<24) |
                            ((uint64_t)storage[36]<<32) | ((uint64_t)storage[37]<<40) |
                            ((uint64_t)storage[38]<<48) | ((uint64_t)storage[39]<<56);
    CHECK(recorded_len == written, "file_length patched");

    /* ---- read ---- */
    m.pos = 0;
    binpazer_reader r;
    char name[64];
    binpazer_type_entry entries[8];
    size_t nentries = 0;
    CHECK(binpazer_reader_begin(&r, mem_read, mem_seek, &m, name, sizeof name,
                                entries, 8, &nentries) == BINPAZER_OK, "reader_begin");
    CHECK(strcmp(name, "demo") == 0, "writer name");
    CHECK(r.version_major == 1 && r.version_minor == 0, "version");
    CHECK(memcmp(r.writer_guid.bytes, writer.bytes, 16) == 0, "writer guid");
    CHECK(nentries == 3, "btt entry count (BTT + Mesh + Note)");
    CHECK(entries[0].type_id == BINPAZER_TYPE_BLOCK_TYPE_TABLE, "entry0 is BTT");
    CHECK(memcmp(entries[0].guid.bytes, BINPAZER_GUID_BLOCK_TYPE_TABLE.bytes, 16) == 0, "entry0 guid");
    CHECK(entries[1].type_id == 1 && memcmp(entries[1].guid.bytes, g_mesh.bytes,16)==0, "entry1 Mesh");
    CHECK(entries[2].type_id == 2 && memcmp(entries[2].guid.bytes, g_note.bytes,16)==0, "entry2 Note");

    binpazer_block blk;
    uint8_t pbuf[64];

    /* One block: Mesh, critical + crc */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next mesh");
    CHECK(blk.type_id == 1 && blk.known, "mesh type/known");
    CHECK(memcmp(blk.guid.bytes, g_mesh.bytes, 16) == 0, "mesh guid resolved");
    CHECK((blk.flags & BINPAZER_FLAG_CRITICAL) && (blk.flags & BINPAZER_FLAG_HAS_CRC), "mesh flags");
    CHECK(blk.payload_len == 16, "mesh len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read mesh payload (crc ok)");
    CHECK(memcmp(pbuf, mesh_payload, 16) == 0, "mesh payload bytes");

    /* A later block: Note, ancillary */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next note");
    CHECK(blk.type_id == 2 && blk.payload_len == 12, "note type/len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read note payload");
    CHECK(memcmp(pbuf, note_payload, 12) == 0, "note payload bytes");

    /* A later block: a normal repeat of one type */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next third");
    CHECK(blk.type_id == 1 && (blk.flags & BINPAZER_FLAG_LARGE_MODE) == 0, "third normal");
    CHECK(blk.payload_len == 24, "third len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read third payload");
    CHECK(memcmp(pbuf, big_payload, 24) == 0, "third payload bytes");

    /* end */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_END, "end of blocks");

    test_crc32c();
    test_index();
    test_large();
    test_btt_capacity();
    test_compression();
    test_group_parse();
    test_streaming_compressed();
    test_stream_payload();
    test_guid_text();

    if (failures == 0) { printf("OK: all C round-trip checks passed\n"); return 0; }
    printf("%d FAILURE(S)\n", failures);
    return 1;
}
