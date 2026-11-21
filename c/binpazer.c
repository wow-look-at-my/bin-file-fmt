/* binpazer reference C implementation. See binpazer.h and SPEC.md. */
#include "binpazer.h"
#include <string.h>

const binpazer_guid BINPAZER_GUID_BLOCK_TYPE_TABLE = {
    { 0x62,0x69,0x6e,0x70, 0x61,0x7a,0x65,0x72,
      0x00,0x00,0x00,0x00, 0x00,0x00,0x00,0x01 }
};
const binpazer_guid BINPAZER_GUID_END = {
    { 0x62,0x69,0x6e,0x70, 0x61,0x7a,0x65,0x72,
      0x00,0x00,0x00,0x00, 0x00,0x00,0x00,0x06 }
};

/* The name an appendable file gives the End type in its Block Type Table. */
static const char END_TYPE_NAME[] = "End";

/* Largest extent of one Padding block that binpazer_writer_resume writes. */
#define MAX_PAD_EXTENT ((uint64_t)1 << 20)

/* ---- little-endian helpers (host-endianness independent) ---- */

static void put_u16(uint8_t *p, uint16_t v) { p[0]=(uint8_t)v; p[1]=(uint8_t)(v>>8); }
static void put_u32(uint8_t *p, uint32_t v) { for (int i=0;i<4;i++) p[i]=(uint8_t)(v>>(8*i)); }
static void put_u64(uint8_t *p, uint64_t v) { for (int i=0;i<8;i++) p[i]=(uint8_t)(v>>(8*i)); }
static uint16_t get_u16(const uint8_t *p) { return (uint16_t)(p[0] | (p[1]<<8)); }
static uint32_t get_u32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1]<<8) | ((uint32_t)p[2]<<16) | ((uint32_t)p[3]<<24);
}
static uint64_t get_u64(const uint8_t *p) {
    uint64_t v=0; for (int i=0;i<8;i++) v |= (uint64_t)p[i] << (8*i); return v;
}
static uint64_t align8(uint64_t x) { return (x + 7u) & ~(uint64_t)7u; }

/* ---- CRC-32C (Castagnoli, reflected) ---- */

uint32_t binpazer_crc32c(uint32_t crc, const void *data, size_t n) {
    const uint8_t *p = (const uint8_t *)data;
    crc = ~crc;
    for (size_t i = 0; i < n; i++) {
        crc ^= p[i];
        for (int k = 0; k < 8; k++) {
            uint32_t mask = (uint32_t)0 - (crc & 1u);
            crc = (crc >> 1) ^ (0x82F63B78u & mask);
        }
    }
    return ~crc;
}

/* ---- writer ---- */

static binpazer_status w_raw(binpazer_writer *w, const void *buf, size_t n) {
    if (w->err) return (binpazer_status)w->err;
    if (w->write(w->ctx, buf, n) != n) { w->err = BINPAZER_ERR_IO; }
    else w->offset += n;
    return (binpazer_status)w->err;
}
static binpazer_status w_pad(binpazer_writer *w, uint64_t n) {
    static const uint8_t zero[8] = {0};
    while (n) { size_t c = n < 8 ? (size_t)n : 8; if (w_raw(w, zero, c)) return (binpazer_status)w->err; n -= c; }
    return BINPAZER_OK;
}

/* on-disk size of one BTT entry given its name length */
static uint64_t btt_entry_size(size_t name_len) {
    return align8((uint64_t)20 + 4 + name_len + 1); /* */
}

static binpazer_status w_btt_entry(binpazer_writer *w, uint16_t type_id,
                                   const binpazer_guid *guid, const char *name) {
    size_t name_len = name ? strlen(name) : 0;
    uint8_t hdr[20];
    put_u16(hdr + 0, type_id);
    put_u16(hdr + 2, 0); /* entry_flags */
    memcpy(hdr + 4, guid->bytes, 16);
    if (w_raw(w, hdr, 20)) return (binpazer_status)w->err;
    uint8_t lp[4]; put_u32(lp, (uint32_t)name_len);
    if (w_raw(w, lp, 4)) return (binpazer_status)w->err;
    if (name_len && w_raw(w, name, name_len)) return (binpazer_status)w->err;
    uint8_t nul = 0; if (w_raw(w, &nul, 1)) return (binpazer_status)w->err;
    uint64_t raw = (uint64_t)25 + name_len;
    return w_pad(w, align8(raw) - raw);
}

binpazer_status binpazer_writer_begin(
    binpazer_writer *w,
    binpazer_write_fn write, binpazer_seek_fn seek, void *ctx,
    const binpazer_guid *writer_guid, const char *writer_name,
    const binpazer_type_def *defs, size_t ndefs)
{
    return binpazer_writer_begin_version(w, write, seek, ctx, writer_guid, writer_name,
                                         defs, ndefs, BINPAZER_VERSION_MINOR);
}

static binpazer_status w_begin(
    binpazer_writer *w,
    binpazer_write_fn write, binpazer_seek_fn seek, void *ctx,
    const binpazer_guid *writer_guid, const char *writer_name,
    const binpazer_type_def *defs, size_t ndefs, uint16_t version_minor, int appendable);

binpazer_status binpazer_writer_begin_version(
    binpazer_writer *w,
    binpazer_write_fn write, binpazer_seek_fn seek, void *ctx,
    const binpazer_guid *writer_guid, const char *writer_name,
    const binpazer_type_def *defs, size_t ndefs, uint16_t version_minor)
{
    return w_begin(w, write, seek, ctx, writer_guid, writer_name, defs, ndefs, version_minor, 0);
}

binpazer_status binpazer_writer_begin_append(
    binpazer_writer *w,
    binpazer_write_fn write, binpazer_seek_fn seek, void *ctx,
    const binpazer_guid *writer_guid, const char *writer_name,
    const binpazer_type_def *defs, size_t ndefs)
{
    return w_begin(w, write, seek, ctx, writer_guid, writer_name, defs, ndefs,
                   BINPAZER_VERSION_MINOR_APPENDABLE, 1);
}

