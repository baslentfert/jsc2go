package jsc2go

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, rt *Runtime, src string) Value {
	t.Helper()
	v, err := rt.RunString(src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v
}

type item struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Tags  []string
}

func (i item) Label() string { return i.Name + " (" + strings.Join(i.Tags, ",") + ")" }

func TestRunAndValues(t *testing.T) {
	rt := New()
	if got := run(t, rt, `1 + 2`).Export(); got != int64(3) {
		t.Fatalf("1 + 2 = %v", got)
	}
	if got := run(t, rt, `"a" + 1`).String(); got != "a1" {
		t.Fatalf("got %q", got)
	}
	if err := rt.Set("items", []item{{"apple", 0.5, []string{"fruit"}}, {"pear", 0.75, nil}}); err != nil {
		t.Fatal(err)
	}
	if got := run(t, rt, `items.map(i => i.name + ":" + i.price).join()`).String(); got != "apple:0.5,pear:0.75" {
		t.Fatalf("got %q", got)
	}
	if got := run(t, rt, `items[0].Label()`).String(); got != "apple (fruit)" {
		t.Fatalf("method: got %q", got)
	}
	var back []item
	if err := rt.ExportTo(run(t, rt, `items.map(i => ({...i, price: i.price * 2}))`), &back); err != nil {
		t.Fatal(err)
	}
	if back[1].Price != 1.5 || back[0].Tags[0] != "fruit" {
		t.Fatalf("ExportTo: %+v", back)
	}
	if !run(t, rt, `undefined`).IsUndefined() || !rt.Get("nothing").IsUndefined() || !Null().IsNull() {
		t.Fatal("undefined/null")
	}
	rt.Set("x", 42)
	if rt.Get("x").ToInteger() != 42 {
		t.Fatal("Get")
	}
	obj := run(t, rt, `({a: {b: 7}})`)
	if obj.Get("a").Get("b").ToFloat() != 7 {
		t.Fatal("Value.Get")
	}
	if err := obj.Set("c", "set"); err != nil || run(t, rt, `1`).IsUndefined() {
		t.Fatal(err)
	}
}

func TestFunctions(t *testing.T) {
	rt := New()
	rt.Set("add", func(a, b int) int { return a + b })
	rt.Set("div", func(a, b float64) (float64, error) {
		if b == 0 {
			return 0, errors.New("division by zero")
		}
		return a / b, nil
	})
	rt.Set("raw", func(c FunctionCall) Value {
		if len(c.Arguments) == 0 {
			c.Throw("no arguments")
		}
		return c.Runtime.ToValue(c.Argument(0).String() + "!")
	})
	rt.Set("keep", func(v Value) Value { return v }) // a Value parameter and result
	if got := run(t, rt, `add(2, 3) + "," + div(1, 4) + "," + raw("hi") + "," + keep({k: 1}).k`).String(); got != "5,0.25,hi!,1" {
		t.Fatalf("got %q", got)
	}
	if got := run(t, rt, `try { div(1, 0) } catch (e) { e.message }`).String(); got != "division by zero" {
		t.Fatalf("got %q", got)
	}
	if got := run(t, rt, `try { raw() } catch (e) { e.message }`).String(); got != "no arguments" {
		t.Fatalf("got %q", got)
	}
	// A JavaScript function called from Go.
	run(t, rt, `function greet(name) { return "hello " + name }`)
	greet, ok := AssertFunction(rt.Get("greet"))
	if !ok {
		t.Fatal("AssertFunction")
	}
	if r, err := greet(Undefined(), rt.ToValue("go")); err != nil || r.String() != "hello go" {
		t.Fatalf("greet: %v %v", r, err)
	}
	if _, ok := AssertFunction(rt.ToValue(1)); ok {
		t.Fatal("AssertFunction on a number")
	}
	// Values of one Runtime cannot go into another.
	other := New()
	if err := other.Set("o", run(t, rt, `({})`)); err == nil {
		t.Fatal("an object of another Runtime was accepted")
	}
}

func TestExceptions(t *testing.T) {
	rt := New()
	_, err := rt.RunString(`function f() { throw new TypeError("bad thing") } f()`)
	var exc *Exception
	if !errors.As(err, &exc) {
		t.Fatalf("got %T %v", err, err)
	}
	if !strings.Contains(exc.Error(), "bad thing") || exc.Value().Get("name").String() != "TypeError" {
		t.Fatalf("exception: %v", exc)
	}
	if _, err := rt.RunString(`this is not javascript`); !errors.As(err, &exc) || exc.Value().Get("name").String() != "SyntaxError" {
		t.Fatalf("syntax error: %v", err)
	}
	// A rejected promise in a job surfaces too.
	if _, err := rt.RunString(`Promise.resolve().then(() => { throw new Error("in a job") }); 1`); err != nil {
		t.Logf("job error: %v", err) // (an unhandled rejection is not an exception)
	}
}

func TestInterruptAndContext(t *testing.T) {
	rt := New()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := rt.RunContext(ctx, "spin.js", `for (;;) {}`)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stopped after %v", d)
	}
	// The runtime works again at once (RunContext cleared the interrupt).
	if got := run(t, rt, `"still " + "here"`).String(); got != "still here" {
		t.Fatalf("got %q", got)
	}
	time.AfterFunc(50*time.Millisecond, rt.Interrupt)
	if _, err := rt.RunString(`(function f() { return f() + 1 })()`); err == nil {
		t.Log("(the recursion overflowed first)")
	}
	rt.ClearInterrupt()
	time.AfterFunc(50*time.Millisecond, rt.Interrupt)
	if _, err := rt.RunString(`while (true) {}`); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Interrupt: %v", err)
	}
	rt.ClearInterrupt()
	run(t, rt, `1`)
}

func TestProgramReuse(t *testing.T) {
	rt := New()
	p, err := rt.Compile("lib.js", `var n = (typeof n == "number" ? n : 0) + 1; n`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		v, err := rt.RunProgram(p)
		if err != nil || v.ToInteger() != int64(i) {
			t.Fatalf("run %d: %v %v", i, v, err)
		}
	}
}

func TestConsole(t *testing.T) {
	rt := New()
	var out, errOut bytes.Buffer
	rt.SetConsole(&out, &errOut)
	run(t, rt, `console.log("a", 1, [1, "b"], {x: {y: [2]}}); console.error("oops", true); print("p")`)
	if got := out.String(); got != "a 1 [ 1, \"b\" ] { x: { y: [ 2 ] } }\np\n" {
		t.Fatalf("stdout %q", got)
	}
	if got := errOut.String(); got != "oops true\n" {
		t.Fatalf("stderr %q", got)
	}
}
