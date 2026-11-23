# binpazer C reference build + test
CC      ?= cc
CFLAGS  ?= -std=c11 -Wall -Wextra -Wpedantic -O2
# The codec layer's libraries. The core (binpazer.c) links none of them, and
# test_c proves it still builds and passes with nothing but libc.
CODEC_LIBS ?= -lz -lzstd -llz4

.PHONY: test test-core speed clean
test: test-core test_codec
	./test_codec

# The core tests link nothing but libc, so they run on a machine with no codec library.
test-core: test_c test_c_tier0 test_c_tier1 test_c_tier2 test_c_tier3 test_c_lanes8 test_c_lanes12 test_append
	./test_c
	./test_c_tier0
	./test_c_tier1
	./test_c_tier2
	./test_c_tier3
	./test_c_lanes8
	./test_c_lanes12
	./test_append

# The arm64 folding loop with other accumulator counts. The tier is explicit because arm64 Linux stops a tier lower by default. On other CPUs the lane define changes nothing.
test_c_lanes%: binpazer.c binpazer.h test.c
	$(CC) $(CFLAGS) -DBINPAZER_CRC_TIER=3 -DCRC_NEON_LANES=$* binpazer.c test.c -o $@

# CRC-32C throughput of each tier on this machine, and of each accumulator count of the arm64 folding loop.
SPEED = crc_speed_tier0 crc_speed_tier1 crc_speed_tier2 crc_speed_tier3 crc_speed_lanes8 crc_speed_lanes12
speed: $(SPEED)
	@for p in $(SPEED); do echo "== $$p"; ./$$p || exit 1; done

crc_speed_tier%: binpazer.c binpazer.h tools/crc_speed.c
	$(CC) $(CFLAGS) -DBINPAZER_CRC_TIER=$* binpazer.c tools/crc_speed.c -o $@

crc_speed_lanes%: binpazer.c binpazer.h tools/crc_speed.c
	$(CC) $(CFLAGS) -DBINPAZER_CRC_TIER=3 -DCRC_NEON_LANES=$* binpazer.c tools/crc_speed.c -o $@

test_c: binpazer.c binpazer.h test.c
	$(CC) $(CFLAGS) binpazer.c test.c -o $@

# The same tests with the CRC capped at a lower tier, which the best CPU path never runs otherwise.
test_c_tier%: binpazer.c binpazer.h test.c
	$(CC) $(CFLAGS) -DBINPAZER_CRC_TIER=$* binpazer.c test.c -o $@

test_append: binpazer.c binpazer.h test_append.c
	$(CC) $(CFLAGS) binpazer.c test_append.c -o $@

test_codec: binpazer.c binpazer.h binpazer_codec.c binpazer_codec.h test_codec.c
	$(CC) $(CFLAGS) binpazer.c binpazer_codec.c test_codec.c -o $@ $(CODEC_LIBS)

clean:
	rm -f test_c test_c_tier* test_c_lanes* crc_speed_* test_append test_codec
