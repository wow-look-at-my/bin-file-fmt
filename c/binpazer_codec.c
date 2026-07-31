/* binpazer codec layer. See binpazer_codec.h and SPEC.md "Compression". */
#include "binpazer_codec.h"

#include <limits.h>
#include <string.h>

#include <lz4frame.h>
#include <zlib.h>
#include <zstd.h>

/* Every buffer is caller-owned, so a length that cannot be expressed as a
 * size_t on this platform is a capacity error rather than a truncation. */
static int fits_size(uint64_t n) { return n <= (uint64_t)(size_t)-1; }

/* ---- stored (registry id 1): the identity codec ---- */

static binpazer_status stored_compress(void *u, const void *src, uint64_t n,
                                       void *dst, uint64_t *dst_len) {
    (void)u;
    if (*dst_len < n) return BINPAZER_ERR_CAPACITY;
    if (n) memcpy(dst, src, (size_t)n);
    *dst_len = n;
    return BINPAZER_OK;
}

static binpazer_status stored_decompress(void *u, const void *src, uint64_t n,
                                         void *dst, uint64_t dst_len) {
    (void)u;
    if (n != dst_len) return BINPAZER_ERR_FORMAT;
    if (n) memcpy(dst, src, (size_t)n);
    return BINPAZER_OK;
}

static uint64_t stored_bound(void *u, uint64_t n) { (void)u; return n; }

/* ---- deflate (registry id 2): raw DEFLATE, RFC 1951 ---- */

/* zlib's avail_in/avail_out are uInt, so a payload larger than that is fed in
 * slices; the stream itself is unaffected. */
static uInt slice32(uint64_t n) { return n > (uint64_t)UINT_MAX ? UINT_MAX : (uInt)n; }

static binpazer_status deflate_compress(void *u, const void *src, uint64_t src_len,
                                        void *dst, uint64_t *dst_len) {
    (void)u;
    z_stream zs;
    memset(&zs, 0, sizeof zs);
    /* A negative windowBits selects RAW deflate -- no zlib or gzip wrapper,
     * which is what RFC 1951 (and every other implementation) means by
     * "deflate" here. */
    if (deflateInit2(&zs, Z_DEFAULT_COMPRESSION, Z_DEFLATED, -MAX_WBITS, 8,
                     Z_DEFAULT_STRATEGY) != Z_OK)
        return BINPAZER_ERR_FORMAT;

    const uint8_t *in = (const uint8_t *)src;
    uint8_t *out = (uint8_t *)dst;
    uint64_t in_left = src_len, out_left = *dst_len, written = 0;

    for (;;) {
        if (zs.avail_in == 0 && in_left) {
            zs.next_in  = (Bytef *)(uintptr_t)in;
            zs.avail_in = slice32(in_left);
            in += zs.avail_in;
            in_left -= zs.avail_in;
        }
        zs.next_out  = out;
        zs.avail_out = slice32(out_left);
        if (zs.avail_out == 0) { deflateEnd(&zs); return BINPAZER_ERR_CAPACITY; }

        uInt before = zs.avail_out;
        int ret = deflate(&zs, (in_left || zs.avail_in) ? Z_NO_FLUSH : Z_FINISH);
        uInt made = before - zs.avail_out;
        out += made; out_left -= made; written += made;

        if (ret == Z_STREAM_END) break;
        if (ret != Z_OK) { deflateEnd(&zs); return BINPAZER_ERR_FORMAT; }
    }
    deflateEnd(&zs);
    *dst_len = written;
    return BINPAZER_OK;
}

