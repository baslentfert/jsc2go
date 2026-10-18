package vm

import (
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

type convPoint struct {
	X, Y   float64
	Label  string `json:"label"`
	Hidden int    `json:"-"`
	secret int
}

func (p convPoint) Sum() float64     { return p.X + p.Y }
func (p *convPoint) Scale(k float64) { p.X *= k; p.Y *= k }

// evalT evaluates src and fails the test on an exception.
func evalT(t *testing.T, v *VM, src string) Value {
	t.Helper()
	res, err := v.Eval(src, "test.js")
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return res
}

// TestToValue converts Go values to JavaScript and inspects them there.
func TestToValue(t *testing.T) {
	v := New()
	r := v.Realm()
	set := func(name string, x any) { r.GlobalObject.CreateDataPropertyOrThrow(StrKey(name), r.ToValue(x)) }
	p := &convPoint{X: 3, Y: 4, Label: "p", Hidden: 7, secret: 9}
	set("p", p)
	set("pv", *p)
	set("nums", []int{1, 2, 3})
	set("bytes", []byte{1, 2, 255})
	set("m", map[string]any{"b": 2, "a": []string{"x"}, "n": nil})
	set("when", time.UnixMilli(1700000000000))
	set("big", new(big.Int).Lsh(big.NewInt(1), 70))
	set("err", errors.New("boom"))
	set("nilptr", (*convPoint)(nil))
	set("add", func(a, b float64) float64 { return a + b })
	set("concat", func(sep string, parts ...string) string { return strings.Join(parts, sep) })
	set("pair", func(s string) (string, int) { return s, len(s) })
	set("fail", func(x int) (int, error) {
		if x < 0 {
			return 0, errors.New("negative")
		}
		return x * 2, nil
	})
	set("crash", func() { panic("oops") })
	set("raw", func(c *Call) Value { return Int(len(c.Args)) })

	checks := []struct{ src, want string }{
		{`[p.X, p.Y, p.label, p.Hidden, p.secret].join()`, "3,4,p,,"},
		{`Object.keys(p).join()`, "X,Y,label"},
		{`p.Sum()`, "7"},
		{`p.Scale(2); [p.X, p.Y].join()`, "3,4"}, // the object is a copy...
		{`typeof pv.Scale + "," + typeof pv.Sum`, "undefined,function"},
		{`Array.isArray(nums) + "," + nums.map(x => x * 2).join()`, "true,2,4,6"},
		{`bytes instanceof Uint8Array && bytes.join()`, "1,2,255"},
		{`Object.keys(m).join() + "," + m.a[0] + "," + m.n`, "a,b,n,x,null"},
		{`when instanceof Date && when.getTime()`, "1700000000000"},
		{`typeof big + "," + (big === 2n ** 70n)`, "bigint,true"},
		{`err instanceof Error && err.message`, "boom"},
		{`nilptr`, "null"},
		{`add(1, 2)`, "3"},
		{`add("3", 4)`, "7"},
		{`concat("-", "a", "b", "c") + concat("+")`, "a-b-c"},
		{`pair("abc").join()`, "abc,3"},
		{`fail(21)`, "42"},
		{`try { fail(-1) } catch (e) { e instanceof Error && e.message }`, "negative"},
		{`try { fail(1.5) } catch (e) { e instanceof TypeError }`, "true"},
		{`try { crash() } catch (e) { e.message }`, "Go panic: oops"},
		{`raw(1, 2, 3)`, "3"},
		{`add.length`, "2"},
	}
	for _, c := range checks {
		if got := v.ToString(evalT(t, v, c.src)).GoString(); got != c.want {
			t.Errorf("%s = %q, want %q", c.src, got, c.want)
		}
	}
	// ...but pointer receivers act on the Go value.
	if p.X != 6 || p.Y != 8 {
		t.Errorf("Scale on the Go value: got %v, %v", p.X, p.Y)
	}
}

// TestStructShape: objects of one Go struct type share a shape, so that
// a property access site sees one (monomorphic) case.
func TestStructShape(t *testing.T) {
	v := New()
	r := v.Realm()
	a := r.ToValue(convPoint{X: 1}).AsObject()
	b := r.ToValue(convPoint{X: 2, Label: "b"}).AsObject()
	if a.props.shape != b.props.shape {
		t.Fatal("two objects of one struct type have different shapes")
	}
}

// TestExport converts JavaScript values to Go.
func TestExport(t *testing.T) {
	v := New()
	got := v.Export(evalT(t, v, `({a: 1, b: 1.5, c: "s", d: [true, null, undefined], e: {f: -0}, g: new Date(5), h: new Uint8Array([7, 8])})`))
	want := map[string]any{"a": int64(1), "b": 1.5, "c": "s", "d": []any{true, nil, nil}, "e": map[string]any{"f": -0.0},
		"g": time.UnixMilli(5), "h": []byte{7, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Export: got %#v\nwant %#v", got, want)
	}
	fn := v.Export(evalT(t, v, `(function (a, b) { if (a < 0) throw new RangeError("neg"); return a * b })`)).(func(...any) (any, error))
	if res, err := fn(6, 7); err != nil || res != int64(42) {
		t.Fatalf("exported function: %v, %v", res, err)
	}
	if _, err := fn(-1, 1); err == nil || !strings.Contains(err.Error(), "neg") {
		t.Fatalf("exported function's exception: %v", err)
	}
}

// TestExportTo converts into typed Go targets, and reports values that do
// not fit.
func TestExportTo(t *testing.T) {
	v := New()
	var p convPoint
	if err := v.ExportTo(evalT(t, v, `({X: 1, Y: 2, label: "q", Hidden: 5})`), &p); err != nil {
		t.Fatal(err)
	}
	if p != (convPoint{X: 1, Y: 2, Label: "q"}) {
		t.Fatalf("struct: %+v", p)
	}
	var ps []*convPoint
	if err := v.ExportTo(evalT(t, v, `[{X: 1}, null, {Y: 2}]`), &ps); err != nil || len(ps) != 3 || ps[0].X != 1 || ps[1] != nil || ps[2].Y != 2 {
		t.Fatalf("slice of pointers: %v %v", ps, err)
	}
	var m map[string]int
	if err := v.ExportTo(evalT(t, v, `({a: 1, b: 2})`), &m); err != nil || m["a"] != 1 || m["b"] != 2 {
		t.Fatalf("map: %v %v", m, err)
	}
	var u8 uint8
	if err := v.ExportTo(Int(256), &u8); err == nil {
		t.Fatal("256 into a uint8: no error")
	}
	var n int
	if err := v.ExportTo(Number(1.5), &n); err == nil {
		t.Fatal("1.5 into an int: no error")
	}
	var f func(int, int) (int, error)
	if err := v.ExportTo(evalT(t, v, `(a, b) => { if (b == 0) throw new Error("div"); return a / b }`), &f); err != nil {
		t.Fatal(err)
	}
	if r, err := f(6, 3); err != nil || r != 2 {
		t.Fatalf("func: %v %v", r, err)
	}
	if _, err := f(1, 0); err == nil || !strings.Contains(err.Error(), "div") {
		t.Fatalf("func's exception: %v", err)
	}
	var when time.Time
	if err := v.ExportTo(evalT(t, v, `new Date(86400000)`), &when); err != nil || !when.Equal(time.UnixMilli(86400000)) {
		t.Fatalf("time: %v %v", when, err)
	}
	if err := v.ExportTo(Int(1), p); err == nil {
		t.Fatal("non-pointer target: no error")
	}
}

// TestToValueJIT runs generated code over converted Go values: the copies
// are plain objects and arrays, read in machine code like any other.
func TestToValueJIT(t *testing.T) {
	saved := jitThreshold
	jitThreshold = 2
	defer func() { jitThreshold = saved }()
	v := New()
	r := v.Realm()
	pts := make([]convPoint, 1000)
	for i := range pts {
		pts[i] = convPoint{X: float64(i), Y: 1}
	}
	r.GlobalObject.CreateDataPropertyOrThrow(StrKey("pts"), r.ToValue(pts))
	r.GlobalObject.CreateDataPropertyOrThrow(StrKey("weight"), r.ToValue(func(p convPoint) float64 { return p.X * p.Y }))
	got := evalT(t, v, `
		function total(a) { var s = 0; for (var i = 0; i < a.length; i++) s += a[i].X + a[i].Y; return s; }
		var t = 0; for (var k = 0; k < 20; k++) t += total(pts);
		t + weight(pts[10])`)
	if got.num() != 20*(999*1000/2+1000)+10 {
		t.Fatalf("got %v", got.num())
	}
}
