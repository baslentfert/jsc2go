// Command jsbench runs the same script on jsc2go, goja, Sobek and modernc
// QuickJS. Every engine gets print(...) and readFile(path) (relative to
// -root).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/grafana/sobek"
	"github.com/baslentfert/jsc2go/vm"
	"modernc.org/quickjs"
)

func main() {
	engine := flag.String("engine", "jsc2go", "jsc2go, goja, sobek or quickjs")
	root := flag.String("root", "", "directory readFile paths are relative to")
	flag.Parse()
	src, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		panic(err)
	}
	if *root != "" {
		if err := os.Chdir(*root); err != nil {
			panic(err)
		}
	}
	readFile := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		return string(b)
	}
	start := time.Now()
	switch *engine {
	case "jsc2go":
		v := vm.New()
		v.Print = func(s string) { fmt.Println(s) }
		r := v.Realm()
		r.DefineFunction(r.GlobalObject, "readFile", 1, func(c *vm.Call) vm.Value {
			return vm.Str(readFile(c.VM.ToString(c.Arg(0)).String()))
		})
		if _, err := v.Eval(string(src), "bench.js"); err != nil {
			fmt.Println("JSERROR", err)
			os.Exit(1)
		}
		if exc := v.RunJobs(); exc != nil {
			fmt.Println("JSERROR", exc)
			os.Exit(1)
		}
	case "goja", "sobek":
		var run func() error
		if *engine == "goja" {
			r := goja.New()
			r.Set("print", func(call goja.FunctionCall) goja.Value {
				parts := make([]string, len(call.Arguments))
				for i, a := range call.Arguments {
					parts[i] = a.String()
				}
				fmt.Println(strings.Join(parts, " "))
				return goja.Undefined()
			})
			r.Set("readFile", readFile)
			run = func() error { _, err := r.RunString(string(src)); return err }
		} else {
			r := sobek.New()
			r.Set("print", func(call sobek.FunctionCall) sobek.Value {
				parts := make([]string, len(call.Arguments))
				for i, a := range call.Arguments {
					parts[i] = a.String()
				}
				fmt.Println(strings.Join(parts, " "))
				return sobek.Undefined()
			})
			r.Set("readFile", readFile)
			run = func() error { _, err := r.RunString(string(src)); return err }
		}
		if err := run(); err != nil {
			fmt.Println("JSERROR", err)
			os.Exit(1)
		}
	case "quickjs":
		m, err := quickjs.NewVM()
		if err != nil {
			panic(err)
		}
		defer m.Close()
		if err := m.RegisterHostFunc("print", func(args []any) (any, error) {
			parts := make([]string, len(args))
			for i, a := range args {
				parts[i] = fmt.Sprint(a)
			}
			fmt.Println(strings.Join(parts, " "))
			return nil, nil
		}); err != nil {
			panic(err)
		}
		if err := m.RegisterHostFunc("readFile", func(args []any) (any, error) {
			return readFile(fmt.Sprint(args[0])), nil
		}); err != nil {
			panic(err)
		}
		if _, err := m.Eval(string(src), quickjs.EvalGlobal); err != nil {
			fmt.Println("JSERROR", err)
			os.Exit(1)
		}
	}
	fmt.Fprintf(os.Stderr, "total %v\n", time.Since(start).Round(time.Millisecond))
}
