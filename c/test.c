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

int main(void) {
    static uint8_t storage[65536];
    membuf m = { storage, sizeof storage, 0, 0 };

    binpazer_guid writer = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_mesh = {{0x11,0x22,0x33,0x44,0x55,0x66,0x77,0x88,0x99,0xaa,0xbb,0xcc,0xdd,0xee,0xff,0x00}};
    binpazer_guid g_note = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};

    binpazer_type_def defs[] = {
        { 2, {{0}}, "Mesh" },
        { 3, {{0}}, "Note" },
    };
    defs[0].guid = g_mesh;
    defs[1].guid = g_note;

    const char *mesh_payload = "vertices-go-here";          /* 16 bytes */
    const char *note_payload = "hello, file!";              /* 12 bytes */
    const char *big_payload  = "0123456789ABCDEF01234567";  /* 24 bytes, multiple of 4 */

    /* ---- write ---- */
    binpazer_writer w;
    CHECK(binpazer_writer_begin(&w, mem_write, mem_seek, &m, &writer, "demo",
                                defs, 2) == BINPAZER_OK, "writer_begin");
    /* a critical block with a CRC */
    CHECK(binpazer_writer_block(&w, 2, BINPAZER_FLAG_CRITICAL | BINPAZER_FLAG_HAS_CRC,
                                mesh_payload, 16) == BINPAZER_OK, "write mesh");
    /* an ancillary block */
    CHECK(binpazer_writer_block(&w, 3, 0, note_payload, 12) == BINPAZER_OK, "write note");
    /* a large_mode block (length counts 4-byte words: 24 bytes -> field 6) */
    CHECK(binpazer_writer_block(&w, 2, BINPAZER_FLAG_LARGE_MODE,
                                big_payload, 24) == BINPAZER_OK, "write large");
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
    /* entry 0 is the auto BTT, then Mesh(2), Note(3) */
    CHECK(entries[0].type_id == 1, "entry0 is BTT");
    CHECK(memcmp(entries[0].guid.bytes, BINPAZER_GUID_BLOCK_TYPE_TABLE.bytes, 16) == 0, "entry0 guid");
    CHECK(entries[1].type_id == 2 && memcmp(entries[1].guid.bytes, g_mesh.bytes,16)==0, "entry1 Mesh");
    CHECK(entries[2].type_id == 3 && memcmp(entries[2].guid.bytes, g_note.bytes,16)==0, "entry2 Note");

    binpazer_block blk;
    uint8_t pbuf[64];

    /* block 1: Mesh, critical + crc */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next mesh");
    CHECK(blk.type_id == 2 && blk.known, "mesh type/known");
    CHECK(memcmp(blk.guid.bytes, g_mesh.bytes, 16) == 0, "mesh guid resolved");
    CHECK((blk.flags & BINPAZER_FLAG_CRITICAL) && (blk.flags & BINPAZER_FLAG_HAS_CRC), "mesh flags");
    CHECK(blk.payload_len == 16, "mesh len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read mesh payload (crc ok)");
    CHECK(memcmp(pbuf, mesh_payload, 16) == 0, "mesh payload bytes");

    /* block 2: Note, ancillary */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next note");
    CHECK(blk.type_id == 3 && blk.payload_len == 12, "note type/len");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read note payload");
    CHECK(memcmp(pbuf, note_payload, 12) == 0, "note payload bytes");

    /* block 3: large_mode */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_OK, "next large");
    CHECK((blk.flags & BINPAZER_FLAG_LARGE_MODE) != 0, "large flag");
    CHECK(blk.payload_len == 24, "large len decoded (6 words * 4)");
    CHECK(binpazer_reader_read_payload(&r, &blk, pbuf, sizeof pbuf) == BINPAZER_OK, "read large payload");
    CHECK(memcmp(pbuf, big_payload, 24) == 0, "large payload bytes");

    /* end */
    CHECK(binpazer_reader_next(&r, entries, nentries, &blk) == BINPAZER_END, "end of blocks");

    if (failures == 0) { printf("OK: all C round-trip checks passed\n"); return 0; }
    printf("%d FAILURE(S)\n", failures);
    return 1;
}
