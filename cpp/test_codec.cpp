// Codec-layer tests for the C++ wrapper: every registry codec must round-trip
// through a real file. Separate from test.cpp so that one keeps proving the
// wrapper builds and passes against the core alone, linking no codec library.
#include "binpazer.hpp"

#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

namespace {

struct MemBuf {
    std::vector<uint8_t> data;
    size_t pos = 0;

    size_t write(const void *buf, size_t n) {
        if (pos + n > data.size()) data.resize(pos + n);
        std::memcpy(data.data() + pos, buf, n);
        pos += n;
        return n;
    }
    size_t read(void *buf, size_t n) {
        size_t avail = data.size() - pos;
        size_t c = n < avail ? n : avail;
        std::memcpy(buf, data.data() + pos, c);
        pos += c;
        return c;
    }
    bool seek(uint64_t off) {
        if (off > data.size()) data.resize(static_cast<size_t>(off));
        pos = static_cast<size_t>(off);
        return true;
    }
};

int failures = 0;
void check(bool cond, const char *msg) {
    if (!cond) { std::printf("FAIL: %s\n", msg); failures++; }
}

// Compressible on purpose: a real codec must visibly shrink it, which is what
// proves the bytes went through the codec rather than past it.
std::vector<uint8_t> make_payload() {
    const std::string line = "binpazer compression parity across implementations. ";
    std::vector<uint8_t> p(8192);
    for (size_t i = 0; i < p.size(); i++) p[i] = static_cast<uint8_t>(line[i % line.size()]);
    return p;
}

void roundtrip(uint16_t codec_id, const char *name, bool expect_shrink,
               const binpazer::Guid *codec_guid = nullptr) {
    const std::vector<uint8_t> payload = make_payload();
    MemBuf m;

    binpazer::Guid wg{{0x01}};
    std::vector<binpazer::TypeDef> defs{{1, binpazer::Guid{{0x11}}, "Payload"}};
    {
        binpazer::Writer w([&](const void *b, size_t n) { return m.write(b, n); }, wg, "codec", defs,
                           [&](uint64_t o) { return m.seek(o); });
        check(w.compress_block(1, binpazer::flag::HasCrc, codec_id, payload, codec_guid) ==
                  binpazer::Status::Ok, name);
        check(w.end() == binpazer::Status::Ok, "codec: writer end");
    }

    m.pos = 0;
    binpazer::Reader r([&](void *b, size_t n) { return m.read(b, n); },
                       [&](uint64_t o) { return m.seek(o); });
    check(r.ok(), "codec: reader begin");
    check(r.version_minor() == BINPAZER_VERSION_MINOR_COMPRESSION, "codec: file declares 1.1");

    auto blk = r.next();
    check(blk.has_value(), "codec: next block");
    if (!blk) return;
    check((blk->flags & binpazer::flag::Compressed) != 0, "codec: compressed flag set");
    if (expect_shrink)
        check(blk->payload_len < payload.size(), "codec: stored payload is smaller than the logical bytes");

    auto got = r.read_payload_decompressed(*blk);
    check(got.has_value(), name);
    check(got && *got == payload, "codec: decoded bytes match");
}

// An uncompressed block must pass through the decompressing reader unchanged.
void test_uncompressed_passthrough() {
    MemBuf m;
    binpazer::Guid wg{{0x01}};
    std::vector<binpazer::TypeDef> defs{{1, binpazer::Guid{{0x11}}, "Payload"}};
    {
        binpazer::Writer w([&](const void *b, size_t n) { return m.write(b, n); }, wg, "codec", defs,
                           [&](uint64_t o) { return m.seek(o); });
        w.block(1, binpazer::flag::HasCrc, std::string_view("plain bytes"));
        w.end();
    }
    m.pos = 0;
    binpazer::Reader r([&](void *b, size_t n) { return m.read(b, n); },
                       [&](uint64_t o) { return m.seek(o); });
    check(r.version_minor() == 0, "passthrough: no compressed block keeps the file 1.0");
    auto blk = r.next();
    check(blk.has_value(), "passthrough: next block");
    if (!blk) return;
    auto got = r.read_payload_decompressed(*blk);
    check(got.has_value(), "passthrough: read uncompressed block");
    check(got && std::string(got->begin(), got->end()) == "plain bytes", "passthrough: bytes match");
}

// Buffer-level helpers, independent of any file.
void test_buffer_helpers() {
    const std::vector<uint8_t> payload = make_payload();
    auto enc = binpazer::compress(binpazer::codec::Zstd, payload);
    check(enc.has_value(), "buffers: compress");
    if (!enc) return;
    check(enc->size() < payload.size(), "buffers: zstd shrinks");

    binpazer::Compression c{};
    c.codec_id = binpazer::codec::Zstd;
    c.stored_length = enc->size();
    c.uncompressed_length = payload.size();
    auto dec = binpazer::decompress(c, enc->data());
    check(dec.has_value() && *dec == payload, "buffers: decompress round-trip");

    // The envelope's length is authoritative: a wrong one is corruption.
    c.uncompressed_length = payload.size() - 1;
    check(!binpazer::decompress(c, enc->data()).has_value(), "buffers: wrong declared length rejected");
}

// The GUID escape, registered through the C registry the wrapper shares.
binpazer_status xor_compress(void *, const void *src, uint64_t n, void *dst, uint64_t *dst_len) {
    if (*dst_len < n) return BINPAZER_ERR_CAPACITY;
    for (uint64_t i = 0; i < n; i++)
        static_cast<uint8_t *>(dst)[i] = static_cast<const uint8_t *>(src)[i] ^ 0x5Au;
    *dst_len = n;
    return BINPAZER_OK;
}
binpazer_status xor_decompress(void *, const void *src, uint64_t n, void *dst, uint64_t dst_len) {
    if (n != dst_len) return BINPAZER_ERR_FORMAT;
    for (uint64_t i = 0; i < n; i++)
        static_cast<uint8_t *>(dst)[i] = static_cast<const uint8_t *>(src)[i] ^ 0x5Au;
    return BINPAZER_OK;
}
uint64_t xor_bound(void *, uint64_t n) { return n; }

void test_guid_codec() {
    static const binpazer_codec xor_codec{xor_compress, xor_decompress, xor_bound, nullptr};
    binpazer::Guid g{{0xAA, 0xBB, 0xCC, 0xDD}};
    check(binpazer_codec_register_guid(&g, &xor_codec) == BINPAZER_OK, "guid codec: register");
    roundtrip(binpazer::codec::ByGuid, "guid codec: round-trip", false, &g);
}

} // namespace

int main() {
    roundtrip(binpazer::codec::Stored,  "codec stored: round-trip",  false);
    roundtrip(binpazer::codec::Deflate, "codec deflate: round-trip", true);
    roundtrip(binpazer::codec::Zstd,    "codec zstd: round-trip",    true);
    roundtrip(binpazer::codec::Lz4,     "codec lz4: round-trip",     true);

    test_uncompressed_passthrough();
    test_buffer_helpers();
    test_guid_codec();

    if (failures == 0) { std::printf("OK: all C++ codec checks passed\n"); return 0; }
    std::printf("%d FAILURE(S)\n", failures);
    return 1;
}
