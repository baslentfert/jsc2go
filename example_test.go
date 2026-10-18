package jsc2go_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/baslentfert/jsc2go"
)

func Example() {
	rt := jsc2go.New()
	rt.Set("add", func(a, b float64) float64 { return a + b })
	v, err := rt.RunString(`add(1, 2) * 10`)
	if err != nil {
		panic(err)
	}
	fmt.Println(v.Export())
	// Output: 30
}

type Point struct{ X, Y float64 }

func (p Point) Len() float64 { return math.Hypot(p.X, p.Y) }

// Structs are copied into plain objects, with their methods; ExportTo
// copies a result back.
func Example_structs() {
	rt := jsc2go.New()
	rt.Set("p", Point{3, 4})
	v, _ := rt.RunString(`({X: p.X * 2, Y: p.Y * 2, len: p.Len()})`)
	var q Point
	rt.ExportTo(v, &q)
	fmt.Println(q, v.Get("len"))
	// Output: {6 8} 5
}

// A Go function's error result becomes a JavaScript exception, and an
// uncaught exception comes back as *Exception.
func Example_errors() {
	rt := jsc2go.New()
	rt.Set("parse", func(s string) (int, error) {
		if s == "" {
			return 0, errors.New("empty input")
		}
		return len(s), nil
	})
	v, _ := rt.RunString(`try { parse("") } catch (e) { "caught: " + e.message }`)
	fmt.Println(v)
	_, err := rt.RunString(`parse("")`)
	var exc *jsc2go.Exception
	fmt.Println(errors.As(err, &exc), strings.Contains(err.Error(), "empty input"))
	// Output:
	// caught: empty input
	// true true
}

// Calling a JavaScript function from Go.
func ExampleAssertFunction() {
	rt := jsc2go.New()
	rt.RunString(`function greet(name) { return "hello, " + name }`)
	greet, _ := jsc2go.AssertFunction(rt.Get("greet"))
	v, _ := greet(jsc2go.Undefined(), rt.ToValue("Go"))
	fmt.Println(v)
	// Output: hello, Go
}

// A raw function sees the JavaScript values themselves: no conversions.
func ExampleFunctionCall() {
	rt := jsc2go.New()
	rt.Set("count", func(c jsc2go.FunctionCall) jsc2go.Value {
		return c.Runtime.ToValue(len(c.Arguments))
	})
	v, _ := rt.RunString(`count(1, "two", [3])`)
	fmt.Println(v)
	// Output: 3
}

// A context stops a script that runs too long; the runtime can be used
// again afterwards.
func ExampleRuntime_RunContext() {
	rt := jsc2go.New()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := rt.RunContext(ctx, "spin.js", `while (true) {}`)
	fmt.Println(errors.Is(err, jsc2go.ErrInterrupted))
	v, _ := rt.RunString(`"still usable"`)
	fmt.Println(v)
	// Output:
	// true
	// still usable
}
