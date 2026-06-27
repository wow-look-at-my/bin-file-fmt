/* Generates the canonical cross-implementation sample file using the C library
 * (with FILE*-backed callbacks). Used as a fixture for the Go/C++ tests.
 *
 *   gen <output-path>
 *
 * This is a tool, not part of the library -- the library itself does no file IO.
 * The exact same inputs are reproduced by each language's test, so a correct
 * implementation writes byte-identical output. */
#include "binpazer.h"
#include <stdio.h>

static size_t file_write(void *ctx, const void *b, size_t n) { return fwrite(b, 1, n, (FILE *)ctx); }
static int    file_seek(void *ctx, uint64_t off) { return fseek((FILE *)ctx, (long)off, SEEK_SET); }

int main(int argc, char **argv) {
    if (argc != 2) { fprintf(stderr, "usage: %s <out>\n", argv[0]); return 2; }
    FILE *f = fopen(argv[1], "wb");
    if (!f) { perror("fopen"); return 1; }

    binpazer_guid writer = {{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer_guid g_mesh = {{0x11,0x22,0x33,0x44,0x55,0x66,0x77,0x88,0x99,0xaa,0xbb,0xcc,0xdd,0xee,0xff,0x00}};
    binpazer_guid g_note = {{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
    binpazer_type_def defs[2] = { {2, {{0}}, "Mesh"}, {3, {{0}}, "Note"} };
    defs[0].guid = g_mesh; defs[1].guid = g_note;

    binpazer_writer w;
    binpazer_writer_begin(&w, file_write, file_seek, f, &writer, "demo", defs, 2);
    binpazer_writer_block(&w, 2, BINPAZER_FLAG_CRITICAL | BINPAZER_FLAG_HAS_CRC, "vertices-go-here", 16);
    binpazer_writer_block(&w, 3, 0, "hello, file!", 12);
    binpazer_writer_block(&w, 2, BINPAZER_FLAG_LARGE_MODE, "0123456789ABCDEF01234567", 24);
    binpazer_writer_end(&w);

    fclose(f);
    if (w.err != BINPAZER_OK) { fprintf(stderr, "write error %d\n", w.err); return 1; }
    return 0;
}
