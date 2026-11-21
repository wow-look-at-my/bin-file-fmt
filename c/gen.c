/* Generates the canonical cross-implementation sample file using the C library
 * (with FILE*-backed callbacks). Used as a fixture for the Go/C++ tests. */
#include "binpazer.h"
#include <stdio.h>

static size_t file_write(void *ctx, const void *b, size_t n) { return fwrite(b, 1, n, (FILE *)ctx); }
static int    file_seek(void *ctx, uint64_t off) { return fseek((FILE *)ctx, (long)off, SEEK_SET); }

/* The compressed fixture uses codec `stored` (identity) deliberately. It is the
 * one codec every implementation can produce with no dependency. All of them
 * write these exact bytes and what gets compared is the envelope itself. */
static int gen_compressed(const char *path) {
    FILE *f = fopen(path, "wb");
    if (!f) { perror("fopen"); return 1; }

    binpazer_guid writer  = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_note  = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
    binpazer_guid g_codec = {{0xc0,0xde,0xc0,0xde,0,0,0,0,0,0,0,0,0,0,0,7}};
    binpazer_type_def defs[1] = { {2, {{0}}, "Note"} };
    defs[0].guid = g_note;

    binpazer_writer w;
    binpazer_writer_begin(&w, file_write, file_seek, f, &writer, "demo", defs, 1);
    binpazer_writer_block(&w, 2, 0, "plain block", 11);
    binpazer_writer_block_compressed(&w, 2, BINPAZER_FLAG_HAS_CRC,
                                     BINPAZER_CODEC_STORED, NULL,
                                     "compressed, sort of", 19, 19);
    binpazer_writer_block_compressed(&w, 2, 0, BINPAZER_CODEC_BY_GUID, &g_codec,
                                     "guid-codec payload", 18, 18);
    binpazer_writer_end(&w);

    fclose(f);
    if (w.err != BINPAZER_OK) { fprintf(stderr, "compressed write error %d\n", w.err); return 1; }
    return 0;
}

/* The appendable fixture: sealed blocks, then the End block and the back-patched
 * file_length of a seekable close. */
static int gen_appendable(const char *path) {
    FILE *f = fopen(path, "wb");
    if (!f) { perror("fopen"); return 1; }

    binpazer_guid writer = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_note = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
    binpazer_type_def defs[1] = { {2, {{0}}, "Note"} };
    defs[0].guid = g_note;

    binpazer_writer w;
    binpazer_writer_begin_append(&w, file_write, file_seek, f, &writer, "demo", defs, 1);
    binpazer_writer_block(&w, 2, 0, "first record", 12);
    binpazer_writer_block(&w, 2, 0, "second record, a bit longer", 27);
    binpazer_writer_block(&w, 2, 0, "", 0);
    binpazer_writer_end(&w);

    fclose(f);
    if (w.err != BINPAZER_OK) { fprintf(stderr, "appendable write error %d\n", w.err); return 1; }
    return 0;
}

static size_t file_read(void *ctx, void *b, size_t n) { return fread(b, 1, n, (FILE *)ctx); }

/* The resume fixture: the appendable sample left open, a torn tail after it,
 * then a resume that seals the tail, appends a record. It ends the file. It
 * pins the Padding bytes of a resume across implementations. */
static int gen_resumed(const char *path) {
    FILE *f = fopen(path, "wb+");
    if (!f) { perror("fopen"); return 1; }

    binpazer_guid writer = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_note = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
    binpazer_type_def defs[1] = { {2, {{0}}, "Note"} };
    defs[0].guid = g_note;

    binpazer_writer w;
    binpazer_writer_begin_append(&w, file_write, file_seek, f, &writer, "demo", defs, 1);
    binpazer_writer_block(&w, 2, 0, "first record", 12);
    binpazer_writer_block(&w, 2, 0, "second record, a bit longer", 27);
    binpazer_writer_block(&w, 2, 0, "", 0);
    if (w.err != BINPAZER_OK) { fprintf(stderr, "resumed write error %d\n", w.err); fclose(f); return 1; }

    /* The torn tail: a sealed header that claims a long payload, and the few payload bytes that landed. */
    static const uint8_t torn[21] = {
        2, 0, BINPAZER_FLAG_HAS_CRC, 0, 0, 0, 0x10, 0,
        0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB,
    };
    fwrite(torn, 1, sizeof torn, f);

    uint8_t scratch[256];
    fseek(f, 0, SEEK_SET);
    binpazer_writer_resume(&w, file_read, file_write, file_seek, f, NULL, 0, NULL, scratch, sizeof scratch);
    binpazer_writer_block(&w, 2, 0, "after the crash", 15);
    binpazer_writer_end(&w);

    fclose(f);
    if (w.err != BINPAZER_OK) { fprintf(stderr, "resume error %d\n", w.err); return 1; }
    return 0;
}

int main(int argc, char **argv) {
    if (argc < 2 || argc > 5) {
        fprintf(stderr, "usage: %s <out> [compressed-out [appendable-out [resumed-out]]]\n", argv[0]);
        return 2;
    }
    FILE *f = fopen(argv[1], "wb");
    if (!f) { perror("fopen"); return 1; }

    binpazer_guid writer = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_mesh = {{0x11,0x22,0x33,0x44,0x55,0x66,0x77,0x88,0x99,0xaa,0xbb,0xcc,0xdd,0xee,0xff,0x00}};
    binpazer_guid g_note = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
    binpazer_type_def defs[2] = { {1, {{0}}, "Mesh"}, {2, {{0}}, "Note"} };
    defs[0].guid = g_mesh; defs[1].guid = g_note;

    binpazer_writer w;
    binpazer_writer_begin(&w, file_write, file_seek, f, &writer, "demo", defs, 2);
    binpazer_writer_block(&w, 1, BINPAZER_FLAG_CRITICAL | BINPAZER_FLAG_HAS_CRC, "vertices-go-here", 16);
    binpazer_writer_block(&w, 2, 0, "hello, file!", 12);
    binpazer_writer_block(&w, 1, 0, "0123456789ABCDEF01234567", 24);
    binpazer_writer_end(&w);

    fclose(f);
    if (w.err != BINPAZER_OK) { fprintf(stderr, "write error %d\n", w.err); return 1; }

    if (argc >= 3 && gen_compressed(argv[2]) != 0) return 1;
    if (argc >= 4 && gen_appendable(argv[3]) != 0) return 1;
    if (argc == 5) return gen_resumed(argv[4]);
    return 0;
}
