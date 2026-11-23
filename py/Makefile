# Builds the native CRC-32C library from the C reference and runs the test suite of the Python implementation.
PYTHON ?= python3
CC     ?= cc
UNAME  := $(shell uname -s)
EXT    := $(if $(filter Darwin,$(UNAME)),dylib,so)
NATIVE := binpazer/_binpazer_native.$(EXT)

.PHONY: native test clean
native: $(NATIVE)

# The flags match build_command in binpazer/_native.py, which builds the same file when an import finds it absent.
$(NATIVE): ../c/binpazer.c ../c/binpazer.h
	$(CC) -std=c11 -O2 -shared -fPIC ../c/binpazer.c -o $@

clean:
	rm -f binpazer/_binpazer_native.*

test: native
	$(PYTHON) -m unittest discover -s tests -t . $(TESTFLAGS)
