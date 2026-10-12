// binpazer -- idiomatic C++ wrapper over the C reference implementation.
// Header-only. Links against the C core (binpazer.c); no file IO, no mmap --
// the caller injects IO via std::function callbacks.
//
// Depends only on the standard library, binpazer.h and binpazer_codec.h. The
// codec entry points (compress, decompress, Writer::compress_block,
// Reader::read_payload_decompressed) additionally need binpazer_codec.c and
// its libraries at LINK time -- and only if you call them. They are inline, so
// a program that never compresses links the core alone, exactly as before.
#ifndef BINPAZER_HPP
#define BINPAZER_HPP

#include "binpazer.h"
#include "binpazer_codec.h"

#include <cstdint>
#include <cstddef>
#include <functional>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace binpazer {

using Guid      = binpazer_guid;
using Block     = binpazer_block;
using TypeEntry = binpazer_type_entry;

inline const Guid &block_type_table_guid() { return BINPAZER_GUID_BLOCK_TYPE_TABLE; }

enum class Status : int {
    Ok          = BINPAZER_OK,
    End         = BINPAZER_END,
    ErrIo       = BINPAZER_ERR_IO,
    ErrMagic    = BINPAZER_ERR_MAGIC,
    ErrVersion  = BINPAZER_ERR_VERSION,
    ErrFormat   = BINPAZER_ERR_FORMAT,
    ErrRange    = BINPAZER_ERR_RANGE,
    ErrCrc      = BINPAZER_ERR_CRC,
    ErrCapacity = BINPAZER_ERR_CAPACITY,
};

namespace flag {
enum : uint16_t {
    Critical   = BINPAZER_FLAG_CRITICAL,
    SafeToCopy = BINPAZER_FLAG_SAFE_TO_COPY,
    Compressed = BINPAZER_FLAG_COMPRESSED,
    HasCrc     = BINPAZER_FLAG_HAS_CRC,
    LargeMode  = BINPAZER_FLAG_LARGE_MODE,
};
} // namespace flag

// Codec ids from SPEC.md's registry. Every one of them is implemented (via the
// C codec layer), so this wrapper reads and writes any compressed file any
// binpazer implementation can produce. ByGuid is the escape for codecs the
// registry does not name; register one with binpazer_codec_register_guid.
namespace codec {
enum : uint16_t {
    Stored  = BINPAZER_CODEC_STORED,
    Deflate = BINPAZER_CODEC_DEFLATE,
    Zstd    = BINPAZER_CODEC_ZSTD,
    Lz4     = BINPAZER_CODEC_LZ4,
    ByGuid  = BINPAZER_CODEC_BY_GUID,
};
} // namespace codec

using Compression = binpazer_compression;

// Parses a Compression Envelope from the front of a payload already read.
inline std::optional<Compression> parse_compression(const void *payload, uint64_t payload_len) {
    Compression c{};
    if (binpazer_compression_parse(payload, payload_len, &c) != BINPAZER_OK) return std::nullopt;
    return c;
}

// Encodes `src` with a codec, sizing the result from the codec's own bound.
// Returns nullopt when the codec is unavailable or the encode fails.
inline std::optional<std::vector<uint8_t>> compress(uint16_t codec_id, const void *src, uint64_t len,
                                                    const Guid *codec_guid = nullptr) {
    uint64_t cap = binpazer_compress_bound(codec_id, codec_guid, len);
    if (cap == 0 && len != 0) return std::nullopt;
    std::vector<uint8_t> out(static_cast<size_t>(cap));
    uint64_t n = cap;
    if (binpazer_compress(codec_id, codec_guid, src, len, out.data(), &n) != BINPAZER_OK)
        return std::nullopt;
    out.resize(static_cast<size_t>(n));
    return out;
}

inline std::optional<std::vector<uint8_t>> compress(uint16_t codec_id, const std::vector<uint8_t> &src,
                                                    const Guid *codec_guid = nullptr) {
    return compress(codec_id, src.data(), src.size(), codec_guid);
}

// Decodes an envelope's codec bytes. `stored` points at the codec bytes, i.e.
// the payload advanced by c.header_size. The envelope's uncompressed_length is
// authoritative: a decode of any other length fails rather than truncating.
inline std::optional<std::vector<uint8_t>> decompress(const Compression &c, const void *stored) {
    std::vector<uint8_t> out(static_cast<size_t>(c.uncompressed_length));
    if (binpazer_decompress(&c, stored, out.data(), out.size()) != BINPAZER_OK) return std::nullopt;
    return out;
}

