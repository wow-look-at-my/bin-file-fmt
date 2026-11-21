/* Tests for appendable files in the binpazer C reference implementation: the
 * append writer, reading while the file grows, and resume. */
#include "binpazer.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* ---- one in-memory file, and cursors over it ---- */

/* A writer and a reader are processes with file positions, so each gets a
 * cursor of its own over the same bytes. */
typedef struct { uint8_t *data; size_t cap, len; } memfile;
typedef struct { memfile *f; size_t pos; } cursor;

static size_t cur_write(void *ctx, const void *buf, size_t n) {
    cursor *c = (cursor *)ctx;
    if (c->pos + n > c->f->cap) return 0;
    if (c->pos > c->f->len) memset(c->f->data + c->f->len, 0, c->pos - c->f->len);
    memcpy(c->f->data + c->pos, buf, n);
    c->pos += n;
    if (c->pos > c->f->len) c->f->len = c->pos;
    return n;
}
static size_t cur_read(void *ctx, void *buf, size_t n) {
    cursor *c = (cursor *)ctx;
    size_t avail = c->pos < c->f->len ? c->f->len - c->pos : 0;
    size_t got = n < avail ? n : avail;
    memcpy(buf, c->f->data + c->pos, got);
    c->pos += got;
    return got;
}
static int cur_seek(void *ctx, uint64_t off) {
    cursor *c = (cursor *)ctx;
    if (off > c->f->cap) return -1;
    c->pos = (size_t)off;
    return 0;
}

static int failures = 0;
#define CHECK(cond, msg) do { if (!(cond)) { printf("FAIL: %s (line %d)\n", msg, __LINE__); failures++; } } while (0)

#define FILE_CAP (4u << 20)
#define TYPE_NOTE 2
static const binpazer_guid kWriter = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
static const char *kRecords[3] = { "first record", "second record, a bit longer", "" };
static uint8_t scratch[4096];

static memfile new_file(void) {
    memfile f = { (uint8_t *)malloc(FILE_CAP), FILE_CAP, 0 };
    if (!f.data) { printf("FAIL: out of memory\n"); exit(1); }
    return f;
}

static binpazer_status begin_append(binpazer_writer *w, cursor *c, int seekable) {
    binpazer_type_def defs[1] = { {TYPE_NOTE, {{0xde,0xad,0xbe,0xef}}, "Note"} };
    return binpazer_writer_begin_append(w, cur_write, seekable ? cur_seek : NULL, c,
                                        &kWriter, "demo", defs, 1);
}

/* Writes the sample: the same blocks as gen_appendable in gen.c. */
static void write_sample(memfile *f, int end) {
    cursor c = { f, 0 };
    binpazer_writer w;
    CHECK(begin_append(&w, &c, 1) == BINPAZER_OK, "sample: begin");
    for (int i = 0; i < 3; i++)
        CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, kRecords[i], strlen(kRecords[i])) == BINPAZER_OK,
              "sample: block");
    if (end) CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "sample: end");
}

/* Follows `f` from its first block. Counts the committed blocks that are not
 * End or Padding, checks each record payload, and returns the status that
 * stopped the walk. `*end_offset` receives the reader's final offset. */
static binpazer_status follow(memfile *f, int *records, const char *extra, uint64_t *end_offset) {
    cursor c = { f, 0 };
    binpazer_reader r;
    binpazer_type_entry te[8]; size_t nte = 0;
    *records = 0;
    binpazer_status st = binpazer_reader_begin(&r, cur_read, cur_seek, &c, NULL, 0, te, 8, &nte);
    if (st != BINPAZER_OK) return st;
    for (;;) {
        binpazer_block blk;
        st = binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch);
        if (st != BINPAZER_OK) break;
        static uint8_t pb[4096];
        if (blk.type_id == BINPAZER_TYPE_PADDING || blk.payload_len > sizeof pb) {
            st = binpazer_reader_skip_payload(&r, &blk);
        } else {
            st = binpazer_reader_read_payload(&r, &blk, pb, sizeof pb);
            if (st == BINPAZER_OK && blk.type_id == TYPE_NOTE) {
                const char *want = *records < 3 ? kRecords[*records] : extra;
                CHECK(want && blk.payload_len == strlen(want) && memcmp(pb, want, blk.payload_len) == 0,
                      "follow: record payload");
                CHECK(blk.known, "follow: record type resolved");
                (*records)++;
            }
        }
        if (st != BINPAZER_OK) break;
    }
    if (end_offset) *end_offset = r.offset;
    CHECK(r.err == BINPAZER_OK || st < 0, "follow: pending and end leave no sticky error");
    return st;
}

