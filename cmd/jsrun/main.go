// Command jsrun runs JavaScript files (or -e code) with the jsc2go VM, like
// JSC's jsc shell.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"

	"github.com/baslentfert/jsc2go/vm"
)

func main() {
	expr := flag.String("e", "", "evaluate `code`")
	module := flag.Bool("module", false, "run files as modules (the default for .mjs files)")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile to `file`")
	memStats := flag.Bool("memstats", false, "print allocation statistics at exit")
	memProfile := flag.String("memprofile", "", "write an allocation profile to `file`")
	root := flag.String("root", "", "change to `dir` after reading the files (readFile paths are relative to it)")
	minHeap := flag.Uint64("minheap", 16, "let the heap grow to `MB` megabytes before the first collections (vm.SetMinHeap; 0: Go's default pacing)")
	flag.Parse()
	vm.SetMinHeap(*minHeap << 20)
	if *memProfile != "" {
		runtime.MemProfileRate = 4096
	}
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	v := vm.New()
	v.Print = func(s string) { fmt.Println(s) }
	// readFile(path) like the jsc shell.
	r := v.Realm()
	r.DefineFunction(r.GlobalObject, "readFile", 1, func(c *vm.Call) vm.Value {
		b, err := os.ReadFile(c.VM.ToString(c.Arg(0)).String())
		if err != nil {
			return vm.Undefined
		}
		return vm.Str(string(b))
	})
	v.HostHooks.LoadModule = func(specifier, referrer string) (string, string, error) {
		name := filepath.Clean(filepath.Join(filepath.Dir(referrer), specifier))
		b, err := os.ReadFile(name)
		return string(b), name, err
	}

	run := func(src, name string) bool {
		var err error
		if *module || strings.HasSuffix(name, ".mjs") {
			var p *vm.Object
			if p, err = v.RunModule(src, filepath.Clean(name)); err == nil {
				if exc := v.RunJobs(); exc != nil {
					err = exc
				} else if settled, rejected, value := vm.PromiseResult(p); settled && rejected {
					err = &vm.Exception{Value: value}
				}
			}
		} else if _, err = v.Eval(src, name); err == nil {
			if exc := v.RunJobs(); exc != nil {
				err = exc
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return false
		}
		return true
	}
	sources := make([]string, len(flag.Args()))
	for i, file := range flag.Args() {
		src, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		sources[i] = string(src)
	}
	if *root != "" {
		if err := os.Chdir(*root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	ok := true
	if *expr != "" {
		ok = run(*expr, "<eval>")
	}
	for i, file := range flag.Args() {
		if !run(sources[i], file) {
			ok = false
		}
	}
	if *memProfile != "" {
		if f, err := os.Create(*memProfile); err == nil {
			pprof.Lookup("allocs").WriteTo(f, 0)
			f.Close()
		}
	}
	if *memStats {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fmt.Fprintf(os.Stderr, "allocated %d MB in %d objects, %d GCs\n", ms.TotalAlloc>>20, ms.Mallocs, ms.NumGC)
	}
	if !ok {
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}