inline std::string to_string(const Guid &g) {
    char buf[BINPAZER_GUID_STRING_SIZE];
    if (binpazer_guid_format(&g, buf, sizeof buf) != BINPAZER_OK) return {};
    return std::string(buf);
}

// Parses canonical UUID text; hyphens optional.
inline std::optional<Guid> parse_guid(std::string_view s) {
    Guid g{};
    if (binpazer_guid_parse(std::string(s).c_str(), &g) != BINPAZER_OK) return std::nullopt;
    return g;
}

using GroupChild = binpazer_group_child;

// Parses a Group block's payload into its nested blocks. Each child's
// `payload` points INTO the buffer passed in, so keep it alive. A structural
// error yields the intact prefix, matching the C and Go parsers -- check
// `ok` to tell a clean parse from a truncated one.
inline std::vector<GroupChild> parse_group(const void *payload, uint64_t len, bool *ok = nullptr) {
    size_t n = 0;
    binpazer_group_parse(payload, len, nullptr, 0, &n);
    std::vector<GroupChild> out(n);
    binpazer_status s = binpazer_group_parse(payload, len, out.data(), out.size(), &n);
    out.resize(n < out.size() ? n : out.size());
    if (ok) *ok = (s == BINPAZER_OK);
    return out;
}

inline std::vector<GroupChild> parse_group(const std::vector<uint8_t> &payload, bool *ok = nullptr) {
    return parse_group(payload.data(), payload.size(), ok);
}

// IO callbacks. read/write return bytes transferred (fread/fwrite style); seek
// returns true on success. An empty `SeekFn` means no seek capability.
using ReadFn  = std::function<size_t(void *buf, size_t n)>;
using WriteFn = std::function<size_t(const void *buf, size_t n)>;
using SeekFn  = std::function<bool(uint64_t offset)>;

struct TypeDef {
    uint16_t    type_id;
    Guid        guid;
    std::string name;
};

namespace detail {
struct IoCtx { ReadFn read; WriteFn write; SeekFn seek; };
inline size_t tramp_read(void *c, void *b, size_t n)        { return static_cast<IoCtx *>(c)->read(b, n); }
inline size_t tramp_write(void *c, const void *b, size_t n) { return static_cast<IoCtx *>(c)->write(b, n); }
inline int    tramp_seek(void *c, uint64_t o)              { return static_cast<IoCtx *>(c)->seek(o) ? 0 : -1; }
} // namespace detail

class Writer {
public:
    // Writes the file header and Block Type Table immediately. Do not include
    // type a later id in `defs`; the table entry is added automatically.
    //
    // `version_minor` declares a minor version in the header up front. The one
    // reason to set it: a compressed block normally raises the version by
    // back-patching the header at end(). Which needs a seek callback -- pass
    // BINPAZER_VERSION_MINOR_COMPRESSION and a STREAMING writer can compress
    // too.
    Writer(WriteFn write, const Guid &writer_guid, std::string_view name,
           const std::vector<TypeDef> &defs, SeekFn seek = {},
           uint16_t version_minor = BINPAZER_VERSION_MINOR) {
        io_.write = std::move(write);
        io_.seek  = std::move(seek);
        name_     = std::string(name);

        std::vector<binpazer_type_def> cdefs;
        cdefs.reserve(defs.size());
        for (const auto &d : defs)
            cdefs.push_back(binpazer_type_def{d.type_id, d.guid, d.name.c_str()});

        binpazer_writer_begin_version(&w_, detail::tramp_write,
                                      io_.seek ? detail::tramp_seek : nullptr, &io_,
                                      &writer_guid, name_.c_str(),
                                      cdefs.data(), cdefs.size(), version_minor);
    }

    Writer(const Writer &) = delete;
    Writer &operator=(const Writer &) = delete;
    Writer(Writer &&) = delete; // w_.ctx points at io_; address must be stable
    Writer &operator=(Writer &&) = delete;