/* Offsets just past each top-level block, by a plain walk. */
static size_t block_ends(memfile *f, uint64_t *ends, size_t cap, uint64_t *first_block) {
    cursor c = { f, 0 };
    binpazer_reader r;
    size_t n = 0;
    if (binpazer_reader_begin(&r, cur_read, cur_seek, &c, NULL, 0, NULL, 0, NULL) != BINPAZER_OK) return 0;
    if (first_block) *first_block = r.offset;
    binpazer_block blk;
    while (binpazer_reader_next(&r, NULL, 0, &blk) == BINPAZER_OK) {
        if (binpazer_reader_skip_payload(&r, &blk) != BINPAZER_OK) break;
        if (n < cap) ends[n] = r.offset;
        n++;
    }
    return n;
}

static void test_header_and_end(void) {
    memfile f = new_file();
    write_sample(&f, 1);

    cursor c = { &f, 0 };
    binpazer_reader r; binpazer_type_entry te[8]; size_t nte = 0;
    CHECK(binpazer_reader_begin(&r, cur_read, cur_seek, &c, NULL, 0, te, 8, &nte) == BINPAZER_OK, "header: begin");
    CHECK(r.header_flags & BINPAZER_HEADER_FLAG_APPENDABLE, "header: appendable flag");
    CHECK(r.version_minor == BINPAZER_VERSION_MINOR_APPENDABLE, "header: declares the appendable version");
    CHECK(r.file_length == f.len, "header: a seekable end back-patches file_length");
    CHECK(nte == 3 && te[2].type_id == BINPAZER_TYPE_END &&
          memcmp(te[2].guid.bytes, BINPAZER_GUID_END.bytes, 16) == 0, "header: End is in the type table");

    int n = 0;
    CHECK(follow(&f, &n, NULL, NULL) == BINPAZER_END && n == 3, "end: follower stops at the End block");

    /* The writer refuses more blocks, and a second end is harmless. */
    cursor wc = { &f, 0 };
    binpazer_writer w;
    memfile g = new_file();
    wc.f = &g;
    CHECK(begin_append(&w, &wc, 1) == BINPAZER_OK, "end: begin");
    CHECK(binpazer_writer_index(&w, NULL, 0) != BINPAZER_OK && w.err == BINPAZER_OK,
          "index: refused without poisoning the writer");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "end: end");
    size_t len = g.len;
    CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, "late", 4) == BINPAZER_ERR_ENDED, "end: block after end");
    CHECK(binpazer_writer_block_compressed(&w, TYPE_NOTE, 0, BINPAZER_CODEC_STORED, NULL, "late", 4, 4)
              == BINPAZER_ERR_ENDED, "end: compressed block after end");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK && g.len == len, "end: idempotent");
    free(g.data);
    free(f.data);
}

