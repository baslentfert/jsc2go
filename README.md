# jsc2go

A port of WebKit's JavaScriptCore to Go: parser, bytecode compiler,
interpreter, a baseline JIT for amd64 and arm64 with inlining and an
optimizing tier for hot loops, runtime and built-ins, Yarr regular
expressions and modules. Go's garbage collector replaces JSC's heap. It
keeps JSC's structure and names, so the C++ sources stay a usable
reference.

- **Conformance:** 85615 of 85664 test262 runs pass on the subset JSC
  itself runs; every remaining failure is one JSC expects too.
- **Speed:** on JetStream2's JavaScript benchmarks it is the fastest of
  the Go engines on all 56, 6.8x modernc QuickJS and 9-10x goja, Sobek
  and Ramune (geomean; [PERFORMANCE.md](PERFORMANCE.md)).
- **Platforms:** the JIT runs on amd64 (Linux, macOS, Windows) and arm64
  (macOS; Linux tested in containers, not yet on hardware); everywhere else
  jsc2go runs with its interpreter. 32-bit platforms do not build yet.
- **Footprint:** a 13.5 MB binary that starts in about 20 ms and uses
  about 10 MB for `print(1)`.

How it is built: [ARCHITECTURE.md](ARCHITECTURE.md).

## Setup

Needs git and Go (version in `go.mod`).

    git clone <this repository> jsc2go
    cd jsc2go
    sh scripts/setup.sh

`setup.sh` clones test262 and the parts of WebKit used as the reference
(JavaScriptCore, WTF, JetStream2 and JSC's test262 expectations) into
`test262/` and `webkit/`, at the commits the port is based on, then builds
and tests. Neither is part of this repository. On Windows, run the `.sh`
scripts from Git Bash.

## Running JavaScript

    go run ./cmd/jsrun file.js          # or -e 'code', -module, .mjs files
    go run ./cmd/jscbc file.js          # dump the bytecode

Environment variables for the JIT:

| variable | effect |
|---|---|
| `JSC2GO_JIT=0` | interpreter only |
| `JSC2GO_JITTHRESHOLD=n` | calls or loop iterations before a function is compiled |
| `JSC2GO_JITLOG=1` | log compiles, exits and what blocks compilation |
| `JSC2GO_JITDUMP=1` | code addresses per bytecode instruction (for profiles) |
| `JSC2GO_INLINE=0` | no inlining |
| `JSC2GO_OPT=0` | no optimizing tier |
| `JSC2GO_OPTHOT=n` | loop iterations before a loop gets an optimized version (default 5000) |

## Tests

    go test ./...
    sh scripts/test262.sh               # test262 with JSC's configuration (~4 min)
    sh scripts/check262.sh              # the same, failing on unexpected failures
    JSC2GO_JITTHRESHOLD=1 sh scripts/test262.sh            # with nearly everything compiled
    JIT_FUZZ_N=8000 go test -v -run TestJITDifferential ./vm  # JIT against the interpreter
    YARR_FUZZ_N=20000 go test -v -run TestPrefiltersDifferential ./yarr

`scripts/setup.sh test262` clones only test262 (all the tests need). test262
needs LF line endings (`setup.sh` clones with `core.autocrlf=false`).

Like JSC's configuration, the runs skip
`built-ins/ThrowTypeError/unique-per-realm-function-proto.js`: it expects
`Function.prototype.caller` and `.arguments` to be `%ThrowTypeError%`, where
JSC and jsc2go have the legacy function reflection accessors.

GitHub Actions (`.github/workflows/ci.yml`) runs the tests, the JIT and
regex fuzzing and the leak probes on Linux and Windows, and test262 (with
and without nearly everything compiled) on Linux, for every push and pull
request; every week, everything also on macOS.
[bench/vps](bench/vps/README.md) runs the same checks and the benchmarks on
a machine of your own.

## Benchmarks

    sh bench/build-engines.sh           # bench/bin/jsrun
    sh bench/jetstream.sh               # JetStream2's JavaScript benchmarks on jsc2go

To compare with the other Go JavaScript engines (goja, Sobek, modernc
QuickJS, Ramune), `sh bench/build-engines.sh all` builds their runners from
the separate `bench/engines` module; only that downloads them. Then pass
`-engines jsc2go,quickjs,goja,sobek,ramune` to `bench/jetstream.sh`.

## Layout

| directory | contents |
|---|---|
| `parser/`, `bytecompiler/`, `bytecode/` | JSC's front end and bytecode (`bytecode/opcodes_gen.go` is generated: `go generate ./bytecode`) |
| `vm/` | runtime, built-ins, the interpreter (`interpreter.go`) and the JIT's code generator (`jit*.go`) |
| `jit/` | macro assembler (amd64, arm64), executable memory, call trampolines |
| `yarr/` | regular expressions |
| `ident/` | identifiers and the atom table |
| `wtf/dtoa/` | number to string conversion (double-conversion) |
| `cmd/` | `jsrun` (run JavaScript), `jscbc` and `jscparse` (bytecode, syntax tree), `test262` and its helpers |
| `bench/` | the JetStream2 runner, the other engines (`bench/engines`), checks for other machines (`bench/vps`) |

## License

jsc2go is a derivative work of JavaScriptCore and WTF and is distributed
under the GNU Lesser General Public License, version 2.1 or later (see
[LICENSE](LICENSE)). [NOTICE](NOTICE) names the original copyright
holders and reproduces the BSD notices of the parts under that license.
