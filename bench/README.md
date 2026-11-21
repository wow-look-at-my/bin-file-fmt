# binpazer benchmark

The benchmark runs the same workloads through the C, C++, Go and TypeScript implementations. It measures CPU time and peak memory of each run, checks that the implementations agree, and writes a comparison report.

## What it measures

Each timed run is one process started under `bench/build/measure`. CPU time is user + sys of the whole process, including runtime start-up. Peak memory is the kernel's `ru_maxrss`. Throughput is logical bytes divided by the median wall time.

## Workloads

| Workload | Payload | Blocks | Codec | Timed operations |
|---|---:|---:|---|---|
| small | 256 B | 200000 | none | write, read, find |
| medium | 64 KiB | 2000 | none | write, read, stream |
| large | 8 MiB | 24 | none | write, read, stream |
| stored | 1 MiB | 64 | stored | write, read |
| deflate | 1 MiB | 64 | deflate | write, read |
| zstd | 1 MiB | 64 | zstd | write, read |
| lz4 | 1 MiB | 64 | lz4 | write, read |

The driver generates each payload deterministically. It is roughly half pseudo-random bytes and half repeated text.

Every implementation writes its own file. For codec none and stored, the files must be byte-identical. For the other codecs, every implementation reads every other implementation's file and must report the same result. The timed read, stream and find runs all use the file that the reference implementation (C, when selected) wrote. Every implementation must print the same result line. Any disagreement, non-zero exit or incorrect count stops the run with an error and no report.

## Build and run

```sh
make -C bench
(cd go && go-toolchain)
(cd ts && ts0 build)
node bench/run.ts
```

A quick run divides the block counts by 8, does one repeat and writes its outputs into `bench/work/`, so it never overwrites the committed results:

```sh
node bench/run.ts --quick
node bench/run.ts --quick --impls c,cpp --only small,zstd
```

To add CPU hot-spot tables, build the gprof programs and pass `--profile`:

```sh
make -C bench prof
node bench/run.ts --profile
```

`make -C bench prof` builds `bench_c` and `bench_cpp` into `bench/build-prof/` with `-pg` and a static link, so gprof also samples libc and the codec libraries. After the timed runs of a workload, `--profile` runs each implementation. This happens once more for small/read, medium/write and medium/read. This also happens lz4/write and lz4/read: C and C++ under gprof, Go with `--cpuprofile` (pprof), TypeScript with `node --cpu-prof`. These profiled runs always use the full block counts, also with `--quick`, because the samplers only take samples a second. Each profiled run must print the same result line as the reference.

`--render` runs nothing. It rebuilds the report from an existing results file, for a change to the report layout:

```sh
node bench/run.ts --render
```

Run `node bench/run.ts --help` for every option. To check the driver itself:

```sh
cd bench && ts0 test
```

## Reading the report

`bench/RESULTS.md` has tables with one row per workload and operation: CPU time, peak memory, and wall time with throughput. A value in parentheses is the ratio to the reference implementation, so `0.412 (1.8x)` means 1.8 times the reference. A "Written file size" table shows the codec workloads. Compressed sizes can differ because encoder output is implementation-defined. With `--profile`, the section "Where the CPU time goes" has one table per workload and operation: implementations as columns, the top functions by self time as rows. Each comes with its share of that run's sampled CPU time.

`bench/results.json` holds the same data plus the raw stats of every repeat and the full profile summaries.
