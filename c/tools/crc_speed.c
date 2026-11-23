/* Prints the throughput of binpazer_crc32c at several buffer sizes. */
#define _POSIX_C_SOURCE 200809L
#include "../binpazer.h"
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

static double now(void) {
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

int main(void) {
    static const size_t sizes[] = { 64, 256, 1024, 4096, 65536, 1 << 20, 8 << 20 };
    size_t max = sizes[sizeof sizes / sizeof sizes[0] - 1];
    uint8_t *buf = malloc(max);
    if (!buf) return 1;
    uint32_t x = 0x9E3779B9u;
    for (size_t i = 0; i < max; i++) {
        x ^= x << 13; x ^= x >> 17; x ^= x << 5;
        buf[i] = (uint8_t)x;
    }
    uint32_t sink = 0;
    for (size_t s = 0; s < sizeof sizes / sizeof sizes[0]; s++) {
        size_t n = sizes[s];
        size_t reps = ((size_t)1 << 30) / n;
        double best = 0;
        for (int round = 0; round < 3; round++) {
            double t0 = now();
            for (size_t r = 0; r < reps; r++) sink = binpazer_crc32c(sink, buf, n);
            double rate = (double)n * (double)reps / (now() - t0) / (double)(1 << 30);
            if (rate > best) best = rate;
        }
        printf("%8zu bytes  %7.2f GiB/s\n", n, best);
    }
    printf("checksum %08x\n", (unsigned)sink);
    free(buf);
    return 0;
}
