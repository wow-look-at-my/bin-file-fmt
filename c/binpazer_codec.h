/*
 * binpazer codecs -- every codec SPEC.md's registry names, for C.
 *
 * A separate translation unit from binpazer.c on purpose: the core stays
 * dependency-free and allocation-free, while this layer links zlib, libzstd
 * and liblz4 so a C program can read and write every compressed file any
 * binpazer implementation can produce. Link binpazer_codec.c to get codecs;
 * link binpazer.c alone and the core is unchanged.
 *
 * The API is buffer-to-buffer -- the caller owns every buffer, so this layer
 * allocates nothing of its own (the codec libraries manage their internal
 * working memory). Size a compression destination with
 * binpazer_compress_bound; a decompression destination is the envelope's
 * uncompressed_length, which is authoritative.
 */
#ifndef BINPAZER_CODEC_H
#define BINPAZER_CODEC_H

#include "binpazer.h"

#ifdef __cplusplus
extern "C" {
#endif

/* A codec implementation. `compress` takes *dst_len as the destination
 * capacity and sets it to the bytes written; `decompress` must produce exactly
 * dst_len bytes; `bound` is the worst-case compressed size of src_len bytes.
 * `user` is passed back to each. */
typedef struct {
    binpazer_status (*compress)(void *user, const void *src, uint64_t src_len,
                                void *dst, uint64_t *dst_len);
    binpazer_status (*decompress)(void *user, const void *src, uint64_t src_len,
                                  void *dst, uint64_t dst_len);
    uint64_t        (*bound)(void *user, uint64_t src_len);
    void            *user;
} binpazer_codec;

/* Registration slots for codecs beyond the built-ins. Every registry id is
 * already built in, so these are for a codec the registry does not name (the
 * GUID escape, or a later revision's id ahead of an upgrade). */
#define BINPAZER_CODEC_MAX_REGISTERED 8

/* Registers a codec for a registry id, replacing any codec on that id
 * (including a built-in). Refuses id 0 and BY_GUID. */
binpazer_status binpazer_codec_register(uint16_t codec_id, const binpazer_codec *c);

/* Registers a codec addressed by GUID (envelope codec id BY_GUID). */
binpazer_status binpazer_codec_register_guid(const binpazer_guid *g, const binpazer_codec *c);

/* Resolves an envelope's codec, or NULL when none is available. `g` is used
 * only when codec_id is BINPAZER_CODEC_BY_GUID. */
const binpazer_codec *binpazer_codec_lookup(uint16_t codec_id, const binpazer_guid *g);

/* Worst-case compressed size of src_len bytes under a codec; 0 when the codec
 * is unavailable. */
uint64_t binpazer_compress_bound(uint16_t codec_id, const binpazer_guid *g, uint64_t src_len);

/* Compresses src into dst. *dst_len is the capacity on entry and the encoded
 * length on return; BINPAZER_ERR_CAPACITY means dst was too small. */
binpazer_status binpazer_compress(uint16_t codec_id, const binpazer_guid *g,
                                  const void *src, uint64_t src_len,
                                  void *dst, uint64_t *dst_len);

/* Decompresses a parsed envelope's codec bytes. `stored` points at the codec
 * bytes (payload + header_size), and dst must hold exactly
 * c->uncompressed_length bytes -- a decode of any other length is corruption
 * (BINPAZER_ERR_FORMAT), which is what bounds a decompression bomb. */
binpazer_status binpazer_decompress(const binpazer_compression *c,
                                    const void *stored, void *dst, uint64_t dst_cap);

/* ---- Writer/reader convenience (parity with the Go library) ---- */

/* Writes one block from its LOGICAL payload, compressing with `codec_id` and
 * wrapping the result in a Compression Envelope -- the codec counterpart of
 * binpazer_writer_block_compressed, which takes bytes the caller encoded.
 * `scratch`/`scratch_cap` receive the encoded bytes; size with
 * binpazer_compress_bound. Needs a seek callback, like every compressed
 * block. */
binpazer_status binpazer_writer_block_compress(
    binpazer_writer *w, uint16_t type_id, uint16_t flags,
    uint16_t codec_id, const binpazer_guid *codec_guid,
    const void *payload, uint64_t payload_len,
    void *scratch, uint64_t scratch_cap);

/* Reads the current block's payload, decompressing it when the block carries a
 * Compression Envelope, and advances past it. `scratch` holds the raw stored
 * payload (blk->payload_len bytes) and `dst` receives the logical bytes;
 * *dst_len reports how many. An uncompressed block is read straight into dst
 * and scratch goes unused (it may be NULL). */
binpazer_status binpazer_reader_read_payload_decompressed(
    binpazer_reader *r, const binpazer_block *blk,
    void *scratch, uint64_t scratch_cap,
    void *dst, uint64_t dst_cap, uint64_t *dst_len);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* BINPAZER_CODEC_H */
