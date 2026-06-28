# binpazer C reference build + test
CC      ?= cc
CFLAGS  ?= -std=c11 -Wall -Wextra -Wpedantic -O2

.PHONY: test clean
test: test_c
	./test_c

test_c: binpazer.c binpazer.h test.c
	$(CC) $(CFLAGS) binpazer.c test.c -o $@

clean:
	rm -f test_c