    Status block(uint16_t type_id, uint16_t flags, const void *payload, uint64_t len) {
        idx_.push_back(binpazer_index_entry{type_id, w_.offset}); // record for the index
        return static_cast<Status>(binpazer_writer_block(&w_, type_id, flags, payload, len));
    }
    Status block(uint16_t type_id, uint16_t flags, const std::vector<uint8_t> &payload) {
        return block(type_id, flags, payload.data(), payload.size());
    }
    Status block(uint16_t type_id, uint16_t flags, std::string_view payload) {
        return block(type_id, flags, payload.data(), payload.size());
    }
    // Writes a block whose payload is a Compression Envelope around `stored` --
    // bytes the caller has already encoded with `codec_id` (for codec::Stored
    // they are the payload itself).
    Status compressed_block(uint16_t type_id, uint16_t flags, uint16_t codec_id,
                            const void *stored, uint64_t stored_len, uint64_t uncompressed_len,
                            const Guid *codec_guid = nullptr) {
        idx_.push_back(binpazer_index_entry{type_id, w_.offset}); // record for the index
        return static_cast<Status>(binpazer_writer_block_compressed(
            &w_, type_id, flags, codec_id, codec_guid, stored, stored_len, uncompressed_len));
    }
    Status compressed_block(uint16_t type_id, uint16_t flags, uint16_t codec_id,
                            std::string_view stored, uint64_t uncompressed_len,
                            const Guid *codec_guid = nullptr) {
        return compressed_block(type_id, flags, codec_id, stored.data(), stored.size(),
                                uncompressed_len, codec_guid);
    }
    // Writes a block from its LOGICAL payload, compressing it with `codec_id`
    // -- the counterpart of compressed_block, which takes bytes the caller
    // already encoded. Needs binpazer_codec.c at link time.
    Status compress_block(uint16_t type_id, uint16_t flags, uint16_t codec_id,
                          const void *payload, uint64_t len,
                          const Guid *codec_guid = nullptr) {
        auto enc = binpazer::compress(codec_id, payload, len, codec_guid);
        if (!enc) return Status::ErrRange;
        return compressed_block(type_id, flags, codec_id, enc->data(), enc->size(), len, codec_guid);
    }
    Status compress_block(uint16_t type_id, uint16_t flags, uint16_t codec_id,
                          const std::vector<uint8_t> &payload, const Guid *codec_guid = nullptr) {
        return compress_block(type_id, flags, codec_id, payload.data(), payload.size(), codec_guid);
    }
    Status compress_block(uint16_t type_id, uint16_t flags, uint16_t codec_id,
                          std::string_view payload, const Guid *codec_guid = nullptr) {
        return compress_block(type_id, flags, codec_id, payload.data(), payload.size(), codec_guid);
    }
    // Writes a Block Index covering every block written so far. Call before end();
    // end() then emits the footer that locates it.
    Status write_index() {
        return static_cast<Status>(binpazer_writer_index(&w_, idx_.data(), idx_.size()));
    }
    Status end() { return static_cast<Status>(binpazer_writer_end(&w_)); }

    uint64_t offset() const { return w_.offset; } // == the next block's offset
    bool     ok()     const { return w_.err == BINPAZER_OK; }
    Status   status() const { return static_cast<Status>(w_.err); }

private:
    detail::IoCtx                     io_{};
    binpazer_writer                   w_{};
    std::string                       name_;
    std::vector<binpazer_index_entry> idx_;
};

class Reader {
public:
    // Type ids are 16-bit, so no conformant Block Type Table can hold more
    // distinct entries than this. Larger declared counts are rejected as
    // Status::ErrCapacity instead of being allocated for.
    static constexpr size_t kMaxTypeEntries = 65536;

    explicit Reader(ReadFn read, SeekFn seek = {}) {
        io_.read = std::move(read);
        io_.seek = std::move(seek);
        entries_.resize(256); // covers typical files without a second pass
        char    namebuf[256] = {0};
        size_t  n = 0;
        binpazer_status s = binpazer_reader_begin(
            &r_, detail::tramp_read, io_.seek ? detail::tramp_seek : nullptr,
            &io_, namebuf, sizeof namebuf, entries_.data(), entries_.size(), &n);
        if (s == BINPAZER_ERR_CAPACITY && n <= kMaxTypeEntries &&
            io_.seek && io_.seek(0)) {
            // The table declares more entries than the buffer: size the buffer
            // from the declared count (already validated by the C core against
            // the table's own byte length) and parse again from the start.
            // Without a seek callback the error stands -- never truncate.
            entries_.resize(n);
            n = 0;
            s = binpazer_reader_begin(
                &r_, detail::tramp_read, detail::tramp_seek, &io_,
                namebuf, sizeof namebuf, entries_.data(), entries_.size(), &n);
        }
        entries_.resize(s == BINPAZER_OK ? n : 0);
        writer_name_ = namebuf;
    }

    Reader(const Reader &) = delete;
    Reader &operator=(const Reader &) = delete;
    Reader(Reader &&) = delete;
    Reader &operator=(Reader &&) = delete;

    bool   ok()     const { return r_.err == BINPAZER_OK; }
    Status status() const { return static_cast<Status>(r_.err); }

