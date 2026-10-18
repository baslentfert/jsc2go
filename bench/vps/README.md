# Checking jsc2go on another machine

`run.sh` checks jsc2go on a machine of your own (a Linux server with
Debian, CentOS or Ubuntu, x86-64 or arm64; or Windows with Git Bash): that
the JIT works there, that the test suites pass, and how fast it is compared
with the reference results of an Apple Silicon Mac and with the other Go
engines.

## Running

Needs git, Go (version in `go.mod`), and for `full` internet access (the
other engines are downloaded).

    git clone <this repository> jsc2go
    cd jsc2go
    sh bench/vps/run.sh            # correctness + JetStream2, about 45 minutes
    sh bench/vps/run.sh quick      # correctness only, about 15 minutes
    sh bench/vps/run.sh full       # also the other engines, octane-zlib and float-mm.c: a few hours

The first time, the script clones test262 and the relevant part of WebKit
(`scripts/setup.sh`, a few minutes). Everything goes to
`bench/vps/out/<host>-<date>/`; `summary.txt` gives PASS or FAIL per step.
On a server, run it in `tmux` or with `nohup`.

## What is checked

| step | what | expected |
|---|---|---|
| `build` | builds everything, `go vet`, builds `bench/bin/jsrun` | PASS |
| `unit-tests` | `go test ./...` | PASS |
| `jit-masm` | every operation of the macro assembler (arm64 or x86-64) against a reference in Go: register aliasing, NaN, ±0, ∞, overflow, all element types | PASS |
| `jit-fuzz` | 8000 random programs, the same output with and without the JIT | PASS; the log says in how many programs generated code actually ran |
| `regex-fuzz` | 20000 random regular expressions with and without prefilters | PASS |
| `jit-active` | does the machine give the JIT executable memory? | PASS. FAIL means jsc2go only uses its interpreter, for instance because SELinux forbids `execmem` (see below) |
| `leaks` | memory leak probes; the live heap after each loop | PASS |
| `test262` | the whole suite with JSC's configuration | 85615 / 85664, no unexpected failures |
| `test262-jit-everything` | the same with `JSC2GO_JITTHRESHOLD=1` (nearly everything compiled) | the same |
| `micro-jit`, `micro-interpreter` | small benchmarks with the JIT on and off (`micro/*.js`) | the same results |
| `jetstream-jit`, `jetstream-nojit` | the JetStream2 benchmarks, 10 iterations | no ERROR lines |
| `jetstream-others` (full) | goja, Sobek, QuickJS and Ramune on this machine | Ramune fails on about 22 benchmarks (its own errors) |
| `jetstream-slow-*` (full) | octane-zlib and float-mm.c, 3 iterations | |

In both test262 steps every unexpected failure is run again on its own;
only what still fails counts. The `staging/sm/Date/dst-offset-caching-*`
tests collect garbage thousands of times and can time out on a busy
machine (about 3 s alone); the log lists them as `flaky (passes alone)`.

Then `compare` (`comparison.txt`) puts this machine's score per benchmark
next to the reference, with the geomeans and (with `full`) the standing
against the other engines on this machine.

## Reference

`baseline/mac-arm64.json` holds the scores of jsc2go with and without its
JIT and those of the other engines on an Apple M-series Mac (macOS, 7
October 2026, before the inlining and the optimizing tier; current numbers
are in [PERFORMANCE.md](../../PERFORMANCE.md)). Another machine is faster
or slower; what matters is (1) that every step passes, (2) that the JIT
gives about the same gain as on the reference (the `jit/no` column of
`comparison.txt`), and (3) the standing against the other engines on the
same machine (`full`).

## If `jit-active` fails (SELinux)

On CentOS, RHEL, Alma and Rocky Linux SELinux is usually enforcing.
Ordinary programs may make memory executable, but policies for services
(or the boolean `deny_execmem`) can forbid it. jsc2go then keeps running
with its interpreter. To check:

    getenforce
    getsebool deny_execmem
    JSC2GO_JITLOG=1 bench/bin/jsrun bench/vps/micro/jit.js 2>&1 | grep 'jit:'
