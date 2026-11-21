// Tests for appendable files in the binpazer C++ wrapper: the append writer,
// reading while the file grows, and resume.
#include "binpazer.hpp"

#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

static int failures = 0;
#define CHECK(cond, msg) do { if (!(cond)) { std::printf("FAIL: %s (line %d)\n", msg, __LINE__); ++failures; } } while (0)

using binpazer::Status;

// A writer and a reader are processes with file positions, so each gets a
// cursor of its own over the same bytes.
struct MemFile {
    std::vector<uint8_t> data = std::vector<uint8_t>(4u << 20);
    size_t len = 0;
};

struct Cursor {
    MemFile *f;
    size_t pos = 0;
    explicit Cursor(MemFile &file) : f(&file) {}

    binpazer::WriteFn write() {
        return [this](const void *b, size_t n) -> size_t {
            if (pos + n > f->data.size()) return 0;
            if (pos > f->len) std::memset(f->data.data() + f->len, 0, pos - f->len);
            std::memcpy(f->data.data() + pos, b, n);
            pos += n;
            if (pos > f->len) f->len = pos;
            return n;
        };
    }
    binpazer::ReadFn read() {
        return [this](void *b, size_t n) -> size_t {
            size_t avail = pos < f->len ? f->len - pos : 0;
            size_t got = n < avail ? n : avail;
            std::memcpy(b, f->data.data() + pos, got);
            pos += got;
            return got;
        };
    }
    binpazer::SeekFn seek() {
        return [this](uint64_t off) -> bool {
            if (off > f->data.size()) return false;
            pos = static_cast<size_t>(off);
            return true;
        };
    }
};

static const uint16_t kTypeNote = 2;
static const binpazer::Guid kWriter{{0xaa,0xbb,0xcc,0xdd,0,0,0,0,0,0,0,0,0,0,0,1}};
static const binpazer::Guid kNote{{0xde,0xad,0xbe,0xef,0,0,0,0,0,0,0,0,0,0,0,0}};
static const char *const kRecords[3] = {"first record", "second record, a bit longer", ""};
static const std::vector<binpazer::TypeDef> kDefs = {{kTypeNote, kNote, "Note"}};

// Writes the sample: the same blocks as gen_appendable in gen.c.
static void write_sample(MemFile &f, bool end) {
    Cursor c(f);
    binpazer::Writer w(binpazer::appendable, c.write(), kWriter, "demo", kDefs, c.seek());
    CHECK(w.ok() && w.appendable(), "sample: begin");
    for (const char *rec : kRecords)
        CHECK(w.block(kTypeNote, 0, std::string_view(rec)) == Status::Ok, "sample: block");
    if (end) CHECK(w.end() == Status::Ok, "sample: end");
}

// Follows `f` from its first block. Counts the committed records, checks each
// payload, and returns the status that stopped the walk.
static Status follow(MemFile &f, int &records, const char *extra = nullptr, uint64_t *end_offset = nullptr) {
    Cursor c(f);
    binpazer::Reader r(c.read(), c.seek());
    records = 0;
    if (!r.ok()) return r.status();
    Status st;
    std::vector<uint8_t> pb(4096);
    for (;;) {
        binpazer::Block blk{};
        st = r.next_committed(blk, 4096);
        if (st != Status::Ok) break;
        if (blk.type_id == BINPAZER_TYPE_PADDING || blk.payload_len > pb.size()) {
            st = r.skip_payload(blk);
        } else {
            st = r.read_payload(blk, pb.data(), pb.size());
            if (st == Status::Ok && blk.type_id == kTypeNote) {
                const char *want = records < 3 ? kRecords[records] : extra;
                CHECK(want && blk.payload_len == std::strlen(want) &&
                          std::memcmp(pb.data(), want, blk.payload_len) == 0,
                      "follow: record payload");
                CHECK(blk.known, "follow: record type resolved");
                ++records;
            }
        }
        if (st != Status::Ok) break;
    }
    if (end_offset) *end_offset = r.offset();
    CHECK(r.ok() || static_cast<int>(st) < 0, "follow: pending and end leave no sticky error");
    return st;
}

// Offsets past each top-level block, by a plain walk.
static std::vector<uint64_t> block_ends(MemFile &f, uint64_t &first_block) {
    Cursor c(f);
    binpazer::Reader r(c.read(), c.seek());
    std::vector<uint64_t> ends;
    first_block = r.offset();
    while (auto blk = r.next()) {
        if (r.skip_payload(*blk) != Status::Ok) break;
        ends.push_back(r.offset());
    }
    return ends;
}

