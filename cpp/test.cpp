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
    const char *big  = "0123456789ABCDEF01234567";  // 24 (multiple of 4)

    {
        binpazer::Writer w(write, writer, "demo", defs, seek);
        CHECK(w.ok(), "writer constructed");
        CHECK(w.block(1, binpazer::flag::Critical | binpazer::flag::HasCrc, mesh, 16) == binpazer::Status::Ok, "write mesh");
        CHECK(w.block(2, 0, note, 12) == binpazer::Status::Ok, "write note");
        CHECK(w.block(1, binpazer::flag::LargeMode, big, 24) == binpazer::Status::Ok, "write large");
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
    CHECK(b3.has_value() && (b3->flags & binpazer::flag::LargeMode), "block3 large");
    CHECK(b3->payload_len == 24, "block3 len decoded");
    auto p3 = r.read_payload(*b3);
    CHECK(p3.size() == 24 && std::memcmp(p3.data(), big, 24) == 0, "block3 payload");

    auto b4 = r.next();
    CHECK(!b4.has_value() && r.ok(), "clean end");

    if (failures == 0) { std::printf("OK: all C++ round-trip checks passed\n"); return 0; }
    std::printf("%d FAILURE(S)\n", failures);
    return 1;
}