static binpazer_status deflate_decompress(void *u, const void *src, uint64_t src_len,
                                          void *dst, uint64_t dst_len) {
    (void)u;
    z_stream zs;
    memset(&zs, 0, sizeof zs);
    if (inflateInit2(&zs, -MAX_WBITS) != Z_OK) return BINPAZER_ERR_FORMAT;

    const uint8_t *in = (const uint8_t *)src;
    uint8_t *out = (uint8_t *)dst;
    uint64_t in_left = src_len, out_left = dst_len, written = 0;
    int ret = Z_OK;

    for (;;) {
        if (zs.avail_in == 0 && in_left) {
            zs.next_in  = (Bytef *)(uintptr_t)in;
            zs.avail_in = slice32(in_left);
            in += zs.avail_in;
            in_left -= zs.avail_in;
        }
        zs.next_out  = out;
        zs.avail_out = slice32(out_left);

        uInt before = zs.avail_out;
        ret = inflate(&zs, Z_NO_FLUSH);
        uInt made = before - zs.avail_out;
        out += made; out_left -= made; written += made;

        if (ret == Z_STREAM_END) break;
        if (ret != Z_OK) { inflateEnd(&zs); return BINPAZER_ERR_FORMAT; }
        /* No progress possible: either the stream wants more input than exists
         * or it would outrun the declared length. Both are corruption. */
        if (made == 0 && zs.avail_in == 0 && in_left == 0) { inflateEnd(&zs); return BINPAZER_ERR_FORMAT; }
        if (out_left == 0 && ret != Z_STREAM_END) { inflateEnd(&zs); return BINPAZER_ERR_FORMAT; }
    }
    int leftover = (zs.avail_in != 0 || in_left != 0);
    inflateEnd(&zs);
    if (written != dst_len || leftover) return BINPAZER_ERR_FORMAT;
    return BINPAZER_OK;
}

static uint64_t deflate_bound(void *u, uint64_t n) {
    (void)u;
    /* compressBound is defined for uLong inputs; for anything larger the
     * per-slice worst case still bounds the whole stream, since deflate's
     * expansion is proportional. */
    uint64_t slices = n / (uint64_t)UINT_MAX + 1;
    return slices * (uint64_t)compressBound(slice32(n)) + 64;
}

/* ---- zstd (registry id 3): RFC 8878 ---- */

static binpazer_status zstd_compress(void *u, const void *src, uint64_t src_len,
                                     void *dst, uint64_t *dst_len) {
    (void)u;
    if (!fits_size(src_len) || !fits_size(*dst_len)) return BINPAZER_ERR_CAPACITY;
    size_t got = ZSTD_compress(dst, (size_t)*dst_len, src, (size_t)src_len,
                               ZSTD_CLEVEL_DEFAULT);
    /* Distinguished without zstd_errors.h: the one failure a caller can act on
     * is a destination below the codec's own bound. */
    if (ZSTD_isError(got))
        return *dst_len < ZSTD_compressBound((size_t)src_len)
                   ? BINPAZER_ERR_CAPACITY : BINPAZER_ERR_FORMAT;
    *dst_len = got;
    return BINPAZER_OK;
}

static binpazer_status zstd_decompress(void *u, const void *src, uint64_t src_len,
                                       void *dst, uint64_t dst_len) {
    (void)u;
    if (!fits_size(src_len) || !fits_size(dst_len)) return BINPAZER_ERR_CAPACITY;
    size_t got = ZSTD_decompress(dst, (size_t)dst_len, src, (size_t)src_len);
    if (ZSTD_isError(got) || got != dst_len) return BINPAZER_ERR_FORMAT;
    return BINPAZER_OK;
}

static uint64_t zstd_bound(void *u, uint64_t n) {
    (void)u;
    if (!fits_size(n)) return 0;
    return ZSTD_compressBound((size_t)n);
}

/* ---- lz4 (registry id 4): LZ4 frame format ---- */

static binpazer_status lz4_compress(void *u, const void *src, uint64_t src_len,
                                    void *dst, uint64_t *dst_len) {
    (void)u;
    if (!fits_size(src_len) || !fits_size(*dst_len)) return BINPAZER_ERR_CAPACITY;
    size_t got = LZ4F_compressFrame(dst, (size_t)*dst_len, src, (size_t)src_len, NULL);
    if (LZ4F_isError(got))
        return *dst_len < LZ4F_compressFrameBound((size_t)src_len, NULL)
                   ? BINPAZER_ERR_CAPACITY : BINPAZER_ERR_FORMAT;
    *dst_len = got;
    return BINPAZER_OK;
}