// The wrapper writes the same bytes as the C core, and so the shared fixture.
static void test_fixture_bytes() {
    MemFile cpp;
    write_sample(cpp, true);

    MemFile c;
    Cursor cc(c);
    binpazer::WriteFn wfn = cc.write();
    binpazer::SeekFn sfn = cc.seek();
    struct Io { binpazer::WriteFn *w; binpazer::SeekFn *s; } io{&wfn, &sfn};
    auto cwrite = [](void *ctx, const void *b, size_t n) -> size_t { return (*static_cast<Io *>(ctx)->w)(b, n); };
    auto cseek = [](void *ctx, uint64_t o) -> int { return (*static_cast<Io *>(ctx)->s)(o) ? 0 : -1; };
    binpazer_type_def defs[1] = {{kTypeNote, kNote, "Note"}};
    binpazer_writer w;
    binpazer_writer_begin_append(&w, cwrite, cseek, &io, &kWriter, "demo", defs, 1);
    for (const char *rec : kRecords) binpazer_writer_block(&w, kTypeNote, 0, rec, std::strlen(rec));
    CHECK(binpazer_writer_end(&w) == BINPAZER_OK, "fixture: C core writes the sample");

    CHECK(c.len == 288, "fixture: the sample is the size of sample-appendable.binpazer");
    CHECK(cpp.len == c.len && std::memcmp(cpp.data.data(), c.data.data(), c.len) == 0,
          "fixture: the wrapper writes the C core's bytes");
}

static void test_header_and_end() {
    MemFile f;
    write_sample(f, true);

    Cursor c(f);
    binpazer::Reader r(c.read(), c.seek());
    CHECK(r.ok(), "header: begin");
    CHECK(r.appendable(), "header: appendable flag");
    CHECK(r.version_minor() == BINPAZER_VERSION_MINOR_APPENDABLE, "header: declares the appendable version");
    CHECK(r.file_length() == f.len, "header: a seekable end back-patches file_length");
    CHECK(r.types().size() == 3 && r.types()[2].type_id == BINPAZER_TYPE_END &&
              std::memcmp(r.types()[2].guid.bytes, BINPAZER_GUID_END.bytes, 16) == 0,
          "header: End is in the type table");
    CHECK(!r.ended(), "header: not ended before the End block is read");

    int n = 0;
    CHECK(follow(f, n) == Status::End && n == 3, "end: follower stops at the End block");

    MemFile g;
    Cursor wc(g);
    binpazer::Writer w(binpazer::appendable, wc.write(), kWriter, "demo", kDefs, wc.seek());
    CHECK(w.write_index() != Status::Ok && w.ok(), "index: refused without poisoning the writer");
    CHECK(w.end() == Status::Ok, "end: end");
    size_t len = g.len;
    CHECK(w.block(kTypeNote, 0, std::string_view("late")) == Status::ErrEnded, "end: block after end");
    CHECK(w.compressed_block(kTypeNote, 0, binpazer::codec::Stored, std::string_view("late"), 4) == Status::ErrEnded,
          "end: compressed block after end");
    CHECK(w.end() == Status::Ok && g.len == len, "end: idempotent");

    // A plain walk marks the clean close.
    Cursor rc(g);
    binpazer::Reader pr(rc.read(), rc.seek());
    while (auto blk = pr.next()) {
        CHECK((blk->flags & binpazer::flag::HasCrc) != 0, "end: every block is sealed");
        if (pr.skip_payload(*blk) != Status::Ok) break;
    }
    CHECK(pr.ended() && pr.ok(), "end: the End block marks a clean close");
}

static void test_non_seekable() {
    MemFile f;
    Cursor c(f);
    binpazer::Writer w(binpazer::appendable, c.write(), kWriter, "demo", kDefs);
    CHECK(w.compressed_block(kTypeNote, 0, binpazer::codec::Stored, std::string_view("abc"), 3) == Status::Ok,
          "stream: compressed block needs no seek");
    CHECK(w.end() == Status::Ok, "stream: end");

    Cursor rc(f);
    binpazer::Reader r(rc.read());
    CHECK(r.file_length() == BINPAZER_FILE_LENGTH_UNKNOWN, "stream: file_length stays the sentinel");
    binpazer::Block blk{};
    CHECK(r.next_committed(blk) == Status::ErrNoSeek, "stream: next_committed needs a seek callback");

    Cursor sc(f);
    binpazer::Writer rw(binpazer::resume, sc.read(), sc.write(), {});
    CHECK(rw.status() == Status::ErrNoSeek, "stream: resume needs a seek callback");
}