static binpazer_status w_begin(
    binpazer_writer *w,
    binpazer_write_fn write, binpazer_seek_fn seek, void *ctx,
    const binpazer_guid *writer_guid, const char *writer_name,
    const binpazer_type_def *defs, size_t ndefs, uint16_t version_minor, int appendable)
{
    memset(w, 0, sizeof *w);
    w->write = write; w->seek = seek; w->ctx = ctx;
    w->version_minor = version_minor;
    w->appendable = appendable;

    size_t name_len = writer_name ? strlen(writer_name) : 0;
    uint64_t first_block = align8((uint64_t)56 + 4 + name_len + 1);

    uint8_t hdr[56];
    memset(hdr, 0, sizeof hdr);
    memcpy(hdr, BINPAZER_MAGIC, 8);
    hdr[8] = 0x0D; hdr[9] = 0x0A; hdr[10] = 0x1A; hdr[11] = 0x0A;
    put_u16(hdr + 12, BINPAZER_VERSION_MAJOR);
    put_u16(hdr + 14, version_minor);
    if (writer_guid) memcpy(hdr + 16, writer_guid->bytes, 16);
    put_u64(hdr + 32, BINPAZER_FILE_LENGTH_UNKNOWN);
    put_u64(hdr + 40, first_block);
    if (appendable) put_u32(hdr + 48, BINPAZER_HEADER_FLAG_APPENDABLE);
    if (w_raw(w, hdr, 56)) return (binpazer_status)w->err;

    uint8_t lp[4]; put_u32(lp, (uint32_t)name_len);
    if (w_raw(w, lp, 4)) return (binpazer_status)w->err;
    if (name_len && w_raw(w, writer_name, name_len)) return (binpazer_status)w->err;
    uint8_t nul = 0; if (w_raw(w, &nul, 1)) return (binpazer_status)w->err;
    if (w_pad(w, first_block - w->offset)) return (binpazer_status)w->err;

    uint64_t payload_len = 8;
    payload_len += btt_entry_size(strlen("BlockTypeTable"));
    for (size_t i = 0; i < ndefs; i++)
        payload_len += btt_entry_size(defs[i].name ? strlen(defs[i].name) : 0);
    /* The End entry lets a reader older than the End block label it. */
    if (appendable) payload_len += btt_entry_size(strlen(END_TYPE_NAME));
    if (payload_len > 0xFFFFFFFFu) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);

    uint8_t bh[8];
    put_u16(bh + 0, BINPAZER_TYPE_BLOCK_TYPE_TABLE);
    put_u16(bh + 2, BINPAZER_FLAG_CRITICAL);
    put_u32(bh + 4, (uint32_t)payload_len);
    if (w_raw(w, bh, 8)) return (binpazer_status)w->err;

    uint8_t cnt[8];
    put_u32(cnt + 0, (uint32_t)(ndefs + 1 + (appendable ? 1 : 0)));
    put_u32(cnt + 4, 0);
    if (w_raw(w, cnt, 8)) return (binpazer_status)w->err;

    if (w_btt_entry(w, BINPAZER_TYPE_BLOCK_TYPE_TABLE,
                    &BINPAZER_GUID_BLOCK_TYPE_TABLE, "BlockTypeTable"))
        return (binpazer_status)w->err;
    for (size_t i = 0; i < ndefs; i++)
        if (w_btt_entry(w, defs[i].type_id, &defs[i].guid, defs[i].name))
            return (binpazer_status)w->err;
    if (appendable && w_btt_entry(w, BINPAZER_TYPE_END, &BINPAZER_GUID_END, END_TYPE_NAME))
        return (binpazer_status)w->err;

    return w_pad(w, align8(w->offset) - w->offset);
}

binpazer_status binpazer_writer_block(
    binpazer_writer *w, uint16_t type_id, uint16_t flags,
    const void *payload, uint64_t payload_len)
{
    if (w->err) return (binpazer_status)w->err;
    if (w->ended) return BINPAZER_ERR_ENDED;
    if (w->appendable) flags |= BINPAZER_FLAG_HAS_CRC; /* the CRC trailer is the commit seal */

    uint32_t length_field;
    if (flags & BINPAZER_FLAG_LARGE_MODE) {
        if (payload_len % BINPAZER_LARGE_UNIT != 0) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
        uint64_t units = payload_len / BINPAZER_LARGE_UNIT;
        if (units > 0xFFFFFFFFu) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
        length_field = (uint32_t)units;
    } else {
        if (payload_len > 0xFFFFFFFFu) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
        length_field = (uint32_t)payload_len;
    }

    uint8_t bh[8];
    put_u16(bh + 0, type_id);
    put_u16(bh + 2, flags);
    put_u32(bh + 4, length_field);

    uint32_t crc = 0;
    if (flags & BINPAZER_FLAG_HAS_CRC) {
        crc = binpazer_crc32c(0, bh, 8);
        if (payload_len) crc = binpazer_crc32c(crc, payload, (size_t)payload_len);
    }
    if (w_raw(w, bh, 8)) return (binpazer_status)w->err;
    if (payload_len && w_raw(w, payload, (size_t)payload_len)) return (binpazer_status)w->err;
    if (flags & BINPAZER_FLAG_HAS_CRC) {
        uint8_t cb[4]; put_u32(cb, crc);
        if (w_raw(w, cb, 4)) return (binpazer_status)w->err;
    }
    return w_pad(w, align8(w->offset) - w->offset);
}