static binpazer_status lz4_decompress(void *u, const void *src, uint64_t src_len,
                                      void *dst, uint64_t dst_len) {
    (void)u;
    if (!fits_size(src_len) || !fits_size(dst_len)) return BINPAZER_ERR_CAPACITY;

    LZ4F_dctx *dctx = NULL;
    if (LZ4F_isError(LZ4F_createDecompressionContext(&dctx, LZ4F_VERSION)))
        return BINPAZER_ERR_FORMAT;

    const uint8_t *in = (const uint8_t *)src;
    uint8_t *out = (uint8_t *)dst;
    size_t in_left = (size_t)src_len, out_left = (size_t)dst_len;
    binpazer_status st = BINPAZER_OK;

    for (;;) {
        size_t in_n = in_left, out_n = out_left;
        size_t hint = LZ4F_decompress(dctx, out, &out_n, in, &in_n, NULL);
        if (LZ4F_isError(hint)) { st = BINPAZER_ERR_FORMAT; break; }
        in += in_n; in_left -= in_n;
        out += out_n; out_left -= out_n;
        if (hint == 0) break;                       /* frame complete */
        /* Neither side moved and the frame wants more: truncated input, or a
         * frame that decodes to more than the envelope declares. */
        if (in_n == 0 && out_n == 0) { st = BINPAZER_ERR_FORMAT; break; }
    }
    LZ4F_freeDecompressionContext(dctx);
    if (st != BINPAZER_OK) return st;
    if (out_left != 0 || in_left != 0) return BINPAZER_ERR_FORMAT;
    return BINPAZER_OK;
}

static uint64_t lz4_bound(void *u, uint64_t n) {
    (void)u;
    if (!fits_size(n)) return 0;
    return LZ4F_compressFrameBound((size_t)n, NULL);
}

/* ---- registry ---- */

static const binpazer_codec codec_stored  = { stored_compress,  stored_decompress,  stored_bound,  NULL };
static const binpazer_codec codec_deflate = { deflate_compress, deflate_decompress, deflate_bound, NULL };
static const binpazer_codec codec_zstd    = { zstd_compress,    zstd_decompress,    zstd_bound,    NULL };
static const binpazer_codec codec_lz4     = { lz4_compress,     lz4_decompress,     lz4_bound,     NULL };

typedef struct { uint16_t id; int used; binpazer_codec c; } id_slot;
typedef struct { binpazer_guid g; int used; binpazer_codec c; } guid_slot;

static id_slot   id_slots[BINPAZER_CODEC_MAX_REGISTERED];
static guid_slot guid_slots[BINPAZER_CODEC_MAX_REGISTERED];

binpazer_status binpazer_codec_register(uint16_t codec_id, const binpazer_codec *c) {
    if (codec_id == BINPAZER_CODEC_INVALID || codec_id == BINPAZER_CODEC_BY_GUID)
        return BINPAZER_ERR_RANGE;
    if (!c || !c->compress || !c->decompress || !c->bound) return BINPAZER_ERR_RANGE;
    for (size_t i = 0; i < BINPAZER_CODEC_MAX_REGISTERED; i++) {
        if (id_slots[i].used && id_slots[i].id == codec_id) { id_slots[i].c = *c; return BINPAZER_OK; }
    }
    for (size_t i = 0; i < BINPAZER_CODEC_MAX_REGISTERED; i++) {
        if (!id_slots[i].used) { id_slots[i].used = 1; id_slots[i].id = codec_id; id_slots[i].c = *c; return BINPAZER_OK; }
    }
    return BINPAZER_ERR_CAPACITY;
}

binpazer_status binpazer_codec_register_guid(const binpazer_guid *g, const binpazer_codec *c) {
    if (!g) return BINPAZER_ERR_RANGE;
    if (!c || !c->compress || !c->decompress || !c->bound) return BINPAZER_ERR_RANGE;
    for (size_t i = 0; i < BINPAZER_CODEC_MAX_REGISTERED; i++) {
        if (guid_slots[i].used && memcmp(guid_slots[i].g.bytes, g->bytes, 16) == 0) {
            guid_slots[i].c = *c; return BINPAZER_OK;
        }
    }
    for (size_t i = 0; i < BINPAZER_CODEC_MAX_REGISTERED; i++) {
        if (!guid_slots[i].used) {
            guid_slots[i].used = 1; guid_slots[i].g = *g; guid_slots[i].c = *c; return BINPAZER_OK;
        }
    }
    return BINPAZER_ERR_CAPACITY;
}

