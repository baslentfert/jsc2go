# How jsc2go is built

jsc2go follows JavaScriptCore closely where Go allows it, and departs from
it where Go's runtime makes JSC's approach impossible or costly. Files and
types keep JSC's names, so `parser/Parser` reads next to
`Source/JavaScriptCore/parser/Parser.cpp`.

## Front end and bytecode

`parser/` is JSC's lexer and parser; `bytecompiler/` its
`BytecodeGenerator` and the per-node code generation; `bytecode/` the
instruction set (`opcodes_gen.go` is generated from JSC's opcode list),
code blocks, jump tables, liveness and the use/def tables. The bytecode is
JSC's: the same opcodes, operands, checkpoints and metadata, which lets
JSC's interpreter and JIT serve as the reference for ours.

## Values, objects and memory

- A `Value` is 16 bytes: a pointer and a word. The word holds a tag
  (undefined, null, booleans, empty, cells, objects, strings, symbols, ...)
  or a float64 XORed with a mask. Numbers are always doubles; there is no
  separate int32 representation. JSC's 8-byte NaN-boxed values would hide
  pointers from Go's garbage collector.
- Objects have shapes (JSC's structures): transitions per added property,
  shared transitions for `delete` and `freeze`, dictionaries for objects
  that change too much, and a prototype epoch that acts as a watchpoint for
  prototype chains. Arrays, closures, RegExp objects and String objects are
  each one allocation.
- Strings are ropes with an append buffer for repeated `s += t`; the atom
  table (`ident/`) interns identifiers.
- Go's garbage collector replaces JSC's heap. Generated code allocates from
  per-VM pools of objects, arrays, closures, strings and scopes, which Go
  code refills.

## Interpreter

`vm.VM.run` interprets the bytecode, with JSC's inline caches for property
accesses and calls, and fills the profiles the JIT compiles from (call
targets, array and typed-array kinds, comparisons with undefined). The loop
is large enough that Go inlines only small callees into it, which shapes how
registers and operands are read.

## Baseline JIT (`vm/jit*.go`, `jit/`)

A function that is called or loops often enough (`JSC2GO_JITTHRESHOLD`) is
compiled, one bytecode instruction at a time, with a macro assembler that
has an amd64 and an arm64 backend (`jit/masm_*.go`). Executable memory
comes from `mmap`/`mprotect` or `VirtualAlloc`/`VirtualProtect`.

- **Inline caches:** `get_by_id` and `put_by_id` with monomorphic,
  polymorphic and megamorphic cases, prototype chains, misses, getters,
  storage growth and the case held as constants in the code; `get_by_val`
  and `put_by_val` for arrays, holes, appends, typed arrays and string
  characters; `get_length`; global variables through the global object's
  fields.
- **Calls:** direct calls between generated code through per-site call
  caches (four cases, closure cases per executable), with the frame built
  in machine code; builtins with a thunk (`String.prototype.charCodeAt`,
  `Math.sqrt`, `slice`, ...) are called as machine code; `f.apply(this,
  arguments)` forwards the caller's arguments directly.
- **Slow paths:** every instruction has a path into Go
  (`jitSlowPath`) that does the general case and resumes in the generated
  code at the next instruction, so one unusual operand does not stop a
  function from being compiled. Rarely taken code is emitted after all
  instructions.
- **Type facts:** per basic block, the JIT knows which registers hold
  numbers and keeps two of them in floating-point registers (the number
  cache), so checks and loads are not repeated.
- **Suspension:** generated code that calls other generated code and then
  needs Go (a slow path, a builtin) is suspended in place: its frames stay
  on the goroutine stack inside the trampoline (`jit.CallS`), Go does the
  work, and the code is resumed, instead of the whole chain of calls being
  unwound. Exceptions thrown meanwhile find the handlers of the outermost
  frame.
- **Garbage collector:** generated code runs only while Go's write barrier
  is off and writes no barriers; it never keeps stack addresses, since Go
  may move the stack while code is suspended.

## Inlining (`vm/jit_inline.go`)

A call site whose profile saw one or two small functions (at most 30
instructions, no loops, no arguments object or closures of their own) gets
their code in place, behind a check on the callee, compiled by a second
compiler into the caller's code, two levels deep. If inlined code needs
something it cannot do, a stub builds the frames it would have had and
continues in Go; a site that stops 64 times is compiled again without
inlining.

## Optimizing tier for loops (`vm/jit_opt.go`)

A hot innermost loop (5000 iterations, `JSC2GO_OPTHOT`) gets a second
version in which its numbers stay unboxed in floating-point registers
(F8-F14 on amd64, 23 registers on arm64; on amd64 values that are never
live together share a register when they do not all fit). Arithmetic,
comparisons, element and property accesses work on the registers. Any
instruction that cannot gives up to the baseline code of the same
instruction: the registers are stored, the number cache is refilled, and
execution continues there. Loops whose entries keep failing are compiled
again without the registers concerned.

## Regular expressions (`yarr/`)

A port of Yarr's parser and pattern compiler, with a program matcher that
keeps an explicit backtrack stack, prefilters that skip positions that
cannot match, and a tree matcher kept as the reference for differential
tests. There is no regex JIT.

## Testing

- test262 with JSC's configuration and expectations
  (`scripts/check262.sh`), also with nearly everything JIT-compiled.
- Differential fuzzing of the JIT against the interpreter
  (`TestJITDifferential`) and of the regex prefilters and matchers.
- Unit tests for the macro assembler against reference implementations in
  Go, and JIT tests per feature, checked by mutation: a test must fail when
  the code path it is meant to cover is broken on purpose.
- Memory leak probes (`bench/vps/checks/leaks.sh`): every cache or table
  that can grow is bounded or weak.

## Not (yet) there

Register allocation over whole functions with int32 speculation (a real
DFG), a regex JIT, `get_by_val` with fresh string keys in machine code,
string concatenation in machine code, direct calls of callees with
exception handlers, caches for proxies, and 32-bit platforms.