// The property the format promises: at every length the file can have while
// it grows, a reader sees exactly the blocks that are wholly written.
static void test_every_prefix() {
    MemFile f;
    write_sample(f, true);
    uint64_t first = 0;
    std::vector<uint64_t> ends = block_ends(f, first);
    CHECK(ends.size() == 4, "prefix: three records and the End block");

    size_t full = f.len;
    for (size_t n = static_cast<size_t>(first); n <= full; ++n) {
        f.len = n;
        int want = 0, got = 0;
        for (uint64_t e : ends) if (e <= n) ++want;
        uint64_t at = 0;
        Status st = follow(f, got, nullptr, &at);
        if (want == 4) {
            CHECK(st == Status::End && got == 3, "prefix: whole file ends");
        } else {
            CHECK(st == Status::Pending, "prefix: a short file is pending, not torn");
            CHECK(got == want, "prefix: only whole blocks are surfaced");
            CHECK(at == (want ? ends[want - 1] : first), "prefix: a pending reader stays at the block start");
        }
    }
    f.len = full;
}

static void test_growing_file() {
    MemFile f;
    Cursor wc(f), rc(f);
    binpazer::Writer w(binpazer::appendable, wc.write(), kWriter, "demo", kDefs, wc.seek());
    CHECK(w.ok(), "grow: begin");

    binpazer::Reader r(rc.read(), rc.seek());
    CHECK(r.ok(), "grow: read begin");
    binpazer::Block blk{};
    uint64_t at = r.offset();
    CHECK(r.next_committed(blk) == Status::Pending, "grow: nothing yet");
    CHECK(r.next_committed(blk) == Status::Pending && r.offset() == at, "grow: pending again, reader has not moved");

    CHECK(w.block(kTypeNote, 0, std::string_view("one")) == Status::Ok, "grow: append");
    CHECK(r.next_committed(blk) == Status::Ok, "grow: block arrives");
    CHECK(blk.known && blk.payload_len == 3 && (blk.flags & binpazer::flag::HasCrc), "grow: header");
    auto p = r.read_payload(blk);
    CHECK(p.size() == 3 && std::memcmp(p.data(), "one", 3) == 0, "grow: payload");
    CHECK(r.next_committed(blk) == Status::Pending, "grow: caught up");

    // A payload larger than the scratch chunk is verified in pieces.
    std::vector<uint8_t> big(3 * 4096 + 5, 'x');
    CHECK(w.block(kTypeNote, 0, big) == Status::Ok, "grow: append big");
    CHECK(r.next_committed(blk, 4096) == Status::Ok && blk.payload_len == big.size(), "grow: big block arrives");
    CHECK(r.read_payload(blk) == big, "grow: big payload");

    CHECK(w.end() == Status::Ok, "grow: end");
    CHECK(r.next_committed(blk) == Status::Ok && blk.type_id == BINPAZER_TYPE_END, "grow: the End block is surfaced");
    CHECK(r.skip_payload(blk) == Status::Ok, "grow: skip End");
    CHECK(r.next_committed(blk) == Status::End && r.ended(), "grow: then the end");
}

static void test_torn() {
    MemFile f;
    write_sample(f, false);
    uint64_t first = 0;
    std::vector<uint64_t> ends = block_ends(f, first);
    CHECK(ends.size() == 3, "torn: three records");
    if (ends.size() != 3) return;
    int n = 0;
    uint64_t at = 0;

    f.data[first + 8] ^= 0xFF; // a payload byte of the first record
    CHECK(follow(f, n, nullptr, &at) == Status::Torn && n == 0 && at == first, "torn: bad seal stops the walk there");
    f.data[first + 8] ^= 0xFF;

    f.data[ends[1] + 2] &= static_cast<uint8_t>(~binpazer::flag::HasCrc); // the last record loses its seal
    CHECK(follow(f, n) == Status::Torn && n == 2, "torn: an unsealed block is not committed");
    f.data[ends[1] + 2] |= binpazer::flag::HasCrc;

    std::memset(f.data.data() + f.len, 0, 64); // zeroed space after the last block
    f.len += 64;
    CHECK(follow(f, n) == Status::Pending && n == 3, "torn: zeroed space is pending");
}