binpazer_status binpazer_writer_index(
    binpazer_writer *w, const binpazer_index_entry *entries, size_t n)
{
    if (w->err) return (binpazer_status)w->err;
    if (w->appendable) return BINPAZER_ERR_RANGE; /* an appendable file carries no Block Index */
    uint64_t payload_len = 8 + (uint64_t)n * 16;
    if (payload_len > 0xFFFFFFFFu) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);

    w->index_offset = w->offset;

    uint8_t bh[8];
    put_u16(bh + 0, BINPAZER_TYPE_BLOCK_INDEX);
    put_u16(bh + 2, 0); /* ancillary; not safe_to_copy */
    put_u32(bh + 4, (uint32_t)payload_len);
    if (w_raw(w, bh, 8)) return (binpazer_status)w->err;

    uint8_t cnt[8];
    put_u32(cnt + 0, (uint32_t)n);
    put_u32(cnt + 4, 0);
    if (w_raw(w, cnt, 8)) return (binpazer_status)w->err;

    for (size_t i = 0; i < n; i++) {
        uint8_t e[16];
        put_u16(e + 0, entries[i].type_id);
        put_u16(e + 2, 0);
        put_u32(e + 4, 0);
        put_u64(e + 8, entries[i].offset);
        if (w_raw(w, e, 16)) return (binpazer_status)w->err;
    }
    w->wrote_index = 1;
    return w_pad(w, align8(w->offset) - w->offset);
}

/* Writes a block whose payload is a Compression Envelope around already-encoded
 * codec bytes. The envelope header is emitted here; the codec bytes come from
 * the caller, which is what keeps this library codec-free. */
binpazer_status binpazer_writer_block_compressed(
    binpazer_writer *w, uint16_t type_id, uint16_t flags,
    uint16_t codec_id, const binpazer_guid *codec_guid,
    const void *stored, uint64_t stored_len, uint64_t uncompressed_len)
{
    if (w->err) return (binpazer_status)w->err;
    if (w->ended) return BINPAZER_ERR_ENDED;
    if (w->appendable) flags |= BINPAZER_FLAG_HAS_CRC; /* the CRC trailer is the commit seal */
    if (codec_id == BINPAZER_CODEC_INVALID) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
    if (codec_id == BINPAZER_CODEC_BY_GUID && !codec_guid) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
    if (!w->seek && w->version_minor < BINPAZER_VERSION_MINOR_COMPRESSION)
        return (binpazer_status)(w->err = BINPAZER_ERR_NOSEEK);

    uint32_t hdr_size = (codec_id == BINPAZER_CODEC_BY_GUID)
                            ? BINPAZER_ENVELOPE_GUID_SIZE : BINPAZER_ENVELOPE_FIXED_SIZE;
    uint64_t payload_len = (uint64_t)hdr_size + stored_len;

    uint32_t length_field;
    if (flags & BINPAZER_FLAG_LARGE_MODE) {
        if (payload_len % BINPAZER_LARGE_UNIT != 0) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
        uint64_t units = payload_len / BINPAZER_LARGE_UNIT;
        if (units > 0xFFFFFFFFu) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
        length_field = (uint32_t)units;
    } else {
        if (payload_len > 0xFFFFFFFFu) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);
        length_field = (uint32_t)payload_len;
    }
    flags |= BINPAZER_FLAG_COMPRESSED;

    uint8_t bh[8];
    put_u16(bh + 0, type_id);
    put_u16(bh + 2, flags);
    put_u32(bh + 4, length_field);

    uint8_t env[BINPAZER_ENVELOPE_GUID_SIZE];
    memset(env, 0, sizeof env);
    put_u16(env + 0, codec_id);
    put_u64(env + 8, uncompressed_len);
    if (codec_id == BINPAZER_CODEC_BY_GUID) memcpy(env + 16, codec_guid->bytes, 16);

    uint32_t crc = 0;
    if (flags & BINPAZER_FLAG_HAS_CRC) {
        crc = binpazer_crc32c(0, bh, 8);
        crc = binpazer_crc32c(crc, env, hdr_size);
        if (stored_len) crc = binpazer_crc32c(crc, stored, (size_t)stored_len);
    }
    if (w_raw(w, bh, 8)) return (binpazer_status)w->err;
    if (w_raw(w, env, hdr_size)) return (binpazer_status)w->err;
    if (stored_len && w_raw(w, stored, (size_t)stored_len)) return (binpazer_status)w->err;
    if (flags & BINPAZER_FLAG_HAS_CRC) {
        uint8_t cb[4]; put_u32(cb, crc);
        if (w_raw(w, cb, 4)) return (binpazer_status)w->err;
    }
    w->used_compression = 1;
    return w_pad(w, align8(w->offset) - w->offset);
}

/* Parses nested blocks out of a Group block's payload. Mirrors the Go
 * library's ParseGroupPayload, including its tolerance: trailing zero padding
 * ends the walk cleanly, and a bad child stops the walk with everything. This
 * happens before it intact. */
