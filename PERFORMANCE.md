# Performance

## JetStream2 against the other Go engines

JetStream2's JavaScript benchmarks (the 56 that need no WebAssembly), run
by `bench/jetstream.sh` with 3 iterations per benchmark, on an x86-64
Windows PC, October 2026. Engines: jsc2go (with its JIT), modernc.org/quickjs
v0.25.0, goja and grafana/sobek (October 2026 snapshots, see
`bench/engines/go.mod`) and Ramune v0.22.0. jsc2go was measured on 18
October, the others on 17 October with the same binaries on the same
machine.

**Higher is better.** The score is JetStream2's (5000 divided by the run
time in milliseconds, combining the first, the worst and the average
iteration): twice the score is twice as fast. Scores differ in size from
benchmark to benchmark, so compare within a row. The last column is jsc2go
divided by the best other engine; "error" means the engine did not finish
the benchmark (see below).

| Benchmark | jsc2go | QuickJS | goja | Sobek | Ramune | jsc2go / best other |
|---|---:|---:|---:|---:|---:|---:|
| 3d-cube-SP | 86.78 | error | error | error | error | – |
| 3d-raytrace-SP | 61.95 | 6.61 | 2.72 | 3.09 | error | 9.4× |
| acorn-wtb | 3.97 | 0.72 | 0.68 | 0.67 | error | 5.5× |
| ai-astar | 79.81 | 9.29 | 8.49 | 8.39 | 7.83 | 8.6× |
| Air | 43.88 | 14.84 | 12.08 | 12.62 | 15.95 | 2.8× |
| async-fs | 20.45 | error | error | error | 2.05 | 10.0× |
| Babylon | 49.30 | 16.29 | 13.49 | 12.38 | 15.08 | 3.0× |
| babylon-wtb | 5.32 | 1.33 | 0.95 | 1.22 | error | 4.0× |
| base64-SP | 103.73 | 7.09 | 2.39 | 2.39 | 6.47 | 14.6× |
| Basic | 44.11 | error | error | error | error | – |
| Box2D | 34.39 | 7.05 | 3.37 | 3.34 | 6.21 | 4.9× |
| cdjs | 19.84 | 2.72 | 2.13 | 1.70 | 1.69 | 7.3× |
| chai-wtb | 17.98 | 3.35 | 3.59 | 3.53 | error | 5.0× |
| coffeescript-wtb | 4.59 | 0.12 | 0.38 | 0.40 | error | 11.5× |
| crypto | 122.23 | 7.88 | 3.58 | 2.46 | error | 15.5× |
| crypto-aes-SP | 54.63 | 8.88 | 3.97 | 4.06 | 4.02 | 6.2× |
| crypto-md5-SP | 62.16 | 4.73 | 2.20 | 2.19 | 3.87 | 13.1× |
| crypto-sha1-SP | 66.99 | 4.19 | 1.49 | 1.89 | 2.96 | 16.0× |
| date-format-tofte-SP | 27.19 | 1.21 | 4.33 | 4.59 | error | 5.9× |
| date-format-xparb-SP | 26.37 | 0.35 | 10.30 | 10.69 | error | 2.5× |
| delta-blue | 67.17 | 5.36 | 3.77 | 4.14 | 5.87 | 11.4× |
| earley-boyer | 59.15 | 5.96 | 6.20 | 6.75 | error | 8.8× |
| espree-wtb | 3.83 | 0.66 | 0.72 | 0.72 | error | 5.3× |
| first-inspector-code-load | 43.76 | 23.12 | 33.22 | 29.70 | 17.95 | 1.3× |
| FlightPlanner | 90.20 | 24.55 | 29.95 | 27.39 | 14.23 | 3.0× |
| float-mm.c | 0.54 | 0.04 | error | error | error | 14.0× |
| gaussian-blur | 59.69 | 1.90 | 1.09 | 0.89 | 1.19 | 31.4× |
| gbemu | 15.77 | 2.46 | 1.33 | 1.13 | error | 6.4× |
| hash-map | 37.80 | 3.19 | 2.88 | 2.86 | 3.02 | 11.8× |
| jshint-wtb | 5.76 | 2.73 | 1.52 | 1.70 | error | 2.1× |
| json-parse-inspector | 89.77 | 17.59 | 35.23 | 35.23 | 14.71 | 2.5× |
| json-stringify-inspector | 40.57 | 14.55 | 24.24 | 30.49 | 11.21 | 1.3× |
| lebab-wtb | 5.47 | 1.87 | 1.86 | 1.88 | error | 2.9× |
| mandreel | 4.77 | 0.53 | 0.19 | 0.19 | error | 8.9× |
| ML | 7.96 | 0.88 | 0.63 | 0.64 | 0.65 | 9.1× |
| multi-inspector-code-load | 65.59 | 19.00 | 33.12 | 32.36 | 18.94 | 2.0× |
| n-body-SP | 125.91 | 7.89 | 2.64 | 2.67 | 5.82 | 15.9× |
| navier-stokes | 194.68 | 11.59 | 3.94 | 3.34 | 5.41 | 16.8× |
| octane-code-load | 310.10 | 168.89 | 143.31 | 145.72 | error | 1.8× |
| octane-zlib | 1.12 | 0.13 | 0.05 | 0.04 | error | 8.9× |
| OfflineAssembler | 10.11 | error | 4.57 | 4.37 | error | 2.2× |
| pdfjs | 22.98 | 3.19 | 1.73 | 1.68 | 3.09 | 7.2× |
| prepack-wtb | 6.63 | 2.32 | 2.11 | 1.94 | error | 2.9× |
| raytrace | 39.21 | 4.54 | 2.90 | 2.91 | 3.16 | 8.6× |
| regex-dna-SP | 83.47 | 8.38 | 11.63 | 11.91 | error | 7.0× |
| regexp | 27.23 | 4.54 | 7.03 | 5.49 | 2.46 | 3.9× |
| richards | 68.36 | 4.47 | 3.27 | 3.27 | 3.67 | 15.3× |
| splay | 34.54 | 10.42 | 6.91 | 6.68 | 8.54 | 3.3× |
| stanford-crypto-aes | 55.29 | 6.45 | 3.07 | 2.95 | 5.33 | 8.6× |
| stanford-crypto-pbkdf2 | 59.61 | 9.44 | 3.61 | 3.62 | 6.33 | 6.3× |
| stanford-crypto-sha256 | 59.11 | 8.16 | 3.36 | 3.26 | 5.38 | 7.2× |
| string-unpack-code-SP | 65.41 | 22.30 | 51.92 | 40.00 | 13.77 | 1.3× |
| tagcloud-SP | 35.99 | 10.35 | 5.03 | 5.20 | 5.86 | 3.5× |
| typescript | 2.11 | 0.54 | 0.40 | 0.46 | error | 3.9× |
| uglify-js-wtb | 4.13 | 1.59 | 1.80 | 1.88 | error | 2.2× |
| UniPoker | 44.11 | 9.06 | 7.57 | 7.71 | 7.76 | 4.9× |
| **Geomean (31 in common)** | **51.09** | **7.48** | **5.36** | **5.21** | **5.62** | |
| **Completed (of 56)** | **56** | **52** | **52** | **52** | **32** | |