const binpazer_codec *binpazer_codec_lookup(uint16_t codec_id, const binpazer_guid *g) {
    if (codec_id == BINPAZER_CODEC_BY_GUID) {
        if (!g) return NULL;
        for (size_t i = 0; i < BINPAZER_CODEC_MAX_REGISTERED; i++)
            if (guid_slots[i].used && memcmp(guid_slots[i].g.bytes, g->bytes, 16) == 0)
                return &guid_slots[i].c;
        return NULL;
    }
    /* A registration wins over the built-in on the same id, so a caller can
     * swap in a differently-tuned encoder. */
    for (size_t i = 0; i < BINPAZER_CODEC_MAX_REGISTERED; i++)
        if (id_slots[i].used && id_slots[i].id == codec_id) return &id_slots[i].c;
    switch (codec_id) {
        case BINPAZER_CODEC_STORED:  return &codec_stored;
        case BINPAZER_CODEC_DEFLATE: return &codec_deflate;
        case BINPAZER_CODEC_ZSTD:    return &codec_zstd;
        case BINPAZER_CODEC_LZ4:     return &codec_lz4;
        default: return NULL;
    }
}

uint64_t binpazer_compress_bound(uint16_t codec_id, const binpazer_guid *g, uint64_t src_len) {
    const binpazer_codec *c = binpazer_codec_lookup(codec_id, g);
    return c ? c->bound(c->user, src_len) : 0;
}

binpazer_status binpazer_compress(uint16_t codec_id, const binpazer_guid *g,
                                  const void *src, uint64_t src_len,
                                  void *dst, uint64_t *dst_len) {
    if (!dst_len) return BINPAZER_ERR_RANGE;
    const binpazer_codec *c = binpazer_codec_lookup(codec_id, g);
    if (!c) return BINPAZER_ERR_RANGE;
    if (src_len && !src) return BINPAZER_ERR_RANGE;
    return c->compress(c->user, src, src_len, dst, dst_len);
}

binpazer_status binpazer_decompress(const binpazer_compression *c,
                                    const void *stored, void *dst, uint64_t dst_cap) {
    if (!c) return BINPAZER_ERR_RANGE;
    if (dst_cap < c->uncompressed_length) return BINPAZER_ERR_CAPACITY;
    const binpazer_codec *impl = binpazer_codec_lookup(c->codec_id, &c->codec_guid);
    if (!impl) return BINPAZER_ERR_RANGE;
    if (c->stored_length && !stored) return BINPAZER_ERR_RANGE;
    return impl->decompress(impl->user, stored, c->stored_length, dst, c->uncompressed_length);
}

/* ---- writer/reader convenience ---- */

binpazer_status binpazer_writer_block_compress(
    binpazer_writer *w, uint16_t type_id, uint16_t flags,
    uint16_t codec_id, const binpazer_guid *codec_guid,
    const void *payload, uint64_t payload_len,
    void *scratch, uint64_t scratch_cap)
{
    if (!w) return BINPAZER_ERR_RANGE;
    if (w->err) return (binpazer_status)w->err;
    uint64_t stored_len = scratch_cap;
    binpazer_status st = binpazer_compress(codec_id, codec_guid, payload, payload_len,
                                           scratch, &stored_len);
    if (st != BINPAZER_OK) return (binpazer_status)(w->err = st);
    return binpazer_writer_block_compressed(w, type_id, flags, codec_id, codec_guid,
                                            scratch, stored_len, payload_len);
}

binpazer_status binpazer_reader_read_payload_decompressed(
    binpazer_reader *r, const binpazer_block *blk,
    void *scratch, uint64_t scratch_cap,
    void *dst, uint64_t dst_cap, uint64_t *dst_len)
{
    if (!r || !blk || !dst_len) return BINPAZER_ERR_RANGE;
    if ((blk->flags & BINPAZER_FLAG_COMPRESSED) == 0) {
        if (dst_cap < blk->payload_len) return BINPAZER_ERR_CAPACITY;
        binpazer_status st = binpazer_reader_read_payload(r, blk, dst, (size_t)dst_cap);
        if (st != BINPAZER_OK) return st;
        *dst_len = blk->payload_len;
        return BINPAZER_OK;
    }
    if (scratch_cap < blk->payload_len || !fits_size(blk->payload_len))
        return BINPAZER_ERR_CAPACITY;

    binpazer_status st = binpazer_reader_read_payload(r, blk, scratch, (size_t)scratch_cap);
    if (st != BINPAZER_OK) return st;

    binpazer_compression c;
    st = binpazer_compression_parse(scratch, blk->payload_len, &c);
    if (st != BINPAZER_OK) return st;
    st = binpazer_decompress(&c, (const uint8_t *)scratch + c.header_size, dst, dst_cap);
    if (st != BINPAZER_OK) return st;
    *dst_len = c.uncompressed_length;
    return BINPAZER_OK;
}