binpazer_status binpazer_group_parse(
    const void *payload, uint64_t payload_len,
    binpazer_group_child *out, size_t cap, size_t *count)
{
    if (!count || (!payload && payload_len)) return BINPAZER_ERR_FORMAT;
    const uint8_t *p = (const uint8_t *)payload;
    uint64_t off = 0;
    size_t n = 0;
    *count = 0;

    while (off < payload_len) {
        uint64_t rem = payload_len - off;
        if (rem < 8) {
            /* A bounded number of bytes left: alignment padding if zero, junk if not. */
            for (uint64_t i = 0; i < rem; i++)
                if (p[off + i] != 0) { *count = n; return BINPAZER_ERR_FORMAT; }
            break;
        }
        uint16_t type_id = get_u16(p + off);
        uint16_t flags   = get_u16(p + off + 2);
        uint32_t lf      = get_u32(p + off + 4);
        uint64_t plen    = (flags & BINPAZER_FLAG_LARGE_MODE)
                               ? (uint64_t)lf * BINPAZER_LARGE_UNIT : lf;
        uint64_t claim   = plen + ((flags & BINPAZER_FLAG_HAS_CRC) ? 4u : 0u);
        if (claim > rem - 8) { *count = n; return BINPAZER_ERR_FORMAT; }

        if (n < cap && out) {
            binpazer_group_child *c = &out[n];
            c->type_id = type_id;
            c->flags = flags;
            c->offset = off;
            c->payload = p + off + 8;
            c->payload_len = plen;
            c->crc_ok = 1;
            if (flags & BINPAZER_FLAG_HAS_CRC) {
                uint8_t bh[8];
                put_u16(bh + 0, type_id);
                put_u16(bh + 2, flags);
                put_u32(bh + 4, lf);
                uint32_t crc = binpazer_crc32c(0, bh, 8);
                if (plen) crc = binpazer_crc32c(crc, c->payload, (size_t)plen);
                c->crc_ok = (crc == get_u32(p + off + 8 + plen));
            }
        }
        n++;

        uint64_t next = align8(off + 8 + claim);
        if (next > payload_len) break; /* final padding not materialized; tolerate */
        off = next;
    }
    *count = n;
    /* Same contract as binpazer_reader_find: a count larger than the buffer is reported, not truncated silently. */
    return n > cap ? BINPAZER_ERR_CAPACITY : BINPAZER_OK;
}

binpazer_status binpazer_compression_parse(
    const void *payload, uint64_t payload_len, binpazer_compression *out)
{
    const uint8_t *p = (const uint8_t *)payload;
    if (!p || !out) return BINPAZER_ERR_FORMAT;
    if (payload_len < BINPAZER_ENVELOPE_FIXED_SIZE) return BINPAZER_ERR_FORMAT;

    memset(out, 0, sizeof *out);
    out->codec_id = get_u16(p);
    if (out->codec_id == BINPAZER_CODEC_INVALID) return BINPAZER_ERR_FORMAT;
    if (get_u16(p + 2) != 0 || get_u32(p + 4) != 0) return BINPAZER_ERR_FORMAT;
    out->uncompressed_length = get_u64(p + 8);
    out->header_size = BINPAZER_ENVELOPE_FIXED_SIZE;
    if (out->codec_id == BINPAZER_CODEC_BY_GUID) {
        if (payload_len < BINPAZER_ENVELOPE_GUID_SIZE) return BINPAZER_ERR_FORMAT;
        memcpy(out->codec_guid.bytes, p + 16, 16);
        out->header_size = BINPAZER_ENVELOPE_GUID_SIZE;
    }
    out->stored_length = payload_len - out->header_size;
    return BINPAZER_OK;
}

/* Covers the next `n` bytes of the destination with sealed Padding blocks. A
 * torn tail can hold sealed nested blocks, and a later short block must not
 * expose one of them as committed. */
static binpazer_status w_pad_over(binpazer_writer *w, uint64_t n) {
    static const uint8_t zero[4096] = {0};
    while (n) {
        uint64_t extent = align8(n);
        if (extent < 16) extent = 16;
        if (extent > MAX_PAD_EXTENT) extent = MAX_PAD_EXTENT;
        uint64_t payload_len = extent - 12; /* header and CRC fill the rest; no alignment pad is left */

        uint8_t bh[8];
        put_u16(bh + 0, BINPAZER_TYPE_PADDING);
        put_u16(bh + 2, BINPAZER_FLAG_HAS_CRC);
        put_u32(bh + 4, (uint32_t)payload_len);
        if (w_raw(w, bh, 8)) return (binpazer_status)w->err;
        uint32_t crc = binpazer_crc32c(0, bh, 8);
        for (uint64_t rem = payload_len; rem; ) {
            size_t c = rem < sizeof zero ? (size_t)rem : sizeof zero;
            if (w_raw(w, zero, c)) return (binpazer_status)w->err;
            crc = binpazer_crc32c(crc, zero, c);
            rem -= c;
        }
        uint8_t cb[4]; put_u32(cb, crc);
        if (w_raw(w, cb, 4)) return (binpazer_status)w->err;

        if (extent >= n) break;
        n -= extent;
    }
    return BINPAZER_OK;
}

binpazer_status binpazer_writer_resume(
    binpazer_writer *w,
    binpazer_read_fn read, binpazer_write_fn write, binpazer_seek_fn seek, void *ctx,
    binpazer_type_entry *entries, size_t entries_cap, size_t *entries_count,
    void *scratch, size_t scratch_cap)
{
    memset(w, 0, sizeof *w);
    if (!read || !write || !seek) return (binpazer_status)(w->err = BINPAZER_ERR_NOSEEK);
    if (!scratch || scratch_cap == 0) return (binpazer_status)(w->err = BINPAZER_ERR_RANGE);

    binpazer_reader r;
    binpazer_status st = binpazer_reader_begin(&r, read, seek, ctx, NULL, 0,
                                               entries, entries_cap, entries_count);
    if (st != BINPAZER_OK) return (binpazer_status)(w->err = st);

    for (;;) {
        binpazer_block blk;
        st = binpazer_reader_next_committed(&r, NULL, 0, &blk, scratch, scratch_cap);
        if (st == BINPAZER_PENDING || st == BINPAZER_TORN) break;
        if (st == BINPAZER_END) st = BINPAZER_ERR_ENDED;
        if (st != BINPAZER_OK) return (binpazer_status)(w->err = st);
        /* The seal is checked, so the block's length is safe to step over. */
        uint64_t next = align8(blk.payload_offset + blk.payload_len + 4);
        if (seek(ctx, next) != 0) return (binpazer_status)(w->err = BINPAZER_ERR_IO);
        r.offset = next;
    }

    /* Count the stale bytes after the committed end: a short read is the end of the file. */
    uint64_t end = r.offset, tail = 0;
    for (;;) {
        size_t got = read(ctx, scratch, scratch_cap);
        tail += got;
        if (got < scratch_cap) break;
    }
    if (seek(ctx, end) != 0) return (binpazer_status)(w->err = BINPAZER_ERR_IO);

    w->write = write; w->seek = seek; w->ctx = ctx;
    w->offset = end;
    w->version_minor = r.version_minor;
    w->appendable = 1;
    return w_pad_over(w, tail);
}

