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

    const char *mesh = "vertices-go-here";          // 16
    const char *note = "hello, file!";              // 12
    const char *big  = "0123456789ABCDEF01234567";  // 24

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

    if (failures == 0) { std::printf("OK: all C++ round-trip checks passed\n"); return 0; }
    std::printf("%d FAILURE(S)\n", failures);
    return 1;
}
