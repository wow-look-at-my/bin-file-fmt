# binpazer C++ wrapper build + test (links the C core)
CXX      ?= c++
CXXFLAGS ?= -std=c++17 -Wall -Wextra -O2
CORE     := ../c/binpazer.c
INCLUDE  := -I../c

.PHONY: test clean
test: test_cpp
	./test_cpp

test_cpp: binpazer.hpp test.cpp $(CORE) ../c/binpazer.h
	$(CXX) $(CXXFLAGS) $(INCLUDE) test.cpp $(CORE) -o $@

clean:
	rm -f test_cpp
