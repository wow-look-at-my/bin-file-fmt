# binpazer C reference build + test
CC      ?= cc
CFLAGS  ?= -std=c11 -Wall -Wextra -Wpedantic -O2
# The codec layer's libraries. The core (binpazer.c) links none of them, and
# test_c proves it still builds and passes with nothing but libc.
CODEC_LIBS ?= -lz -lzstd -llz4

.PHONY: test clean
test: test_c test_c_tier0 test_c_tier1 test_c_tier2 test_append test_codec
	./test_c
	./test_c_tier0
	./test_c_tier1
	./test_c_tier2
	./test_append
	./test_codec

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
	rm -f test_c test_c_tier* test_append test_codec