Over the 31 benchmarks all five complete, jsc2go is 6.8× QuickJS, 9.1× Ramune, 9.5× goja, 9.8× Sobek. It completes all 56 and is the fastest on each.

The smallest margins are where parsing and compiling dominate
(string-unpack-code, the code-load benchmarks, json-stringify-inspector);
the largest are numeric loops (gaussian-blur, navier-stokes, n-body, the
crypto benchmarks).

Errors of the other engines:

- **QuickJS** (4): 3d-cube-SP (Error: bad vector sum for CubeSize = 20; expected 2889.0000000000045 b); async-fs (no result); Basic (TypeError: cannot read property 'length' of undefined); OfflineAssembler (timeout).
- **goja** (4): 3d-cube-SP (Error: bad vector sum for CubeSize = 20; expected 2889.0000000000045 b); async-fs (SyntaxError: SyntaxError: Async generators are not supported yet at 11); Basic (TypeError: Cannot read property 'length' of undefined at lex (<eval>:4); float-mm.c (timeout).
- **Sobek** (4): 3d-cube-SP (Error: bad vector sum for CubeSize = 20; expected 2889.0000000000045 b); async-fs (SyntaxError: SyntaxError: Async generators are not supported yet at 11); Basic (TypeError: Cannot read property 'length' of undefined at lex (<eval>:4); float-mm.c (timeout).
- **Ramune** (24): 3d-cube-SP (TypeError: cannot set property 'V' of undefined); 3d-raytrace-SP (ReferenceError: h is not defined); acorn-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); babylon-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); Basic (TypeError: cannot read property 'length' of undefined); chai-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); coffeescript-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); crypto (ReferenceError: setupEngine is not defined); date-format-tofte-SP (ReferenceError: i is not defined); date-format-xparb-SP (ReferenceError: i is not defined); earley-boyer (SyntaxError: Octal escape sequences are not allowed in strict mode); espree-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); float-mm.c (TypeError: cannot set property 'Module' of undefined); gbemu (TypeError: 'length' is read-only); jshint-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); lebab-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); mandreel (ReferenceError: Mandreel_timeouts is not defined); octane-code-load (ReferenceError: salt is not defined); octane-zlib (SyntaxError: cannot delete a direct reference in strict mode); OfflineAssembler (timeout); prepack-wtb (ReferenceError: __web_tooling_global_suites__ is not defined); regex-dna-SP (ReferenceError: i is not defined); typescript (SyntaxError: invalid argument name in strict code); uglify-js-wtb (ReferenceError: __web_tooling_global_suites__ is not defined).

