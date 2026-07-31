/* Cross-implementation interop harness.
 *
 * Byte-identical output is only guaranteed for uncompressed files and the
 * `stored` codec -- a real codec's encoder output is implementation-defined,
 * so zlib and klauspost/compress will not agree bit for bit and never had to.
 * What DOES have to hold is that every implementation can DECODE what every
 * other one writes. This tool is the C half of that check: it writes one file
 * per registry codec for the Go CLI to read, and verifies files the Go CLI
 * wrote.
 *
 *   interop payload <file>   write the canonical payload (both sides compare
 *                            against this, so there is one definition of it)
 *   interop write <dir>      write <dir>/c-<codec>.binpazer for each codec
 *   interop read <file>      decode block type 1 and check it against the
 *                            canonical payload; nonzero exit on mismatch
 */
#include "binpazer_codec.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define PAYLOAD_LEN 8192
#define TYPE_PAYLOAD 1

/* Compressible on purpose, and generated identically here and in the CI step
 * that drives the Go side. */
static void make_payload(uint8_t *p) {
    static const char *line = "binpazer compression parity across implementations. ";
    size_t len = strlen(line);
    for (size_t i = 0; i < PAYLOAD_LEN; i++) p[i] = (uint8_t)line[i % len];
}

/* ---- file IO callbacks (the library does none itself) ---- */

static size_t f_write(void *ctx, const void *buf, size_t n) { return fwrite(buf, 1, n, (FILE *)ctx); }
static size_t f_read(void *ctx, void *buf, size_t n)        { return fread(buf, 1, n, (FILE *)ctx); }
static int    f_seek(void *ctx, uint64_t off) { return fseek((FILE *)ctx, (long)off, SEEK_SET); }

static const struct { uint16_t id; const char *name; } kCodecs[] = {
    { BINPAZER_CODEC_STORED,  "stored"  },
    { BINPAZER_CODEC_DEFLATE, "deflate" },
    { BINPAZER_CODEC_ZSTD,    "zstd"    },
    { BINPAZER_CODEC_LZ4,     "lz4"     },
};
static const size_t kNumCodecs = sizeof kCodecs / sizeof kCodecs[0];

static int write_one(const char *path, uint16_t codec_id, const uint8_t *payload) {
    FILE *f = fopen(path, "wb+");
    if (!f) { fprintf(stderr, "interop: cannot create %s\n", path); return 1; }

    binpazer_guid wg = {{0x01}};
    binpazer_type_def defs[1] = { {TYPE_PAYLOAD, {{0x11}}, "Payload"} };

    uint64_t cap = binpazer_compress_bound(codec_id, NULL, PAYLOAD_LEN);
    uint8_t *scratch = (uint8_t *)malloc((size_t)cap);
    if (!scratch) { fclose(f); return 1; }

    binpazer_writer w;
    int rc = 0;
    if (binpazer_writer_begin(&w, f_write, f_seek, f, &wg, "interop", defs, 1) != BINPAZER_OK) rc = 1;
    if (!rc && binpazer_writer_block_compress(&w, TYPE_PAYLOAD, BINPAZER_FLAG_HAS_CRC,
                                              codec_id, NULL, payload, PAYLOAD_LEN,
                                              scratch, cap) != BINPAZER_OK) rc = 1;
    if (!rc && binpazer_writer_end(&w) != BINPAZER_OK) rc = 1;

    free(scratch);
    fclose(f);
    if (rc) fprintf(stderr, "interop: writing %s failed\n", path);
    return rc;
}

static int cmd_write(const char *dir) {
    uint8_t payload[PAYLOAD_LEN];
    make_payload(payload);
    char path[1024];
    for (size_t i = 0; i < kNumCodecs; i++) {
        snprintf(path, sizeof path, "%s/c-%s.binpazer", dir, kCodecs[i].name);
        if (write_one(path, kCodecs[i].id, payload) != 0) return 1;
        printf("wrote %s\n", path);
    }
    return 0;
}

static int cmd_read(const char *path) {
    FILE *f = fopen(path, "rb");
    if (!f) { fprintf(stderr, "interop: cannot open %s\n", path); return 1; }

    binpazer_reader r;
    char nm[64];
    binpazer_type_entry te[16];
    size_t nte = 0;
    if (binpazer_reader_begin(&r, f_read, f_seek, f, nm, sizeof nm, te, 16, &nte) != BINPAZER_OK) {
        fprintf(stderr, "interop: %s: bad header\n", path);
        fclose(f);
        return 1;
    }

    uint8_t expect[PAYLOAD_LEN];
    make_payload(expect);

    static uint8_t scratch[PAYLOAD_LEN * 4];
    static uint8_t decoded[PAYLOAD_LEN * 2];
    binpazer_block blk;
    int rc = 1;
    while (binpazer_reader_next(&r, te, nte, &blk) == BINPAZER_OK) {
        if (blk.type_id != TYPE_PAYLOAD) {
            if (binpazer_reader_skip_payload(&r, &blk) != BINPAZER_OK) break;
            continue;
        }
        uint64_t got = 0;
        binpazer_status st = binpazer_reader_read_payload_decompressed(
            &r, &blk, scratch, sizeof scratch, decoded, sizeof decoded, &got);
        if (st != BINPAZER_OK) {
            fprintf(stderr, "interop: %s: decode failed (status %d)\n", path, (int)st);
            break;
        }
        if (got != PAYLOAD_LEN || memcmp(decoded, expect, PAYLOAD_LEN) != 0) {
            fprintf(stderr, "interop: %s: decoded %llu bytes, payload mismatch\n",
                    path, (unsigned long long)got);
            break;
        }
        printf("ok %s: %llu bytes decoded, matches the canonical payload\n",
               path, (unsigned long long)got);
        rc = 0;
        break;
    }
    fclose(f);
    if (rc) fprintf(stderr, "interop: %s: no readable type-%d block\n", path, TYPE_PAYLOAD);
    return rc;
}

static int cmd_payload(const char *path) {
    uint8_t payload[PAYLOAD_LEN];
    make_payload(payload);
    FILE *f = fopen(path, "wb");
    if (!f) { fprintf(stderr, "interop: cannot create %s\n", path); return 1; }
    size_t n = fwrite(payload, 1, sizeof payload, f);
    fclose(f);
    return n == sizeof payload ? 0 : 1;
}

int main(int argc, char **argv) {
    if (argc == 3 && strcmp(argv[1], "payload") == 0) return cmd_payload(argv[2]);
    if (argc == 3 && strcmp(argv[1], "write") == 0)   return cmd_write(argv[2]);
    if (argc == 3 && strcmp(argv[1], "read") == 0)    return cmd_read(argv[2]);
    fprintf(stderr, "usage: interop payload <file> | write <dir> | read <file>\n");
    return 2;
}
