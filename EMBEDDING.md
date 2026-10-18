# Embedding API: design

Package `jsc2go` (the module root; `github.com/baslentfert/jsc2go` in the
public repository), a thin layer over `vm`. Close to goja's API where that
costs nothing, so that goja users feel at home; different where goja's
design defeats the JIT.

## Use

```go
rt := jsc2go.New()

// Values and functions in, results out.
rt.Set("greeting", "hello")
rt.Set("add", func(a, b float64) float64 { return a + b }) // typed: converted once, at Set
rt.Set("log", func(c jsc2go.FunctionCall) jsc2go.Value {   // raw: no conversion at all
	fmt.Println(c.Argument(0).String())
	return jsc2go.Undefined()
})
v, err := rt.RunString(`add(1, 2) + greeting.length`)
fmt.Println(v.Export()) // 8

// Go structs: copied in as plain objects (one shape per Go type, so the
// JIT caches their properties), with the type's methods; ExportTo copies
// back.
type Point struct{ X, Y float64 }
func (p Point) Len() float64 { return math.Hypot(p.X, p.Y) }
rt.Set("p", Point{3, 4})
v, _ = rt.RunString(`p.Len() + p.X`) // 8
var q Point
rt.ExportTo(rt.Get("p"), &q)

// Calling JavaScript from Go.
rt.RunString(`function scale(p, k) { return {X: p.X * k, Y: p.Y * k} }`)
scale, _ := jsc2go.AssertFunction(rt.Get("scale"))
r, err := scale(jsc2go.Undefined(), rt.ToValue(Point{1, 2}), rt.ToValue(10))

// Modules, as Bun's and Node's built-ins: Go functions behind an import.
rt.RegisterModule("app:db", map[string]any{
	"query": func(sql string) ([]map[string]any, error) { ... },
})
rt.RunModule("main.mjs", `import { query } from "app:db"; ...`)

// Stopping: a context, or Interrupt from another goroutine.
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
_, err = rt.RunContext(ctx, "main.js", src) // errors.Is(err, jsc2go.ErrInterrupted)

// Compile once, run in many runtimes (one runtime per goroutine).
prog, err := jsc2go.Compile("lib.js", src)
rt.RunProgram(prog)

// An event loop: setTimeout, setInterval, queueMicrotask and promises run
// in RunLoop; other goroutines hand work to it with Post, Do and Invoke
// (which also waits for a returned promise).
go rt.RunLoop(ctx)
res, err := rt.Invoke(ctx, rt.Get("handler"), "arg")
```

`examples/dashboard` uses all of it: a Wails desktop app whose logic is
JavaScript importing the system as Go modules, reloaded from disk when it
changes.

## Choices, and why

| Choice | Why |
|---|---|
| Copy Go structs, maps and slices into plain objects and arrays by default | Plain objects keep the JIT's inline caches, constant cases and inlining. Live wrappers (goja) read through reflection on every access; in JavaScriptCore, objects that intercept property access turn caching off altogether (`ProhibitsPropertyCaching`). |
| One shape per Go struct type | Objects of one type then share their shape, so a site that reads `p.X` stays monomorphic. |
| Methods as non-enumerable functions on the object, bound to its copy | A method needs its receiver, and each object is a copy of its own struct value; non-enumerable, so `JSON.stringify` and `Object.keys` see only the fields. |
| Typed Go functions analysed once, at `Set` | A converter per parameter and result is built once per function type and cached; a call only runs the converters and `reflect.Value.Call`. |
| `func(FunctionCall) Value` as the raw form | No conversion; the same as goja's fast form. |
| A Go `error` result throws, a JavaScript exception returns `*Exception` | As goja. |
| `context.Context` and `Interrupt` | A goroutine waits on `ctx.Done()` and interrupts. Generated code needs no extra check: calls already compare the call depth with a limit, which `Interrupt` sets to -1, and loops already return to the interpreter at least every 16384 iterations, which checks the flag. Proper tail calls and regular expressions check it too. |
| One runtime per goroutine; `Program` shared | As goja and every other engine here; `Interrupt`, `Post`, `Do` and `Invoke` are the cross-goroutine entry points. |
| Modules registered from Go (`app:...`) | How Bun, Node and Deno expose host functionality: a fixed surface, typed functions, the rest written in JavaScript on top. |

## Later

- A goja compatibility package (`compat/goja`) with goja's exact API, for
  existing goja code.
- A cheaper path from generated code into typed Go functions (no bytecode
  decode, no allocation for four or more arguments), and native getters
  generated code can call; later perhaps unboxed arguments for typed
  signatures, as V8's fast API calls.
- Live binding of Go values as an opt-in, for where the Go value must stay
  authoritative (accessors that run Go on every get and set leave
  generated code each time).
