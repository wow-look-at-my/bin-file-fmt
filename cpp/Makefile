# binpazer C++ wrapper build + test (links the C core)
CXX      ?= c++
CXXFLAGS ?= -std=c++17 -Wall -Wextra -O2
CORE     := ../c/binpazer.c
CODEC    := ../c/binpazer_codec.c
INCLUDE  := -I../c
# Only the codec test needs these; test_cpp proves the wrapper still links
# against the core alone.
CODEC_LIBS ?= -lz -lzstd -llz4

.PHONY: test clean
test: test_cpp test_cpp_append test_cpp_codec
	./test_cpp
	./test_cpp_append
	./test_cpp_codec

test_cpp: binpazer.hpp test.cpp $(CORE) ../c/binpazer.h
	$(CXX) $(CXXFLAGS) $(INCLUDE) test.cpp $(CORE) -o $@

test_cpp_append: binpazer.hpp test_append.cpp $(CORE) ../c/binpazer.h
	$(CXX) $(CXXFLAGS) $(INCLUDE) test_append.cpp $(CORE) -o $@

test_cpp_codec: binpazer.hpp test_codec.cpp $(CORE) $(CODEC) ../c/binpazer.h ../c/binpazer_codec.h
	$(CXX) $(CXXFLAGS) $(INCLUDE) test_codec.cpp $(CORE) $(CODEC) -o $@ $(CODEC_LIBS)

clean:
	rm -f test_cpp test_cpp_append test_cpp_codec