binpazer_status binpazer_writer_end(binpazer_writer *w) {
    if (w->err) return (binpazer_status)w->err;
    if (w->appendable) {
        if (w->ended) return BINPAZER_OK;
        if (binpazer_writer_block(w, BINPAZER_TYPE_END, 0, NULL, 0)) return (binpazer_status)w->err;
        w->ended = 1;
    }
    if (w->wrote_index) {
        uint8_t ft[16];
        put_u64(ft + 0, w->index_offset);
        memcpy(ft + 8, "rezapnib", 8);
        if (w_raw(w, ft, 16)) return (binpazer_status)w->err;
    }
    if (w->seek) {
        if (w->used_compression && w->version_minor < BINPAZER_VERSION_MINOR_COMPRESSION) {
            if (w->seek(w->ctx, 14) != 0) return (binpazer_status)(w->err = BINPAZER_ERR_IO);
            uint8_t v[2]; put_u16(v, BINPAZER_VERSION_MINOR_COMPRESSION);
            if (w->write(w->ctx, v, 2) != 2) return (binpazer_status)(w->err = BINPAZER_ERR_IO);
        }
        uint64_t total = w->offset;
        if (w->seek(w->ctx, 32) != 0) return (binpazer_status)(w->err = BINPAZER_ERR_IO);
        uint8_t b[8]; put_u64(b, total);
        if (w->write(w->ctx, b, 8) != 8) return (binpazer_status)(w->err = BINPAZER_ERR_IO);
    }
    return BINPAZER_OK;
}

/* ---- reader ---- */

static binpazer_status r_raw(binpazer_reader *r, void *buf, size_t n) {
    if (r->err) return (binpazer_status)r->err;
    if (r->read(r->ctx, buf, n) != n) { r->err = BINPAZER_ERR_IO; }
    else r->offset += n;
    return (binpazer_status)r->err;
}
static binpazer_status r_skip(binpazer_reader *r, uint64_t n) {
    uint8_t t[64];
    while (n) { size_t c = n < sizeof t ? (size_t)n : sizeof t; if (r_raw(r, t, c)) return (binpazer_status)r->err; n -= c; }
    return BINPAZER_OK;
}

binpazer_status binpazer_reader_begin(
    binpazer_reader *r,
    binpazer_read_fn read, binpazer_seek_fn seek, void *ctx,
    char *writer_name_buf, size_t writer_name_cap,
    binpazer_type_entry *entries, size_t entries_cap, size_t *entries_count)
{
    memset(r, 0, sizeof *r);
    r->read = read; r->seek = seek; r->ctx = ctx;
    if (entries_count) *entries_count = 0;

    uint8_t hdr[56];
    if (r_raw(r, hdr, 56)) return (binpazer_status)r->err;
    if (memcmp(hdr, BINPAZER_MAGIC, 8) != 0) return (binpazer_status)(r->err = BINPAZER_ERR_MAGIC);
    if (hdr[8]!=0x0D || hdr[9]!=0x0A || hdr[10]!=0x1A || hdr[11]!=0x0A)
        return (binpazer_status)(r->err = BINPAZER_ERR_MAGIC);
    r->version_major = get_u16(hdr + 12);
    r->version_minor = get_u16(hdr + 14);
    if (r->version_major > BINPAZER_VERSION_MAJOR) return (binpazer_status)(r->err = BINPAZER_ERR_VERSION);
    memcpy(r->writer_guid.bytes, hdr + 16, 16);
    r->file_length        = get_u64(hdr + 32);
    r->first_block_offset = get_u64(hdr + 40);
    r->header_flags       = get_u32(hdr + 48);

    uint8_t lp[4]; if (r_raw(r, lp, 4)) return (binpazer_status)r->err;
    uint32_t name_len = get_u32(lp);
    if (writer_name_buf && writer_name_cap) {
        uint32_t copy = name_len < writer_name_cap - 1 ? name_len : (uint32_t)(writer_name_cap - 1);
        if (copy && r_raw(r, writer_name_buf, copy)) return (binpazer_status)r->err;
        writer_name_buf[copy] = '\0';
        if (r_skip(r, name_len - copy)) return (binpazer_status)r->err;
    } else {
        if (r_skip(r, name_len)) return (binpazer_status)r->err;
    }
    if (r_skip(r, 1)) return (binpazer_status)r->err;            /* trailing NUL */
    if (r->offset > r->first_block_offset) return (binpazer_status)(r->err = BINPAZER_ERR_FORMAT);
    if (r_skip(r, r->first_block_offset - r->offset)) return (binpazer_status)r->err; /* header pad */

    /* Block Type Table */
    uint8_t bh[8]; if (r_raw(r, bh, 8)) return (binpazer_status)r->err;
    if (get_u16(bh) != BINPAZER_TYPE_BLOCK_TYPE_TABLE) return (binpazer_status)(r->err = BINPAZER_ERR_FORMAT);
    uint64_t btt_start = r->first_block_offset;
    uint32_t btt_len = get_u32(bh + 4);

    uint8_t cc[8]; if (r_raw(r, cc, 8)) return (binpazer_status)r->err;
    uint32_t entry_count = get_u32(cc);

    /* Each entry occupies many bytes on disk (20-byte header, 4-byte name length, empty name, NUL, padded to a set
     * limit), so a count the table's own payload cannot hold is structurally invalid. */
    if (btt_len < 8 || (uint64_t)entry_count * 32u > (uint64_t)btt_len - 8)
        return (binpazer_status)(r->err = BINPAZER_ERR_FORMAT);
    /* An undersized caller buffer is an error, not a silent truncation (the
     * dropped types would later resolve as unknown). */
    if (entries && entry_count > entries_cap) {
        if (entries_count) *entries_count = entry_count;
        return (binpazer_status)(r->err = BINPAZER_ERR_CAPACITY);
    }

    for (uint32_t i = 0; i < entry_count; i++) {
        uint8_t eh[20]; if (r_raw(r, eh, 20)) return (binpazer_status)r->err;
        uint16_t tid = get_u16(eh);
        uint8_t lp2[4]; if (r_raw(r, lp2, 4)) return (binpazer_status)r->err;
        uint32_t nlen = get_u32(lp2);
        if (r_skip(r, (uint64_t)nlen + 1)) return (binpazer_status)r->err; /* name + NUL */
        uint64_t raw = (uint64_t)25 + nlen;
        if (r_skip(r, align8(raw) - raw)) return (binpazer_status)r->err;  /* entry pad */
        if (entries) {
            entries[i].type_id = tid;
            memcpy(entries[i].guid.bytes, eh + 4, 16);
            if (entries_count) *entries_count = i + 1;
        }
    }
    uint64_t btt_end = align8(btt_start + 8 + btt_len);
    if (r->offset > btt_end) return (binpazer_status)(r->err = BINPAZER_ERR_FORMAT);
    if (r_skip(r, btt_end - r->offset)) return (binpazer_status)r->err; /* block pad */

    /* Opportunistically locate a Block Index via the footer. */
    r->has_index = 0;
    if (r->seek && r->file_length != BINPAZER_FILE_LENGTH_UNKNOWN && r->file_length >= 16) {
        uint64_t saved = r->offset;
        uint8_t ft[16];
        if (r->seek(r->ctx, r->file_length - 16) == 0 &&
            r->read(r->ctx, ft, 16) == 16 &&
            memcmp(ft + 8, "rezapnib", 8) == 0) {
            r->index_offset = get_u64(ft);
            r->has_index = 1;
        }
        if (r->seek(r->ctx, saved) != 0) return (binpazer_status)(r->err = BINPAZER_ERR_IO);
        r->offset = saved;
    }
    r->blocks_end = r->file_length;
    if (r->has_index) r->blocks_end = r->file_length - 16; /* exclude the footer */
    return BINPAZER_OK;
}