    const std::string &writer_name() const { return writer_name_; }
    Guid     writer_guid()  const { return r_.writer_guid; }
    uint16_t version_major() const { return r_.version_major; }
    uint16_t version_minor() const { return r_.version_minor; }
    uint64_t file_length()   const { return r_.file_length; }
    const std::vector<TypeEntry> &types() const { return entries_; }

    // Returns the next block, or nullopt at end / on error (check status()).
    std::optional<Block> next() {
        Block b{};
        binpazer_status s = binpazer_reader_next(&r_, entries_.data(), entries_.size(), &b);
        if (s != BINPAZER_OK) return std::nullopt;
        return b;
    }

    Status read_payload(const Block &b, void *buf, size_t cap) {
        return static_cast<Status>(binpazer_reader_read_payload(&r_, &b, buf, cap));
    }
    // Convenience: returns the payload, or an empty vector on error.
    std::vector<uint8_t> read_payload(const Block &b) {
        std::vector<uint8_t> v(b.payload_len);
        if (binpazer_reader_read_payload(&r_, &b, v.data(), v.size()) != BINPAZER_OK)
            v.clear();
        return v;
    }
    Status skip_payload(const Block &b) {
        return static_cast<Status>(binpazer_reader_skip_payload(&r_, &b));
    }
    // Streams the payload through `sink` in `chunk`-sized pieces (return false
    // to abort), verifying the CRC, then advances past the block. Peak memory
    // is `chunk`, not the payload -- for blocks too big to hold at once.
    using SinkFn = std::function<bool(const void *buf, size_t n)>;
    Status stream_payload(const Block &b, const SinkFn &sink, size_t chunk = 64 * 1024) {
        std::vector<uint8_t> scratch(chunk ? chunk : 1);
        auto tramp = [](void *ctx, const void *buf, size_t n) -> binpazer_status {
            return (*static_cast<SinkFn *>(ctx))(buf, n) ? BINPAZER_OK : BINPAZER_ERR_IO;
        };
        return static_cast<Status>(binpazer_reader_stream_payload(
            &r_, &b, scratch.data(), scratch.size(), tramp,
            const_cast<SinkFn *>(&sink)));
    }
    // Reads the current block's payload, decompressing it when the block
    // carries a Compression Envelope, and advances past it. An uncompressed
    // block comes back unchanged, so one call reads a whole file's blocks
    // whether or not they are compressed. nullopt on error.
    std::optional<std::vector<uint8_t>> read_payload_decompressed(const Block &b) {
        std::vector<uint8_t> raw(static_cast<size_t>(b.payload_len));
        if (binpazer_reader_read_payload(&r_, &b, raw.data(), raw.size()) != BINPAZER_OK)
            return std::nullopt;
        if ((b.flags & BINPAZER_FLAG_COMPRESSED) == 0) return raw;
        Compression c{};
        if (binpazer_compression_parse(raw.data(), b.payload_len, &c) != BINPAZER_OK)
            return std::nullopt;
        return binpazer::decompress(c, raw.data() + c.header_size);
    }

    bool     has_index() const { return r_.has_index != 0; }
    uint64_t offset()    const { return r_.offset; }

    // Offsets of all blocks with this type id, using the Block Index when present
    // (seeking straight to them) and otherwise walking the file.
    std::vector<uint64_t> find(uint16_t type_id) {
        std::vector<binpazer_index_entry> buf(16);
        size_t n = 0;
        binpazer_status s = binpazer_reader_find(&r_, type_id, buf.data(), buf.size(), &n);
        if (s == BINPAZER_ERR_CAPACITY) {
            buf.resize(n);
            s = binpazer_reader_find(&r_, type_id, buf.data(), buf.size(), &n);
        }
        std::vector<uint64_t> out;
        if (s == BINPAZER_OK) {
            out.reserve(n);
            for (size_t i = 0; i < n; ++i) out.push_back(buf[i].offset);
        }
        return out;
    }

    // Seeks to a block (e.g. an offset from find) and returns its header; nullopt
    // on error. After this, read_payload reads that block.
    std::optional<Block> at(uint64_t offset) {
        Block b{};
        if (binpazer_reader_at(&r_, entries_.data(), entries_.size(), offset, &b) != BINPAZER_OK)
            return std::nullopt;
        return b;
    }

private:
    detail::IoCtx                    io_{};
    binpazer_reader                  r_{};
    std::vector<binpazer_type_entry> entries_;
    std::string                      writer_name_;
};

} // namespace binpazer

#endif // BINPAZER_HPP
