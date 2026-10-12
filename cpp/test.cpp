// Round-trip test for the binpazer C++ wrapper. In-memory IO via lambdas.
#include "binpazer.hpp"

#include <cstdio>
#include <cstring>
#include <vector>

static int failures = 0;
#define CHECK(cond, msg) do { if (!(cond)) { std::printf("FAIL: %s\n", msg); ++failures; } } while (0)

int main() {
    std::vector<uint8_t> buf;
    size_t pos = 0;

    auto write = [&](const void *b, size_t n) -> size_t {
        if (pos + n > buf.size()) buf.resize(pos + n);
        std::memcpy(buf.data() + pos, b, n);
        pos += n;
        return n;
    };
    auto seek = [&](uint64_t o) -> bool {
        if (o > buf.size()) buf.resize(static_cast<size_t>(o));
        pos = static_cast<size_t>(o);
        return true;
    };

    binpazer::Guid writer{{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
    binpazer::Guid g_mesh{{0x11,0x22,0x33,0x44,0x55,0x66,0x77,0x88,0x99,0xaa,0xbb,0xcc,0xdd,0xee,0xff,0x00}};
    binpazer::Guid g_note{{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};

    std::vector<binpazer::TypeDef> defs = {
        {1, g_mesh, "Mesh"},
        {2, g_note, "Note"},
    };

    const char *mesh = "vertices-go-here";
    const char *note = "hello, file!";
    const char *big  = "0123456789ABCDEF01234567";

    {
        binpazer::Writer w(write, writer, "demo", defs, seek);
        CHECK(w.ok(), "writer constructed");
        CHECK(w.block(1, binpazer::flag::Critical | binpazer::flag::HasCrc, mesh, 16) == binpazer::Status::Ok, "write mesh");
        CHECK(w.block(2, 0, note, 12) == binpazer::Status::Ok, "write note");
        CHECK(w.block(1, 0, big, 24) == binpazer::Status::Ok, "write third");
        CHECK(w.end() == binpazer::Status::Ok, "writer end");
        CHECK(w.ok(), "writer no error");
    }
    std::printf("wrote %zu bytes\n", buf.size());

    pos = 0;
    auto read = [&](void *b, size_t n) -> size_t {
        size_t avail = buf.size() - pos;
        size_t c = n < avail ? n : avail;
        std::memcpy(b, buf.data() + pos, c);
        pos += c;
        return c;
    };

    binpazer::Reader r(read, seek);
    CHECK(r.ok(), "reader constructed");
    CHECK(r.writer_name() == "demo", "writer name");
    CHECK(r.version_major() == 1 && r.version_minor() == 0, "version");
    CHECK(std::memcmp(r.writer_guid().bytes, writer.bytes, 16) == 0, "writer guid");
    CHECK(r.file_length() == buf.size(), "file_length patched");
    CHECK(r.types().size() == 3, "btt entry count");
    CHECK(r.types()[0].type_id == BINPAZER_TYPE_BLOCK_TYPE_TABLE, "entry0 BTT");
    CHECK(r.types()[1].type_id == 1 && std::memcmp(r.types()[1].guid.bytes, g_mesh.bytes,16)==0, "entry1 Mesh");
    CHECK(r.types()[2].type_id == 2 && std::memcmp(r.types()[2].guid.bytes, g_note.bytes,16)==0, "entry2 Note");

    auto b1 = r.next();
    CHECK(b1.has_value() && b1->type_id == 1 && b1->known, "block1 mesh");
    CHECK((b1->flags & binpazer::flag::HasCrc) != 0, "block1 has crc");
    auto p1 = r.read_payload(*b1);
    CHECK(p1.size() == 16 && std::memcmp(p1.data(), mesh, 16) == 0, "block1 payload (crc verified)");

    auto b2 = r.next();
    CHECK(b2.has_value() && b2->type_id == 2 && b2->payload_len == 12, "block2 note");
    auto p2 = r.read_payload(*b2);
    CHECK(p2.size() == 12 && std::memcmp(p2.data(), note, 12) == 0, "block2 payload");

    auto b3 = r.next();
    CHECK(b3.has_value() && b3->type_id == 1 && (b3->flags & binpazer::flag::LargeMode) == 0, "block3 normal");
    CHECK(b3->payload_len == 24, "block3 len");
    auto p3 = r.read_payload(*b3);
    CHECK(p3.size() == 24 && std::memcmp(p3.data(), big, 24) == 0, "block3 payload");

    auto b4 = r.next();
    CHECK(!b4.has_value() && r.ok(), "clean end");

    // --- Block Index: write with an index, discover it, find + at ---
    {
        std::vector<uint8_t> ibuf;
        size_t ip = 0;
        auto iwrite = [&](const void *b, size_t n) -> size_t {
            if (ip + n > ibuf.size()) ibuf.resize(ip + n);
            std::memcpy(ibuf.data() + ip, b, n); ip += n; return n;
        };
        auto iseek = [&](uint64_t o) -> bool {
            if (o > ibuf.size()) ibuf.resize(static_cast<size_t>(o));
            ip = static_cast<size_t>(o); return true;
        };
        uint64_t offA, offB, offC;
        {
            binpazer::Writer w(iwrite, writer, "demo", defs, iseek);
            offA = w.offset(); w.block(1, 0, "aaaa", 4);
            offB = w.offset(); w.block(2, 0, "bb", 2);
            offC = w.offset(); w.block(1, 0, "cccccc", 6);
            CHECK(w.write_index() == binpazer::Status::Ok, "index: write_index");
            CHECK(w.end() == binpazer::Status::Ok, "index: end");
        }
        ip = 0;
        auto iread = [&](void *b, size_t n) -> size_t {
            size_t av = ibuf.size() - ip, c = n < av ? n : av;
            std::memcpy(b, ibuf.data() + ip, c); ip += c; return c;
        };
        binpazer::Reader r(iread, iseek);
        CHECK(r.has_index(), "index: discovered via footer");
        auto t1 = r.find(1);
        CHECK(t1.size() == 2 && t1[0] == offA && t1[1] == offC, "index: find type1");
        auto t2 = r.find(2);
        CHECK(t2.size() == 1 && t2[0] == offB, "index: find type2");
        auto blk = r.at(t1[0]);
        CHECK(blk.has_value() && blk->type_id == 1 && blk->payload_len == 4, "index: at header");
        auto p = r.read_payload(*blk);
        CHECK(p.size() == 4 && std::memcmp(p.data(), "aaaa", 4) == 0, "index: at payload");
    }

    // --- BTT capacity: >256 types must never be silently truncated ---
    {
        std::vector<uint8_t> tbuf;
        size_t tp = 0;
        auto twrite = [&](const void *b, size_t n) -> size_t {
            if (tp + n > tbuf.size()) tbuf.resize(tp + n);
            std::memcpy(tbuf.data() + tp, b, n); tp += n; return n;
        };
        auto tseek = [&](uint64_t o) -> bool {
            if (o > tbuf.size()) tbuf.resize(static_cast<size_t>(o));
            tp = static_cast<size_t>(o); return true;
        };
        std::vector<binpazer::TypeDef> many;
        for (int i = 1; i <= 300; ++i)
            many.push_back({static_cast<uint16_t>(i), g_mesh, "T" + std::to_string(i)});
        {
            binpazer::Writer w(twrite, writer, "demo", many, tseek);
            CHECK(w.block(1, 0, "x", 1) == binpazer::Status::Ok, "btt-cap: write block");
            CHECK(w.end() == binpazer::Status::Ok, "btt-cap: writer end");
        }
        tp = 0;
        auto tread = [&](void *b, size_t n) -> size_t {
            size_t av = tbuf.size() - tp, c = n < av ? n : av;
            std::memcpy(b, tbuf.data() + tp, c); tp += c; return c;
        };
        binpazer::Reader r(tread, tseek); // seekable: grows to fit and re-parses
        CHECK(r.ok(), "btt-cap: seekable reader ok");
        CHECK(r.types().size() == 301, "btt-cap: all 301 entries loaded");
        CHECK(r.types()[300].type_id == 300, "btt-cap: last entry intact");
        auto b = r.next();
        CHECK(b.has_value() && b->type_id == 1 && b->known, "btt-cap: walk still works");

        tp = 0;
        binpazer::Reader r2(tread); // no seek: loud error, not silent truncation
        CHECK(!r2.ok() && r2.status() == binpazer::Status::ErrCapacity,
              "btt-cap: no-seek reader reports ErrCapacity");
        CHECK(r2.types().empty(), "btt-cap: no partial table on error");
    }

    // --- large_mode (4096-byte units) ---
    {
        std::vector<uint8_t> lbuf;
        size_t lp = 0;
        auto lwrite = [&](const void *b, size_t n) -> size_t {
            if (lp + n > lbuf.size()) lbuf.resize(lp + n);
            std::memcpy(lbuf.data() + lp, b, n); lp += n; return n;
        };
        auto lseek = [&](uint64_t o) -> bool {
            if (o > lbuf.size()) lbuf.resize(static_cast<size_t>(o));
            lp = static_cast<size_t>(o); return true;
        };
        std::vector<uint8_t> big4(4096);
        for (size_t i = 0; i < big4.size(); ++i) big4[i] = static_cast<uint8_t>(i);
        std::vector<binpazer::TypeDef> d = {{1, g_mesh, "Big"}};
        {
            binpazer::Writer w(lwrite, writer, "demo", d, lseek);
            CHECK(w.block(1, binpazer::flag::LargeMode, big4) == binpazer::Status::Ok, "large: write 4096");
            CHECK(w.end() == binpazer::Status::Ok, "large: end");
        }
        lp = 0;
        auto lread = [&](void *b, size_t n) -> size_t {
            size_t av = lbuf.size() - lp, c = n < av ? n : av;
            std::memcpy(b, lbuf.data() + lp, c); lp += c; return c;
        };
        binpazer::Reader r(lread, lseek);
        auto b = r.next();
        CHECK(b.has_value() && (b->flags & binpazer::flag::LargeMode) && b->payload_len == 4096, "large: decoded 4096");
        auto p = r.read_payload(*b);
        CHECK(p.size() == 4096 && std::memcmp(p.data(), big4.data(), 4096) == 0, "large: payload");
    }

    // Like the C core, the wrapper carries no codec: it writes and parses the
    // envelope around bytes the caller encoded. This uses codec `stored`
    // (identity) and the GUID escape.
    {
        std::vector<uint8_t> cbuf;
        size_t cp = 0;
        auto cwrite = [&](const void *b, size_t n) -> size_t {
            if (cp + n > cbuf.size()) cbuf.resize(cp + n);
            std::memcpy(cbuf.data() + cp, b, n);
            cp += n;
            return n;
        };
        auto cseek = [&](uint64_t o) -> bool {
            if (o > cbuf.size()) cbuf.resize(static_cast<size_t>(o));
            cp = static_cast<size_t>(o);
            return true;
        };
        binpazer::Guid g_codec{{0xc0,0xde,0xc0,0xde,0,0,0,0,0,0,0,0,0,0,0,7}};
        std::vector<binpazer::TypeDef> d = {{2, g_note, "Note"}};
        const char *plain = "plain block";
        const char *comp  = "compressed, sort of";
        const char *gcod  = "guid-codec payload";
        {
            binpazer::Writer w(cwrite, writer, "demo", d, cseek);
            CHECK(w.block(2, 0, std::string_view(plain, 11)) == binpazer::Status::Ok, "compress: plain");
            CHECK(w.compressed_block(2, binpazer::flag::HasCrc, binpazer::codec::Stored,
                                     std::string_view(comp, 19), 19) == binpazer::Status::Ok,
                  "compress: stored block");
            CHECK(w.compressed_block(2, 0, binpazer::codec::ByGuid,
                                     std::string_view(gcod, 18), 18, &g_codec) == binpazer::Status::Ok,
                  "compress: guid-codec block");
            CHECK(w.end() == binpazer::Status::Ok, "compress: end");
        }

        cp = 0;
        auto cread = [&](void *b, size_t n) -> size_t {
            size_t av = cbuf.size() - cp, c = n < av ? n : av;
            std::memcpy(b, cbuf.data() + cp, c); cp += c; return c;
        };
        binpazer::Reader r(cread, cseek);

        auto b0 = r.next();
        CHECK(b0.has_value() && (b0->flags & binpazer::flag::Compressed) == 0, "compress: block 0 plain");
        auto p0 = r.read_payload(*b0);
        CHECK(p0.size() == 11 && std::memcmp(p0.data(), plain, 11) == 0, "compress: block 0 payload");

        auto b1 = r.next();
        CHECK(b1.has_value() && (b1->flags & binpazer::flag::Compressed) != 0, "compress: block 1 compressed");
        auto p1 = r.read_payload(*b1);
        auto c1 = binpazer::parse_compression(p1.data(), p1.size());
        CHECK(c1.has_value(), "compress: block 1 envelope parses");
        CHECK(c1 && c1->codec_id == binpazer::codec::Stored, "compress: block 1 codec");
        CHECK(c1 && c1->uncompressed_length == 19 && c1->stored_length == 19, "compress: block 1 lengths");
        CHECK(c1 && std::memcmp(p1.data() + c1->header_size, comp, 19) == 0, "compress: block 1 bytes");

        auto b2 = r.next();
        CHECK(b2.has_value(), "compress: block 2 present");
        auto p2 = r.read_payload(*b2);
        auto c2 = binpazer::parse_compression(p2.data(), p2.size());
        CHECK(c2 && c2->codec_id == binpazer::codec::ByGuid, "compress: block 2 codec is the GUID escape");
        CHECK(c2 && std::memcmp(c2->codec_guid.bytes, g_codec.bytes, 16) == 0, "compress: block 2 codec guid");
        CHECK(c2 && std::memcmp(p2.data() + c2->header_size, gcod, 18) == 0, "compress: block 2 bytes");

        // An envelope with codec one id is corruption, not an empty payload.
        uint8_t bad[BINPAZER_ENVELOPE_FIXED_SIZE] = {0};
        CHECK(!binpazer::parse_compression(bad, sizeof bad).has_value(), "compress: codec id 0 rejected");
    }

    // Group blocks: the wrapper parses nested blocks out of one payload, and
    // a child's payload aliases the buffer rather than being copied.
    {
        std::vector<uint8_t> g(32, 0);
        auto put16 = [&](size_t at, uint16_t v) { g[at] = uint8_t(v); g[at+1] = uint8_t(v >> 8); };
        auto put32 = [&](size_t at, uint32_t v) { for (int i = 0; i < 4; i++) g[at+i] = uint8_t(v >> (8*i)); };
        put16(0, 1); put16(2, 0); put32(4, 4); std::memcpy(g.data() + 8, "abcd", 4);
        put16(16, 2); put16(18, 0); put32(20, 2); std::memcpy(g.data() + 24, "xy", 2);

        bool ok = false;
        auto kids = binpazer::parse_group(g, &ok);
        CHECK(ok, "group: clean parse");
        CHECK(kids.size() == 2, "group: two children");
        CHECK(kids.size() == 2 && kids[0].type_id == 1 && kids[0].payload_len == 4, "group: child 0");
        CHECK(kids.size() == 2 && std::memcmp(kids[0].payload, "abcd", 4) == 0, "group: child 0 payload");
        CHECK(kids.size() == 2 && kids[1].type_id == 2 && kids[1].offset == 16, "group: child 1");
        CHECK(kids.size() == 2 && kids[0].payload == g.data() + 8, "group: payload aliases the buffer");
    }

    {
        std::vector<uint8_t> sbuf;
        size_t spos = 0;
        auto swrite = [&](const void *b, size_t n) -> size_t {
            if (spos + n > sbuf.size()) sbuf.resize(spos + n);
            std::memcpy(sbuf.data() + spos, b, n);
            spos += n;
            return n;
        };
        binpazer::Writer w(swrite, writer, "stream", defs, {}, BINPAZER_VERSION_MINOR_COMPRESSION);
        CHECK(w.ok(), "streaming: writer constructed");
        CHECK(w.compressed_block(1, 0, binpazer::codec::Stored, std::string_view("payload"), 7) ==
                  binpazer::Status::Ok,
              "streaming: compressed block with no seek callback");
        CHECK(w.end() == binpazer::Status::Ok, "streaming: end");
        CHECK(sbuf[14] == BINPAZER_VERSION_MINOR_COMPRESSION, "streaming: header declares 1.1");
    }

    // Streaming a payload in pieces smaller than the payload, and GUID text.
    {
        std::vector<uint8_t> sbuf;
        size_t spos = 0;
        auto swrite = [&](const void *b, size_t n) -> size_t {
            if (spos + n > sbuf.size()) sbuf.resize(spos + n);
            std::memcpy(sbuf.data() + spos, b, n);
            spos += n;
            return n;
        };
        auto sseek = [&](uint64_t o) -> bool {
            if (o > sbuf.size()) sbuf.resize(static_cast<size_t>(o));
            spos = static_cast<size_t>(o);
            return true;
        };
        std::vector<uint8_t> payload(1000);
        for (size_t i = 0; i < payload.size(); i++) payload[i] = static_cast<uint8_t>(i * 7 + 3);
        {
            binpazer::Writer w(swrite, writer, "stream", defs, sseek);
            w.block(1, binpazer::flag::HasCrc, payload);
            w.end();
        }
        spos = 0;
        binpazer::Reader r2([&](void *b, size_t n) -> size_t {
                                size_t avail = sbuf.size() - spos;
                                size_t c = n < avail ? n : avail;
                                std::memcpy(b, sbuf.data() + spos, c);
                                spos += c;
                                return c;
                            },
                            sseek);
        auto b = r2.next();
        CHECK(b.has_value(), "stream: block present");
        std::vector<uint8_t> got;
        size_t chunks = 0;
        binpazer::Status st = b ? r2.stream_payload(*b,
                                                    [&](const void *buf, size_t n) {
                                                        const auto *p8 = static_cast<const uint8_t *>(buf);
                                                        got.insert(got.end(), p8, p8 + n);
                                                        ++chunks;
                                                        return true;
                                                    },
                                                    64)
                                : binpazer::Status::ErrIo;
        CHECK(st == binpazer::Status::Ok, "stream: streamed ok");
        CHECK(got == payload, "stream: bytes reassembled");
        CHECK(chunks == (payload.size() + 63) / 64, "stream: chunked at the requested size");
    }

    {
        binpazer::Guid g{{0xaa,0xbb,0xcc,0xdd,0x00,0x01,0x02,0x03,0x04,0x05,0x06,0x07,0x08,0x09,0x0a,0x0b}};
        CHECK(binpazer::to_string(g) == "aabbccdd-0001-0203-0405-060708090a0b", "guid: canonical text");
        auto back = binpazer::parse_guid("aabbccdd-0001-0203-0405-060708090a0b");
        CHECK(back && std::memcmp(back->bytes, g.bytes, 16) == 0, "guid: round-trip");
        CHECK(!binpazer::parse_guid("nope").has_value(), "guid: junk rejected");
    }

    if (failures == 0) { std::printf("OK: all C++ round-trip checks passed\n"); return 0; }
    std::printf("%d FAILURE(S)\n", failures);
    return 1;
}