/* Fills `blk` from an 8-byte block header whose payload starts at
 * `payload_offset`, resolving its GUID. Records the End block, after which an
 * appendable file has no block. */
static void r_block_from_header(
    binpazer_reader *r, const uint8_t bh[8], uint64_t payload_offset,
    const binpazer_type_entry *entries, size_t entries_count, binpazer_block *blk)
{
    blk->type_id = get_u16(bh);
    blk->flags   = get_u16(bh + 2);
    uint32_t lf  = get_u32(bh + 4);
    blk->payload_len = (blk->flags & BINPAZER_FLAG_LARGE_MODE) ? (uint64_t)lf * BINPAZER_LARGE_UNIT : lf;
    blk->payload_offset = payload_offset;
    blk->known = 0;
    memset(blk->guid.bytes, 0, 16);
    for (size_t i = 0; i < entries_count; i++) {
        if (entries[i].type_id == blk->type_id) {
            blk->guid = entries[i].guid; blk->known = 1; break;
        }
    }
    if (blk->type_id == BINPAZER_TYPE_END && (r->header_flags & BINPAZER_HEADER_FLAG_APPENDABLE))
        r->ended = 1;
}

binpazer_status binpazer_reader_next(
    binpazer_reader *r,
    const binpazer_type_entry *entries, size_t entries_count,
    binpazer_block *blk)
{
    if (r->err) return (binpazer_status)r->err;
    if (r->ended) return BINPAZER_END;
    if (r->blocks_end != BINPAZER_FILE_LENGTH_UNKNOWN && r->offset >= r->blocks_end)
        return BINPAZER_END;

    uint8_t bh[8];
    size_t got = r->read(r->ctx, bh, 8);
    if (got == 0) return BINPAZER_END;            /* clean EOF at a boundary */
    if (got != 8) return (binpazer_status)(r->err = BINPAZER_ERR_IO);
    r->offset += 8;

    r_block_from_header(r, bh, r->offset, entries, entries_count, blk);
    return BINPAZER_OK;
}

/* Reads the block at the reader's offset and checks its commit seal. That
 * block is through the raw read callback: the reader's offset and sticky
 * error stay untouched, so the caller can seek back and try again. */
static binpazer_status r_verify_committed(
    binpazer_reader *r, uint8_t bh[8], void *scratch, size_t scratch_cap)
{
    if (r->read(r->ctx, bh, 8) != 8) return BINPAZER_PENDING;
    uint16_t type_id = get_u16(bh);
    uint16_t flags   = get_u16(bh + 2);
    uint32_t lf      = get_u32(bh + 4);
    /* Zeroed space is where a block will be, not a block. */
    if (type_id == BINPAZER_TYPE_INVALID) return BINPAZER_PENDING;
    if (!(flags & BINPAZER_FLAG_HAS_CRC)) return BINPAZER_TORN;

    uint64_t left = (flags & BINPAZER_FLAG_LARGE_MODE) ? (uint64_t)lf * BINPAZER_LARGE_UNIT : lf;
    uint64_t body_end = r->offset + 8 + left + 4;
    uint32_t crc = binpazer_crc32c(0, bh, 8);
    while (left) {
        size_t n = left < (uint64_t)scratch_cap ? (size_t)left : scratch_cap;
        if (r->read(r->ctx, scratch, n) != n) return BINPAZER_PENDING;
        crc = binpazer_crc32c(crc, scratch, n);
        left -= n;
    }
    uint8_t tail[12]; /* the CRC trailer, then the alignment padding */
    size_t tail_len = 4 + (size_t)(align8(body_end) - body_end);
    if (r->read(r->ctx, tail, tail_len) != tail_len) return BINPAZER_PENDING;
    return crc == get_u32(tail) ? BINPAZER_OK : BINPAZER_TORN;
}

