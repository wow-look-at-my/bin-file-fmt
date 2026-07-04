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
 * find blocks by type without walking, and confirm the no-index walk fallback. */
static void test_index(void) {
    static uint8_t bufA[65536], bufB[65536];
    membuf A = { bufA, sizeof bufA, 0, 0 };
    membuf B = { bufB, sizeof bufB, 0, 0 };

    binpazer_guid wg = {{0x01}};
    binpazer_guid g1 = {{0x11}}, g2 = {{0x22}};
    binpazer_type_def defs[2] = { {1, {{0}}, "A"}, {2, {{0}}, "B"} };
    defs[0].guid = g1; defs[1].guid = g2;

    /* file A: three blocks + index + footer */
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

    /* 4 entries on disk (BTT + 3 user types); a 2-entry buffer is too small */
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

    const char *mesh_payload = "vertices-go-here";          /* 16 bytes */
    const char *note_payload = "hello, file!";              /* 12 bytes */
    const char *big_payload  = "0123456789ABCDEF01234567";  /* 24 bytes */

    /* ---- write ---- */
    binpazer_writer w;
    CHECK(binpazer_writer_begin(&w, mem_write, mem_seek, &m, &writer, "demo",
                                defs, 2) == BINPAZER_OK, "writer_begin");
    /* a critical block with a CRC */
    CHECK(binpazer_writer_block(&w, 1, BINPAZER_FLAG_CRITICAL | BINPAZER_FLAG_HAS_CRC,
                                mesh_payload, 16) == BINPAZER_OK, "write mesh");
    /* an ancillary block */
    CHECK(binpazer_writer_block(&w, 2, 0, note_payload, 12) == BINPAZER_OK, "write note");
    /* a third (normal) block, repeating type id 1 */
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
    /* entry 0 is the auto BTT (65534), then Mesh(1), Note(2) */
    CHECK(entries[0].type_id == BINPAZER_TYPE_BLOCK_TYPE_TABLE, "entry0 is BTT");
    CHECK(memcmp(entries[0].guid.bytes, BINPAZER_GUID_BLOCK_TYPE_TABLE.bytes, 16) == 0, "entry0 guid");
    CHECK(entries[1].type_id == 1 && memcmp(entries[1].guid.bytes, g_mesh.bytes,16)==0, "entry1 Mesh");
    CHECK(entries[2].type_id == 2 && memcmp(entries[2].guid.bytes, g_note.bytes,16)==0, "entry2 Note");

    binpazer_block blk;
    uint8_t pbuf[64];

    /* block 1: Mesh, critical + crc */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next mesh");
    CHECK(blk.type_id == 1 && blk.known, "mesh type/known");
    CHECK(memcmp(blk.guid.bytes, g_mesh.bytes, 16) == 0, "mesh guid resolved");
    CHECK((blk.flags & BINPAZER_FLAG_CRITICAL) && (blk.flags & BINPAZER_FLAG_HAS_CRC), "mesh flags");
    CHECK(blk.payload_len == 16, "mesh len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read mesh payload (crc ok)");
    CHECK(memcmp(pbuf, mesh_payload, 16) == 0, "mesh payload bytes");

    /* block 2: Note, ancillary */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next note");
    CHECK(blk.type_id == 2 && blk.payload_len == 12, "note type/len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read note payload");
    CHECK(memcmp(pbuf, note_payload, 12) == 0, "note payload bytes");

    /* block 3: a normal repeat of type 1 */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next third");
    CHECK(blk.type_id == 1 && (blk.flags & BINPAZER_FLAG_LARGE_MODE) == 0, "third normal");
    CHECK(blk.payload_len == 24, "third len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read third payload");
    CHECK(memcmp(pbuf, big_payload, 24) == 0, "third payload bytes");

    /* end */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_END, "end of blocks");

    test_index();
    test_large();
    test_btt_capacity();

    if (failures == 0) { printf("OK: all C round-trip checks passed\n"); return 0; }
    printf("%d FAILURE(S)\n", failures);
    return 1;
}