## How it got there

Against the same engines, jsc2go's geomean margin went from 2.6-3.1x (8
October 2026, the first JIT with slow paths) to 6.1-8.7x (17 October) and
6.8-9.8x (18 October). The steps that mattered most, each measured with
interleaved runs:

| Step | Largest gains (x86) |
|---|---|
| A slow path for every instruction, property caches with prototype chains, misses, getters and megamorphic tables, JSC-style intrinsics and thunks | crypto-aes +75-104%, ML +47-60%, gbemu +38-50%, typescript +24-34% |
| Number facts per basic block and a two-register number cache | +4.6% geomean |
| Switches in machine code, a prototype epoch, shared transitions for `delete` and `freeze`, builtins called without the slow path | +6.9% geomean |
| Arrays, closures, RegExp and String objects in one allocation | about +3.5% geomean |
| Monomorphic cache cases as constants in the code | +8.4% on 15 benchmarks |
| Regex engine: fixed alternatives without backtracking, a program matcher with an explicit backtrack stack | regex-dna 2.3x, regexp about +25% |
| Suspending nested generated code instead of unwinding to Go | +2.1% geomean (babylon +26%, espree +17%) |
| Variables of enclosing functions with constant depth, reprofiling sites that had not run, `f.apply(this, arguments)` without an arguments object, dynamic scope (`with`, `eval`) through the slow path | raytrace +62%, date-format-tofte +21% |
| Inlining of small functions | delta-blue +33%, richards +32%, md5 and sha1 +12% |
| Optimized versions of hot loops with numbers in registers | gaussian-blur +56%, 3d-cube +21%, float-mm +12% |
| Constant cases for polymorphic sites, fused global reads | md5 +8%, sha1 +7% |
| Bit operations on undefined, direct `apply` calls, `new Array(n)` from a pool, for-of and DataView in machine code | raytrace +47%, crypto-aes +28%, md5 +9%, ML +8% |

Each candidate was found by counting rather than guessing: slow paths per
opcode and operand kind over all 56 benchmarks, why calls were not inlined,
where inlined code stopped, then a CPU profile of the benchmarks concerned.

## Tried and not kept

| Idea | Result |
|---|---|
| Registers across instruction boundaries in the baseline JIT | almost every instruction has a slow path that resumes at the next one, so every boundary is a merge point; replaced by the separate optimized loop versions |
| Load forwarding in the macro assembler | -3.8% geomean: compiling became 2.5x as expensive and few loads could be forwarded |
| Virtual calls for megamorphic call sites (twice) | net neutral: gbemu and typescript +8%, others slower |
| Larger inlined callees (45 or 60 instructions instead of 30) | md5 and delta-blue -3..-4%, parsers at most +2.5% |
| Recompiling call sites that had not run when compiled | typescript, Box2D and the code-load benchmarks -5%: the compiles cost more than the inlining gained |
| int32 registers in optimized loops | +18% on a bit-operation loop, nothing on the benchmarks |
| Calls inside optimized loops | x86 -4..-7% (md5, base64), arm64 n-body +18%: only calls of builtins with a thunk are allowed |
| Sharing optimized-loop registers on arm64 | gaussian-blur -4..-5%: with 23 registers the most used values fit anyway |
| A higher GOGC instead of a minimum heap | base64 +23-33%, splay -9..-15% |
| Paired loads and stores (LDP/STP) on arm64 | no net gain |

On Windows, JetStream2 runs of the same binary drift by 5-20% over an hour;
every result above was confirmed with interleaved runs of the benchmarks
concerned, alternating the order of old and new.

## Footprint

`jsrun` is a 13.5 MB binary (Windows, amd64), starts in about 20 ms, and
uses about 10 MB of memory for `print(1)` and 35 MB for 100,000 small
objects.