static void test_not_appendable() {
    MemFile f;
    {
        Cursor c(f);
        binpazer::Writer w(c.write(), kWriter, "demo", kDefs, c.seek());
        w.block(kTypeNote, binpazer::flag::HasCrc, std::string_view("x"));
        CHECK(w.end() == Status::Ok && !w.appendable(), "plain: write");
    }
    int n = 0;
    CHECK(follow(f, n) == Status::ErrNotAppendable, "plain: next_committed refuses");
    Cursor rc(f);
    binpazer::Writer w(binpazer::resume, rc.read(), rc.write(), rc.seek());
    CHECK(w.status() == Status::ErrNotAppendable && w.types().empty(), "plain: resume refuses");
}

static void check_resumed_types(const binpazer::Writer &w) {
    const auto &t = w.types();
    CHECK(t.size() == 3 && t[0].type_id == BINPAZER_TYPE_BLOCK_TYPE_TABLE && t[1].type_id == kTypeNote &&
              std::memcmp(t[1].guid.bytes, kNote.bytes, 16) == 0 && t[2].type_id == BINPAZER_TYPE_END,
          "resume: types() holds the file's type table");
}

static void test_resume() {
    MemFile f;
    write_sample(f, false);
    size_t clean = f.len;
    std::vector<uint8_t> saved(f.data.begin(), f.data.begin() + static_cast<std::ptrdiff_t>(clean));
    int n = 0;
    uint64_t at = 0;

    {
        // The cursor starts away from zero: resume reads the header from the start.
        Cursor c(f);
        c.pos = clean;
        binpazer::Writer w(binpazer::resume, c.read(), c.write(), c.seek());
        CHECK(w.ok() && w.appendable() && w.offset() == clean && f.len == clean, "resume: clean file");
        check_resumed_types(w);
        CHECK(w.block(kTypeNote, 0, std::string_view("after restart")) == Status::Ok, "resume: append");
        CHECK(w.write_index() != Status::Ok && w.ok(), "resume: index refused");
        CHECK(follow(f, n, "after restart") == Status::Pending && n == 4, "resume: old and new records");
    }

    // Torn tails of many lengths: a header that promises more than was written.
    static const size_t tails[] = {1, 7, 8, 9, 15, 16, 17, 100, 4096 + 3};
    for (size_t tail : tails) {
        std::memcpy(f.data.data(), saved.data(), clean);
        std::memset(f.data.data() + clean, 0xAB, tail);
        if (tail >= 8) {
            const uint8_t torn[8] = {kTypeNote, 0, binpazer::flag::HasCrc, 0, 0, 0, 0x10, 0};
            std::memcpy(f.data.data() + clean, torn, 8);
        }
        f.len = clean + tail;

        Cursor c(f);
        binpazer::Writer w(binpazer::resume, c.read(), c.write(), c.seek(), 4096);
        std::string what = "resume: torn tail of " + std::to_string(tail) + " bytes";
        CHECK(w.ok(), what.c_str());
        CHECK(w.offset() >= clean + tail && w.offset() % 8 == 0 && w.offset() == f.len,
              "resume: padding covers every stale byte");
        CHECK(std::memcmp(f.data.data(), saved.data(), clean) == 0, "resume: committed bytes are untouched");
        CHECK(w.block(kTypeNote, 0, std::string_view("recovered")) == Status::Ok, "resume: append after a torn tail");
        CHECK(follow(f, n, "recovered", &at) == Status::Pending && n == 4 && at == f.len,
              "resume: the file is whole again");
    }

    // An ended file does not resume.
    MemFile e;
    write_sample(e, true);
    Cursor ec(e);
    binpazer::Writer w(binpazer::resume, ec.read(), ec.write(), ec.seek());
    CHECK(w.status() == Status::ErrEnded && !w.ok() && w.types().empty(), "resume: ended file refuses");
}

int main() {
    test_fixture_bytes();
    test_header_and_end();
    test_non_seekable();
    test_every_prefix();
    test_growing_file();
    test_torn();
    test_not_appendable();
    test_resume();

    if (failures == 0) { std::printf("OK: all C++ appendable-file checks passed\n"); return 0; }
    std::printf("%d FAILURE(S)\n", failures);
    return 1;
}
