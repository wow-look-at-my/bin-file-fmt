// Benchmark program for the C++ binpazer wrapper: write, read, stream and find workloads.
#define _FILE_OFFSET_BITS 64
#define _POSIX_C_SOURCE 200809L

#include "binpazer.hpp"

#include <cinttypes>
#include <cstdarg>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <exception>
#include <new>
#include <string>
#include <sys/types.h>
#include <vector>

namespace {

constexpr uint16_t kTypePayload = 1;
constexpr size_t kIoBufSize = 65536;
constexpr size_t kStreamChunk = 65536;
constexpr uint16_t kCodecNone = BINPAZER_CODEC_INVALID;
constexpr uint32_t kFnvBasis = 2166136261u;

// Thrown for any failed step; main prints it and exits non-zero.
struct Failure {
	std::string msg;
};

[[noreturn]] void fail(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
void fail(const char *fmt, ...) {
	char buf[512];
	va_list ap;
	va_start(ap, fmt);
	std::vsnprintf(buf, sizeof buf, fmt, ap);
	va_end(ap);
	throw Failure{buf};
}

int status_code(binpazer::Status s) { return static_cast<int>(s); }

uint32_t mix(uint32_t h, uint8_t b) { return static_cast<uint32_t>((h ^ b) * 16777619u); }

uint32_t sum_parts(uint32_t h, uint64_t len, uint8_t first, uint8_t mid, uint8_t last) {
	uint32_t l = static_cast<uint32_t>(len);
	for (int i = 0; i < 4; i++) h = mix(h, static_cast<uint8_t>(l >> (8 * i)));
	if (len > 0) {
		h = mix(h, first);
		h = mix(h, mid);
		h = mix(h, last);
	}
	return h;
}

uint32_t sum_block(uint32_t h, const uint8_t *p, uint64_t len) {
	if (len == 0) return sum_parts(h, 0, 0, 0, 0);
	return sum_parts(h, len, p[0], p[len >> 1], p[len - 1]);
}

void print_result(const char *op, uint64_t blocks, uint64_t bytes, uint32_t h) {
	if (std::printf("{\"op\":\"%s\",\"blocks\":%" PRIu64 ",\"bytes\":%" PRIu64 ",\"checksum\":\"%08" PRIx32 "\"}\n",
			op, blocks, bytes, h) < 0 ||
	    std::fflush(stdout) != 0)
		fail("writing the result line");
}

class File {
public:
	File(const char *path, const char *mode) : path_(path) {
		f_ = std::fopen(path, mode);
		if (!f_) fail("cannot open %s", path);
		if (std::setvbuf(f_, nullptr, _IOFBF, kIoBufSize) != 0) {
			std::fclose(f_);
			f_ = nullptr;
			fail("setvbuf on %s", path);
		}
	}
	~File() {
		if (f_) std::fclose(f_);
	}
	File(const File &) = delete;
	File &operator=(const File &) = delete;

	FILE *get() const { return f_; }
	size_t read(void *buf, size_t n) { return std::fread(buf, 1, n, f_); }
	size_t write(const void *buf, size_t n) { return std::fwrite(buf, 1, n, f_); }
	bool seek(uint64_t off) {
		if (off > static_cast<uint64_t>(INT64_MAX)) return false;
		return fseeko(f_, static_cast<off_t>(off), SEEK_SET) == 0;
	}
	void close() {
		FILE *f = f_;
		f_ = nullptr;
		if (std::fclose(f) != 0) fail("closing %s", path_.c_str());
	}

private:
	FILE *f_ = nullptr;
	std::string path_;
};

uint64_t parse_count(const char *s, const char *what) {
	char *end = nullptr;
	if (s[0] < '0' || s[0] > '9') fail("bad %s: %s", what, s);
	unsigned long long v = std::strtoull(s, &end, 10);
	if (*end != '\0') fail("bad %s: %s", what, s);
	return static_cast<uint64_t>(v);
}

uint16_t parse_codec(const char *s) {
	static const struct {
		const char *name;
		uint16_t id;
	} codecs[] = {
		{ "none", kCodecNone },
		{ "stored", binpazer::codec::Stored },
		{ "deflate", binpazer::codec::Deflate },
		{ "zstd", binpazer::codec::Zstd },
		{ "lz4", binpazer::codec::Lz4 },
	};
	for (const auto &c : codecs)
		if (std::strcmp(s, c.name) == 0) return c.id;
	fail("unknown codec: %s", s);
}

std::vector<uint8_t> load_payload(const char *path) {
	File f(path, "rb");
	off_t size;
	if (fseeko(f.get(), 0, SEEK_END) != 0 || (size = ftello(f.get())) < 0 || fseeko(f.get(), 0, SEEK_SET) != 0)
		fail("sizing %s", path);
	std::vector<uint8_t> p(static_cast<size_t>(size));
	if (f.read(p.data(), p.size()) != p.size()) fail("reading %s", path);
	return p;
}

void cmd_write(const char *out_path, const char *payload_path, const char *blocks_s, const char *codec_s) {
	uint64_t blocks = parse_count(blocks_s, "block count");
	uint16_t codec = parse_codec(codec_s);
	const std::vector<uint8_t> payload = load_payload(payload_path);
	const uint64_t plen = payload.size();

	File f(out_path, "wb");
	binpazer::Guid wg{ { 0xB0 } };
	std::vector<binpazer::TypeDef> defs{ { kTypePayload, binpazer::Guid{ { 0x11 } }, "Payload" } };
	{
		binpazer::Writer w([&](const void *b, size_t n) { return f.write(b, n); }, wg, "bench", defs,
				   [&](uint64_t o) { return f.seek(o); });
		if (!w.ok()) fail("writer begin (status %d)", status_code(w.status()));
		for (uint64_t i = 0; i < blocks; i++) {
			binpazer::Status st = codec == kCodecNone
				? w.block(kTypePayload, binpazer::flag::HasCrc, payload.data(), plen)
				: w.compress_block(kTypePayload, binpazer::flag::HasCrc, codec, payload.data(), plen);
			if (st != binpazer::Status::Ok) fail("writing block %" PRIu64 " (status %d)", i, status_code(st));
		}
		binpazer::Status st = w.write_index();
		if (st != binpazer::Status::Ok) fail("writing the block index (status %d)", status_code(st));
		if ((st = w.end()) != binpazer::Status::Ok) fail("ending the file (status %d)", status_code(st));
	}
	f.close();
	print_result("write", blocks, blocks * plen, 0);
}

// A wrapper Reader over a File, begun with a seek callback.
struct Input {
	File file;
	binpazer::Reader r;

	explicit Input(const char *path)
	    : file(path, "rb"), r([this](void *b, size_t n) { return file.read(b, n); },
				  [this](uint64_t o) { return file.seek(o); }) {
		if (!r.ok()) fail("reader begin on %s (status %d)", path, status_code(r.status()));
	}

	// Returns the next block, or false at a clean end; fails on a read error.
	bool next(binpazer::Block &b) {
		auto blk = r.next();
		if (blk) {
			b = *blk;
			return true;
		}
		if (r.status() != binpazer::Status::Ok) fail("reading a block header (status %d)", status_code(r.status()));
		uint64_t end = r.file_length();
		if (end != BINPAZER_FILE_LENGTH_UNKNOWN && r.has_index()) end -= 16;
		if (end != BINPAZER_FILE_LENGTH_UNKNOWN && r.offset() < end)
			fail("block walk ended at offset %" PRIu64 ", before the stream end %" PRIu64, r.offset(), end);
		return false;
	}

	void skip(const binpazer::Block &b) {
		binpazer::Status st = r.skip_payload(b);
		if (st != binpazer::Status::Ok) fail("skipping a block (status %d)", status_code(st));
	}

	// Returns the logical bytes of a plain or compressed block.
	std::vector<uint8_t> read_decoded(const binpazer::Block &b) {
		auto p = r.read_payload_decompressed(b);
		if (!p)
			fail("reading the payload at offset %" PRIu64 " (status %d)", b.payload_offset, status_code(r.status()));
		return std::move(*p);
	}
};

void cmd_read(const char *path) {
	Input in(path);
	uint64_t blocks = 0, bytes = 0;
	uint32_t h = kFnvBasis;
	binpazer::Block b{};
	while (in.next(b)) {
		if (b.type_id != kTypePayload) {
			in.skip(b);
			continue;
		}
		const std::vector<uint8_t> p = in.read_decoded(b);
		h = sum_block(h, p.data(), p.size());
		blocks++;
		bytes += p.size();
	}
	print_result("read", blocks, bytes, h);
}

void cmd_stream(const char *path) {
	Input in(path);
	uint64_t blocks = 0, bytes = 0;
	uint32_t h = kFnvBasis;
	binpazer::Block b{};
	while (in.next(b)) {
		if (b.type_id != kTypePayload) {
			in.skip(b);
			continue;
		}
		const uint64_t len = b.payload_len, mid = len >> 1;
		uint64_t pos = 0;
		uint8_t first = 0, mid_b = 0, last = 0;
		binpazer::Reader::SinkFn sink = [&](const void *buf, size_t n) {
			if (n == 0) return true;
			const uint8_t *p = static_cast<const uint8_t *>(buf);
			if (pos == 0) first = p[0];
			if (mid >= pos && mid < pos + n) mid_b = p[mid - pos];
			last = p[n - 1];
			pos += n;
			return true;
		};
		binpazer::Status st = in.r.stream_payload(b, sink, kStreamChunk);
		if (st != binpazer::Status::Ok)
			fail("streaming the payload at offset %" PRIu64 " (status %d)", b.payload_offset, status_code(st));
		h = sum_parts(h, len, first, mid_b, last);
		blocks++;
		bytes += len;
	}
	print_result("stream", blocks, bytes, h);
}

void cmd_find(const char *path, const char *lookups_s) {
	uint64_t lookups = parse_count(lookups_s, "lookup count");
	Input in(path);
	std::vector<uint64_t> offsets = in.r.find(kTypePayload);
	if (!in.r.ok()) fail("find (status %d)", status_code(in.r.status()));
	if (offsets.empty()) fail("find returned no type-%u blocks", kTypePayload);
	uint64_t bytes = 0;
	uint32_t h = kFnvBasis;
	for (uint64_t k = 0; k < lookups; k++) {
		uint32_t mixed = static_cast<uint32_t>(k) * 2654435761u;
		uint64_t off = offsets[static_cast<size_t>(mixed % offsets.size())];
		auto b = in.r.at(off);
		if (!b) fail("seeking to the block at offset %" PRIu64 " (status %d)", off, status_code(in.r.status()));
		if (b->type_id != kTypePayload)
			fail("block at offset %" PRIu64 " has type %u, not %u", off, b->type_id, kTypePayload);
		const std::vector<uint8_t> p = in.read_decoded(*b);
		h = sum_block(h, p.data(), p.size());
		bytes += p.size();
	}
	print_result("find", lookups, bytes, h);
}

int run(int argc, char **argv) {
	if (argc == 6 && std::strcmp(argv[1], "write") == 0) {
		cmd_write(argv[2], argv[3], argv[4], argv[5]);
		return 0;
	}
	if (argc == 3 && std::strcmp(argv[1], "read") == 0) {
		cmd_read(argv[2]);
		return 0;
	}
	if (argc == 3 && std::strcmp(argv[1], "stream") == 0) {
		cmd_stream(argv[2]);
		return 0;
	}
	if (argc == 4 && std::strcmp(argv[1], "find") == 0) {
		cmd_find(argv[2], argv[3]);
		return 0;
	}
	std::fputs("usage: bench_cpp write <out-file> <payload-file> <blocks> <codec>\n"
		   "       bench_cpp read <in-file>\n"
		   "       bench_cpp stream <in-file>\n"
		   "       bench_cpp find <in-file> <lookups>\n"
		   "codec: none stored deflate zstd lz4\n",
		   stderr);
	return 2;
}

} // namespace

int main(int argc, char **argv) {
	try {
		return run(argc, argv);
	} catch (const Failure &e) {
		std::fprintf(stderr, "bench_cpp: %s\n", e.msg.c_str());
	} catch (const std::bad_alloc &) {
		std::fprintf(stderr, "bench_cpp: out of memory\n");
	} catch (const std::exception &e) {
		std::fprintf(stderr, "bench_cpp: %s\n", e.what());
	}
	return 1;
}