static void test_non_seekable_end(void) {
    memfile f = new_file();
    cursor c = { &f, 0 };
    binpazer_writer w;
    CHECK(begin_append(&w, &c, 0) == BINPAZER_OK, "stream: begin");
    CHECK(binpazer_writer_block_compressed(&w, TYPE_NOTE, 0, BINPAZER_CODEC_STORED, NULL, "abc", 3, 3)
              == BINPAZER_OK, "stream: compressed block needs no seek");
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "stream: end");

    cursor rc = { &f, 0 };
    binpazer_reader r;
    CHECK(binpazer_reader_begin(&r, cur_read, NULL, &rc, NULL, 0, NULL, 0, NULL) == BINPAZER_OK, "stream: read begin");
    CHECK(r.file_length == BINPAZER_FILE_LENGTH_UNKNOWN, "stream: file_length stays the sentinel");
    binpazer_block blk; int n = 0;
    while (binpazer_reader_next(&r, NULL, 0, &blk) == BINPAZER_OK) {
        CHECK(blk.flags & BINPAZER_FLAG_HAS_CRC, "stream: every block is sealed");
        if (binpazer_reader_skip_payload(&r, &blk) != BINPAZER_OK) break;
        n++;
    }
    CHECK(n == 2 && r.ended && r.err == BINPAZER_OK, "stream: the End block marks a clean close");
    CHECK(binpazer_reader_next_committed(&r, NULL, 0, &blk, scratch, sizeof scratch) == BINPAZER_ERR_NOSEEK,
          "stream: next_committed needs a seek callback");
    free(f.data);
}

/* The property the format promises: at every length the file can have while
 * it grows, a reader sees exactly the blocks that are wholly written. */
static void test_every_prefix(void) {
    memfile f = new_file();
    write_sample(&f, 1);
    uint64_t ends[8], first = 0;
    size_t nends = block_ends(&f, ends, 8, &first);
    CHECK(nends == 4, "prefix: three records and the End block");

    size_t full = f.len;
    for (size_t n = (size_t)first; n <= full; n++) {
        f.len = n;
        int want = 0, got = 0;
        for (size_t i = 0; i < nends; i++) if (ends[i] <= n) want++;
        uint64_t at = 0;
        binpazer_status st = follow(&f, &got, NULL, &at);
        if (want == 4) {
            CHECK(st == BINPAZER_END && got == 3, "prefix: whole file ends");
        } else {
            CHECK(st == BINPAZER_PENDING, "prefix: a short file is pending, not torn");
            CHECK(got == want, "prefix: only whole blocks are surfaced");
            CHECK(at == (want ? ends[want - 1] : first), "prefix: a pending reader stays at the block start");
        }
    }
    f.len = full;
    free(f.data);
}

static void test_growing_file(void) {
    memfile f = new_file();
    cursor wc = { &f, 0 }, rc = { &f, 0 };
    binpazer_writer w;
    CHECK(begin_append(&w, &wc, 1) == BINPAZER_OK, "grow: begin");

    binpazer_reader r; binpazer_type_entry te[8]; size_t nte = 0;
    CHECK(binpazer_reader_begin(&r, cur_read, cur_seek, &rc, NULL, 0, te, 8, &nte) == BINPAZER_OK, "grow: read begin");
    binpazer_block blk; uint8_t pb[64];
    uint64_t at = r.offset;
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_PENDING, "grow: nothing yet");
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_PENDING && r.offset == at,
          "grow: pending again, reader has not moved");

    CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, "one", 3) == BINPAZER_OK, "grow: append");
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_OK, "grow: block arrives");
    CHECK(blk.known && blk.payload_len == 3 && (blk.flags & BINPAZER_FLAG_HAS_CRC), "grow: header");
    CHECK(binpazer_reader_read_payload(&r, &blk, pb, sizeof pb) == BINPAZER_OK && memcmp(pb, "one", 3) == 0, "grow: payload");
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_PENDING, "grow: caught up");

    /* A payload larger than the scratch buffer is verified in pieces. */
    static uint8_t big[3 * sizeof scratch + 5], back[sizeof big];
    memset(big, 'x', sizeof big);
    CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, big, sizeof big) == BINPAZER_OK, "grow: append big");
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_OK &&
          blk.payload_len == sizeof big, "grow: big block arrives");
    CHECK(binpazer_reader_read_payload(&r, &blk, back, sizeof back) == BINPAZER_OK &&
          memcmp(back, big, sizeof big) == 0, "grow: big payload");

    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "grow: end");
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_OK &&
          blk.type_id == BINPAZER_TYPE_END, "grow: the End block is surfaced");
    CHECK(binpazer_reader_skip_payload(&r, &blk) == BINPAZER_OK, "grow: skip End");
    CHECK(binpazer_reader_next_committed(&r, te, nte, &blk, scratch, sizeof scratch) == BINPAZER_END, "grow: then the end");
    free(f.data);
}