binpazer_status binpazer_reader_next_committed(
    binpazer_reader *r,
    const binpazer_type_entry *entries, size_t entries_count,
    binpazer_block *blk, void *scratch, size_t scratch_cap)
{
    if (r->err) return (binpazer_status)r->err;
    if (!r->seek) return (binpazer_status)(r->err = BINPAZER_ERR_NOSEEK);
    if (!blk || !scratch || scratch_cap == 0) return (binpazer_status)(r->err = BINPAZER_ERR_RANGE);
    if (!(r->header_flags & BINPAZER_HEADER_FLAG_APPENDABLE)) return BINPAZER_ERR_NOT_APPENDABLE;
    if (r->ended) return BINPAZER_END;
    if (r->blocks_end != BINPAZER_FILE_LENGTH_UNKNOWN && r->offset >= r->blocks_end)
        return BINPAZER_END;

    /* Seek first: a FILE* that hit the end of the file reads nothing until it is repositioned. */
    uint64_t start = r->offset;
    if (r->seek(r->ctx, start) != 0) return (binpazer_status)(r->err = BINPAZER_ERR_IO);
    uint8_t bh[8];
    binpazer_status st = r_verify_committed(r, bh, scratch, scratch_cap);
    uint64_t at = (st == BINPAZER_OK) ? start + 8 : start;
    if (r->seek(r->ctx, at) != 0) return (binpazer_status)(r->err = BINPAZER_ERR_IO);
    r->offset = at;
    if (st == BINPAZER_OK) r_block_from_header(r, bh, at, entries, entries_count, blk);
    return st;
}

static binpazer_status r_advance_tail(binpazer_reader *r, const binpazer_block *blk) {
    uint64_t crc = (blk->flags & BINPAZER_FLAG_HAS_CRC) ? 4 : 0;
    uint64_t next = align8(blk->payload_offset + blk->payload_len + crc);
    return r_skip(r, next - r->offset);
}

/* The block header bytes as they appear on disk -- the first thing a block's
 * CRC covers. */
static void block_header_bytes(const binpazer_block *blk, uint8_t bh[8]) {
    uint32_t lf = (blk->flags & BINPAZER_FLAG_LARGE_MODE)
                ? (uint32_t)(blk->payload_len / BINPAZER_LARGE_UNIT) : (uint32_t)blk->payload_len;
    put_u16(bh + 0, blk->type_id);
    put_u16(bh + 2, blk->flags);
    put_u32(bh + 4, lf);
}

binpazer_status binpazer_reader_read_payload(
    binpazer_reader *r, const binpazer_block *blk, void *buf, size_t buf_cap)
{
    if (r->err) return (binpazer_status)r->err;
    if (buf_cap < blk->payload_len) return (binpazer_status)(r->err = BINPAZER_ERR_CAPACITY);
    if (blk->payload_len && r_raw(r, buf, (size_t)blk->payload_len)) return (binpazer_status)r->err;
    if (blk->flags & BINPAZER_FLAG_HAS_CRC) {
        uint8_t cb[4]; if (r_raw(r, cb, 4)) return (binpazer_status)r->err;
        uint8_t bh[8];
        block_header_bytes(blk, bh);
        uint32_t crc = binpazer_crc32c(0, bh, 8);
        if (blk->payload_len) crc = binpazer_crc32c(crc, buf, (size_t)blk->payload_len);
        if (crc != get_u32(cb)) return (binpazer_status)(r->err = BINPAZER_ERR_CRC);
    }
    return r_advance_tail(r, blk);
}

binpazer_status binpazer_reader_skip_payload(binpazer_reader *r, const binpazer_block *blk) {
    if (r->err) return (binpazer_status)r->err;
    return r_advance_tail(r, blk);
}

binpazer_status binpazer_reader_stream_payload(
    binpazer_reader *r, const binpazer_block *blk,
    void *scratch, size_t scratch_cap,
    binpazer_sink_fn sink, void *sink_ctx)
{
    if (r->err) return (binpazer_status)r->err;
    if (!blk || !sink || !scratch || scratch_cap == 0) return (binpazer_status)(r->err = BINPAZER_ERR_RANGE);

    int has_crc = (blk->flags & BINPAZER_FLAG_HAS_CRC) != 0;
    uint32_t crc = 0;
    if (has_crc) {
        uint8_t bh[8];
        block_header_bytes(blk, bh);
        crc = binpazer_crc32c(0, bh, 8);
    }

    uint64_t left = blk->payload_len;
    while (left) {
        size_t n = left < (uint64_t)scratch_cap ? (size_t)left : scratch_cap;
        if (r_raw(r, scratch, n)) return (binpazer_status)r->err;
        if (has_crc) crc = binpazer_crc32c(crc, scratch, n);
        binpazer_status s = sink(sink_ctx, scratch, n);
        if (s != BINPAZER_OK) return (binpazer_status)(r->err = s);
        left -= n;
    }

    if (has_crc) {
        uint8_t cb[4];
        if (r_raw(r, cb, 4)) return (binpazer_status)r->err;
        /* Reported after the fact. */
        if (crc != get_u32(cb)) return (binpazer_status)(r->err = BINPAZER_ERR_CRC);
    }
    return r_advance_tail(r, blk);
}

binpazer_status binpazer_guid_format(const binpazer_guid *g, char *buf, size_t cap) {
    static const char hex[] = "0123456789abcdef";
    if (!g || !buf || cap < BINPAZER_GUID_STRING_SIZE) return BINPAZER_ERR_CAPACITY;
    size_t at = 0;
    for (size_t i = 0; i < 16; i++) {
        if (i == 4 || i == 6 || i == 8 || i == 10) buf[at++] = '-';
        buf[at++] = hex[g->bytes[i] >> 4];
        buf[at++] = hex[g->bytes[i] & 0x0F];
    }
    buf[at] = '\0';
    return BINPAZER_OK;
}

