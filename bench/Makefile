# Builds the C and C++ bench programs and the measure helper into $(BUILD)/.
CC       ?= cc
CXX      ?= c++
CFLAGS   ?= -std=c11 -Wall -Wextra -Wpedantic -O2
CXXFLAGS ?= -std=c++17 -Wall -Wextra -O2
BUILD    ?= build
EXTRA_CFLAGS  ?=
EXTRA_LDFLAGS ?=
CODEC_LIBS ?= -lz -lzstd -llz4

LIB_SRC = ../c/binpazer.c ../c/binpazer_codec.c
LIB_HDR = ../c/binpazer.h ../c/binpazer_codec.h

.PHONY: all clean prof
all: $(BUILD)/bench_c $(BUILD)/bench_cpp $(BUILD)/measure

# gprof builds: the static link puts libc. The codec libraries in the histogram, and -fno-inline keeps hot helpers such as binpazer_crc32c visible.
prof:
	$(MAKE) BUILD=build-prof EXTRA_CFLAGS="-pg -fno-inline" EXTRA_LDFLAGS="-pg -static" build-prof/bench_c build-prof/bench_cpp

$(BUILD):
	mkdir -p $@

$(BUILD)/bench_c: bench.c $(LIB_SRC) $(LIB_HDR) | $(BUILD)
	$(CC) $(CFLAGS) $(EXTRA_CFLAGS) -I../c bench.c $(LIB_SRC) -o $@ $(EXTRA_LDFLAGS) $(CODEC_LIBS)

$(BUILD)/binpazer.o: ../c/binpazer.c $(LIB_HDR) | $(BUILD)
	$(CC) $(CFLAGS) $(EXTRA_CFLAGS) -c $< -o $@

$(BUILD)/binpazer_codec.o: ../c/binpazer_codec.c $(LIB_HDR) | $(BUILD)
	$(CC) $(CFLAGS) $(EXTRA_CFLAGS) -c $< -o $@

$(BUILD)/bench_cpp: bench.cpp ../cpp/binpazer.hpp $(BUILD)/binpazer.o $(BUILD)/binpazer_codec.o | $(BUILD)
	$(CXX) $(CXXFLAGS) $(EXTRA_CFLAGS) -I../c -I../cpp bench.cpp $(BUILD)/binpazer.o $(BUILD)/binpazer_codec.o \
		-o $@ $(EXTRA_LDFLAGS) $(CODEC_LIBS)

$(BUILD)/measure: measure.c | $(BUILD)
	$(CC) $(CFLAGS) $(EXTRA_CFLAGS) measure.c -o $@ $(EXTRA_LDFLAGS)

clean:
	rm -rf $(BUILD)