static void test_torn(void) {
    memfile f = new_file();
    write_sample(&f, 0);
    uint64_t ends[8], first = 0;
    size_t nends = block_ends(&f, ends, 8, &first);
    CHECK(nends == 3, "torn: three records");
    int n = 0; uint64_t at = 0;

    f.data[first + 8] ^= 0xFF; /* a payload byte of the first record */
    CHECK(follow(&f, &n, NULL, &at) == BINPAZER_TORN && n == 0 && at == first, "torn: bad seal stops the walk there");
    f.data[first + 8] ^= 0xFF;

    f.data[ends[1] + 2] &= (uint8_t)~BINPAZER_FLAG_HAS_CRC; /* the last record loses its seal */
    CHECK(follow(&f, &n, NULL, NULL) == BINPAZER_TORN && n == 2, "torn: an unsealed block is not committed");
    f.data[ends[1] + 2] |= BINPAZER_FLAG_HAS_CRC;

    memset(f.data + f.len, 0, 64); /* zeroed space after the last block */
    f.len += 64;
    CHECK(follow(&f, &n, NULL, NULL) == BINPAZER_PENDING && n == 3, "torn: zeroed space is pending");
    free(f.data);
}

static void test_not_appendable(void) {
    memfile f = new_file();
    cursor c = { &f, 0 };
    binpazer_writer w;
    binpazer_type_def defs[1] = { {TYPE_NOTE, {{0xde}}, "Note"} };
    binpazer_writer_begin(&w, cur_write, cur_seek, &c, &kWriter, "demo", defs, 1);
    binpazer_writer_block(&w, TYPE_NOTE, BINPAZER_FLAG_HAS_CRC, "x", 1);
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "plain: write");

    int n = 0;
    CHECK(follow(&f, &n, NULL, NULL) == BINPAZER_ERR_NOT_APPENDABLE, "plain: next_committed refuses");
    cursor rc = { &f, 0 };
    binpazer_writer w2;
    CHECK(binpazer_writer_resume(&w2, cur_read, cur_write, cur_seek, &rc, NULL, 0, NULL, scratch, sizeof scratch)
              == BINPAZER_ERR_NOT_APPENDABLE, "plain: resume refuses");
    free(f.data);
}

static binpazer_status resume(binpazer_writer *w, cursor *c) {
    binpazer_type_entry te[8]; size_t nte = 0;
    binpazer_status st = binpazer_writer_resume(w, cur_read, cur_write, cur_seek, c, te, 8, &nte,
                                                scratch, sizeof scratch);
    if (st == BINPAZER_OK) CHECK(nte == 3 && te[1].type_id == TYPE_NOTE, "resume: type table returned");
    return st;
}

