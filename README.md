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

## Embedding

The root package runs JavaScript inside a Go program, with an API close to
goja's:

```go
import "github.com/baslentfert/jsc2go"

rt := jsc2go.New()
rt.Set("add", func(a, b float64) float64 { return a + b })
v, err := rt.RunString(`add(1, 2) * 10`)
fmt.Println(v.Export(), err) // 30 <nil>
```

- Go values, functions and structs go in with `Set` and `ToValue`; results
  come back with `Export` and `ExportTo`. Structs are copied into plain
  objects with one shape per type, so the JIT keeps caching their
  properties. A Go `error` result throws; an uncaught exception comes
  back as `*Exception`.
- `RunContext` and `Interrupt` stop a script from another goroutine,
  including endless loops, deep recursion and regular expressions; the
  runtime can be used again afterwards.
- An event loop (`RunLoop`) with `setTimeout`, `setInterval`,
  `queueMicrotask` and promises; `Post`, `Do` and `Invoke` hand work to
  it from other goroutines.
- ES modules: `RegisterModule` exposes Go functions as an importable
  module (as Bun's and Node's built-ins), `SetModuleLoader` and
  `FileLoader` load the rest.

The package documentation and `example_test.go` show each part;
[EMBEDDING.md](EMBEDDING.md) explains the choices.
[examples/dashboard](examples/dashboard/README.md) is a
[Wails](https://wails.io) desktop app whose logic is a JavaScript file
importing the system (CPU, memory, disks, processes, SQLite, commands)
as Go modules; it reloads that file when it changes, without a rebuild.

A Runtime belongs to one goroutine at a time, as in goja.

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
| the root | the embedding API (`Runtime`, `Value`, the event loop, modules) |
| `examples/` | programs that embed jsc2go, each with its own `go.mod` |
| `cmd/` | `jsrun` (run JavaScript), `jscbc` and `jscparse` (bytecode, syntax tree), `test262` and its helpers |
| `bench/` | the JetStream2 runner, the other engines (`bench/engines`), checks for other machines (`bench/vps`) |

## License

jsc2go is a derivative work of JavaScriptCore and WTF and is distributed
under the GNU Lesser General Public License, version 2.1 or later (see
[LICENSE](LICENSE)). [NOTICE](NOTICE) names the original copyright
holders and reproduces the BSD notices of the parts under that license.

### Licensing in practice

A plain summary, not legal advice; the license text is what counts.

**You may:**
- use jsc2go in any program, including commercial and closed-source ones;
- study, modify and redistribute it;
- run it on servers or internally without any obligation: the LGPL is
  about distributing software, not about using it over a network.

**When you distribute a program that contains jsc2go** (give or sell a
binary to others), you must:
1. include LICENSE and NOTICE (or point to them) and keep the copyright
   notices;
2. make the source code of the jsc2go you used available, including your
   changes to jsc2go, which stay under the LGPL;
3. let the recipient replace jsc2go with a modified version and rebuild
   your program. Your own code can stay closed, but Go links everything
   statically into one binary, so in practice this means providing your
   program's source or object files so that it can be relinked against
   another jsc2go.

**You may not:**
- distribute jsc2go, or a modified version of it, under a closed or more
  restrictive license;
- remove the copyright and license notices.

**Your program can keep its own license**, MIT or closed: the LGPL
covers jsc2go, not the code that uses it. Bun is the same arrangement one
level down: it is MIT-licensed and statically links JavaScriptCore, which
stays LGPL; Bun publishes its JavaScriptCore source and explains how to
rebuild Bun with a modified one (see Bun's
[LICENSE.md](https://github.com/oven-sh/bun/blob/main/LICENSE.md)).
jsc2go is in JavaScriptCore's place there, your program in Bun's. For an
open-source program the source requirements are met by publishing it.

jsc2go comes without any warranty. Why LGPL: jsc2go is translated from
JavaScriptCore, whose parser and much of whose runtime are under the LGPL
(version 2 or later); a translation is a derivative work, so it cannot be
relicensed under a more permissive license such as MIT.