binpazer_status binpazer_guid_parse(const char *s, binpazer_guid *out) {
    if (!s || !out) return BINPAZER_ERR_RANGE;
    int nibbles = 0;
    uint8_t acc = 0;
    for (const char *p = s; *p; p++) {
        if (*p == '-') continue; /* hyphens are separators, not data */
        uint8_t v;
        if (*p >= '0' && *p <= '9')      v = (uint8_t)(*p - '0');
        else if (*p >= 'a' && *p <= 'f') v = (uint8_t)(*p - 'a' + 10);
        else if (*p >= 'A' && *p <= 'F') v = (uint8_t)(*p - 'A' + 10);
        else return BINPAZER_ERR_FORMAT;
        if (nibbles >= 32) return BINPAZER_ERR_FORMAT;
        acc = (uint8_t)((acc << 4) | v);
        if (nibbles % 2 == 1) out->bytes[nibbles / 2] = acc;
        nibbles++;
    }
    return nibbles == 32 ? BINPAZER_OK : BINPAZER_ERR_FORMAT;
}

binpazer_status binpazer_reader_find(
    binpazer_reader *r, uint16_t type_id,
    binpazer_index_entry *out, size_t cap, size_t *count)
{
    if (r->err) return (binpazer_status)r->err;
    if (!r->seek) return (binpazer_status)(r->err = BINPAZER_ERR_NOSEEK);
    if (count) *count = 0;

    size_t total = 0;
    uint64_t saved = r->offset;
    binpazer_status st = BINPAZER_OK;

    do {
        if (r->has_index) {
            /* Scan only the compact index -- no file walk. */
            if (r->seek(r->ctx, r->index_offset) != 0) { st = BINPAZER_ERR_IO; break; }
            r->offset = r->index_offset;
            uint8_t bh[8], cc[8];
            if (r_raw(r, bh, 8) || r_raw(r, cc, 8)) { st = (binpazer_status)r->err; break; }
            uint32_t entry_count = get_u32(cc);
            for (uint32_t i = 0; i < entry_count; i++) {
                uint8_t e[16];
                if (r_raw(r, e, 16)) { st = (binpazer_status)r->err; break; }
                if (get_u16(e) == type_id) {
                    if (total < cap) { out[total].type_id = type_id; out[total].offset = get_u64(e + 8); }
                    total++;
                }
            }
        } else {
            /* No index -- walk the file. */
            if (r->seek(r->ctx, r->first_block_offset) != 0) { st = BINPAZER_ERR_IO; break; }
            r->offset = r->first_block_offset;
            int appendable = (r->header_flags & BINPAZER_HEADER_FLAG_APPENDABLE) != 0;
            while (r->blocks_end == BINPAZER_FILE_LENGTH_UNKNOWN || r->offset < r->blocks_end) {
                uint64_t blk_off = r->offset;
                uint8_t bh[8];
                if (appendable) {
                    /* Only committed blocks of an appendable file are found; the walk stops where they stop. */
                    uint8_t tmp[512];
                    if (r_verify_committed(r, bh, tmp, sizeof tmp) != BINPAZER_OK) break;
                } else {
                    size_t got = r->read(r->ctx, bh, 8);
                    if (got == 0) break;
                    if (got != 8) { st = BINPAZER_ERR_IO; break; }
                }
                r->offset += 8;
                uint16_t tid = get_u16(bh);
                uint16_t flags = get_u16(bh + 2);
                uint32_t lf = get_u32(bh + 4);
                uint64_t plen = (flags & BINPAZER_FLAG_LARGE_MODE) ? (uint64_t)lf * BINPAZER_LARGE_UNIT : lf;
                if (tid == type_id) {
                    if (total < cap) { out[total].type_id = tid; out[total].offset = blk_off; }
                    total++;
                }
                uint64_t crc = (flags & BINPAZER_FLAG_HAS_CRC) ? 4 : 0;
                uint64_t next = align8(blk_off + 8 + plen + crc);
                if (r->seek(r->ctx, next) != 0) { st = BINPAZER_ERR_IO; break; }
                r->offset = next;
            }
        }
    } while (0);

    if (count) *count = total;
    if (st == BINPAZER_OK && total > cap) st = BINPAZER_ERR_CAPACITY;
    if (r->seek(r->ctx, saved) == 0) r->offset = saved; /* restore position */
    if (st != BINPAZER_OK && st != BINPAZER_ERR_CAPACITY) r->err = st;
    return st;
}

binpazer_status binpazer_reader_at(
    binpazer_reader *r, const binpazer_type_entry *entries, size_t entries_count,
    uint64_t offset, binpazer_block *blk)
{
    if (r->err) return (binpazer_status)r->err;
    if (!r->seek) return (binpazer_status)(r->err = BINPAZER_ERR_NOSEEK);
    if (r->seek(r->ctx, offset) != 0) return (binpazer_status)(r->err = BINPAZER_ERR_IO);
    r->offset = offset;

    uint8_t bh[8];
    if (r_raw(r, bh, 8)) return (binpazer_status)r->err;
    blk->type_id = get_u16(bh);
    blk->flags   = get_u16(bh + 2);
    uint32_t lf  = get_u32(bh + 4);
    blk->payload_len = (blk->flags & BINPAZER_FLAG_LARGE_MODE) ? (uint64_t)lf * BINPAZER_LARGE_UNIT : lf;
    blk->payload_offset = r->offset;
    blk->known = 0;
    memset(blk->guid.bytes, 0, 16);
    for (size_t i = 0; i < entries_count; i++) {
        if (entries[i].type_id == blk->type_id) { blk->guid = entries[i].guid; blk->known = 1; break; }
    }
    return BINPAZER_OK;
}