static void test_resume(void) {
    memfile f = new_file();
    write_sample(&f, 0);
    size_t clean = f.len;
    uint8_t *saved = (uint8_t *)malloc(clean);
    memcpy(saved, f.data, clean);
    int n = 0; uint64_t at = 0;

    /* A clean file resumes at its end, with no padding. */
    cursor c = { &f, 0 };
    binpazer_writer w;
    CHECK(resume(&w, &c) == BINPAZER_OK && w.offset == clean && f.len == clean, "resume: clean file");
    CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, "after restart", 13) == BINPAZER_OK, "resume: append");
    CHECK(follow(&f, &n, "after restart", NULL) == BINPAZER_PENDING && n == 4, "resume: old and new records");

    /* Torn tails of many lengths: a header that promises more than was written. */
    static const size_t tails[] = { 1, 7, 8, 9, 15, 16, 17, 100, 4096 + 3 };
    for (size_t t = 0; t < sizeof tails / sizeof tails[0]; t++) {
        size_t tail = tails[t];
        memcpy(f.data, saved, clean);
        memset(f.data + clean, 0xAB, tail);
        if (tail >= 8) {
            uint8_t torn[8] = { TYPE_NOTE, 0, BINPAZER_FLAG_HAS_CRC, 0, 0, 0, 0x10, 0 }; /* */
            memcpy(f.data + clean, torn, 8);
        }
        f.len = clean + tail;

        cursor tc = { &f, 0 };
        CHECK(resume(&w, &tc) == BINPAZER_OK, "resume: torn tail");
        CHECK(w.offset >= clean + tail && w.offset % 8 == 0 && w.offset == f.len, "resume: padding covers every stale byte");
        CHECK(memcmp(f.data, saved, clean) == 0, "resume: committed bytes are untouched");
        CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, "recovered", 9) == BINPAZER_OK, "resume: append after a torn tail");
        CHECK(follow(&f, &n, "recovered", &at) == BINPAZER_PENDING && n == 4 && at == f.len,
              "resume: the file is whole again");
    }

    size_t tail = 2 * ((size_t)1 << 20) + 100;
    memcpy(f.data, saved, clean);
    memset(f.data + clean, 0xAB, tail);
    f.len = clean + tail;
    cursor lc = { &f, 0 };
    CHECK(resume(&w, &lc) == BINPAZER_OK && w.offset >= clean + tail, "resume: long tail");
    {
        cursor rc = { &f, 0 };
        binpazer_reader r; binpazer_block blk; int pads = 0;
        binpazer_reader_begin(&r, cur_read, cur_seek, &rc, NULL, 0, NULL, 0, NULL);
        while (binpazer_reader_next_committed(&r, NULL, 0, &blk, scratch, sizeof scratch) == BINPAZER_OK) {
            if (blk.type_id == BINPAZER_TYPE_PADDING) pads++;
            uint64_t next = (blk.payload_offset + blk.payload_len + 4 + 7) & ~(uint64_t)7;
            cur_seek(&rc, next); r.offset = next;
        }
        CHECK(pads == 3 && r.offset == f.len, "resume: long tail takes three padding blocks");
    }

    /* An ended file does not resume. */
    memfile e = new_file();
    write_sample(&e, 1);
    cursor ec = { &e, 0 };
    CHECK(resume(&w, &ec) == BINPAZER_ERR_ENDED, "resume: ended file refuses");
    free(e.data);
    free(saved);
    free(f.data);
}

/* A torn tail can hold a complete sealed block of its own. Resume must not
 * let it surface as committed. */
static void test_resume_hides_ghost(void) {
    memfile f = new_file();
    write_sample(&f, 0);
    size_t clean = f.len;

    memfile g = new_file();
    cursor gc = { &g, 0 };
    binpazer_writer gw;
    begin_append(&gw, &gc, 1);
    size_t start = g.len;
    binpazer_writer_block(&gw, TYPE_NOTE, 0, "ghost", 5);

    uint8_t torn[24] = { TYPE_NOTE, 0, BINPAZER_FLAG_HAS_CRC, 0, 0, 0, 0x10, 0 };
    memcpy(f.data + clean, torn, sizeof torn);
    memcpy(f.data + clean + sizeof torn, g.data + start, g.len - start);
    f.len = clean + sizeof torn + (g.len - start);

    cursor c = { &f, 0 };
    binpazer_writer w;
    CHECK(resume(&w, &c) == BINPAZER_OK, "ghost: resume");
    CHECK(binpazer_writer_block(&w, TYPE_NOTE, 0, "real", 4) == BINPAZER_OK, "ghost: append");
    int n = 0;
    CHECK(follow(&f, &n, "real", NULL) == BINPAZER_PENDING && n == 4, "ghost: only real records surface");
    free(g.data);
    free(f.data);
}

int main(void) {
    test_header_and_end();
    test_non_seekable_end();
    test_every_prefix();
    test_growing_file();
    test_torn();
    test_not_appendable();
    test_resume();
    test_resume_hides_ghost();

    if (failures == 0) { printf("OK: all C appendable-file checks passed\n"); return 0; }
    printf("%d FAILURE(S)\n", failures);
    return 1;
}
