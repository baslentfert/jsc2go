// Command test262 runs the test262 conformance suite against the jsc2go
// interpreter, like JSC's test262-runner.
//
//	go run ./cmd/test262 [-j N] [-skip-features a,b] [-v] test262/test/built-ins/Array
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baslentfert/jsc2go/vm"
)

type metadata struct {
	includes      []string
	flags         map[string]bool
	features      []string
	negativePhase string
	negativeType  string
}

var (
	frontMatterRe = regexp.MustCompile(`(?s)/\*---(.*?)---\*/`)
	listRe        = regexp.MustCompile(`\[(.*?)\]`)
)

func parseMetadata(src string) metadata {
	md := metadata{flags: map[string]bool{}}
	m := frontMatterRe.FindStringSubmatch(src)
	if m == nil {
		return md
	}
	lines := strings.Split(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(m[1]), "\n")
	blockList := func(i *int) []string {
		var out []string
		for *i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[*i+1]), "- ") {
			*i++
			out = append(out, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[*i]), "- ")))
		}
		return out
	}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "flags:"):
			flags := splitList(line)
			if len(flags) == 0 {
				flags = blockList(&i)
			}
			for _, f := range flags {
				md.flags[f] = true
			}
		case strings.HasPrefix(line, "features:"):
			md.features = splitList(line)
			if len(md.features) == 0 {
				md.features = blockList(&i)
			}
		case strings.HasPrefix(line, "includes:"):
			md.includes = splitList(line)
			if len(md.includes) == 0 {
				md.includes = blockList(&i)
			}
		case strings.HasPrefix(line, "negative:"):
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t")) {
				i++
				t := strings.TrimSpace(lines[i])
				if p, ok := strings.CutPrefix(t, "phase:"); ok {
					md.negativePhase = strings.TrimSpace(p)
				}
				if p, ok := strings.CutPrefix(t, "type:"); ok {
					md.negativeType = strings.TrimSpace(p)
				}
			}
		}
	}
	return md
}

func splitList(line string) []string {
	m := listRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	var out []string
	for _, f := range strings.Split(m[1], ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

type result struct {
	path    string
	outcome string // pass, fail, skip, crash, timeout
	detail  string
}

var harnessCache sync.Map

func harnessFile(root, name string) string {
	if s, ok := harnessCache.Load(name); ok {
		return s.(string)
	}
	b, err := os.ReadFile(filepath.Join(root, "harness", name))
	if err != nil {
		panic(fmt.Sprintf("missing harness file %s: %v", name, err))
	}
	harnessCache.Store(name, string(b))
	return string(b)
}

type runner struct {
	root         string
	skipFeatures map[string]bool
	skipFiles    []string // path suffixes, with forward slashes
	timeout      time.Duration
}

func (r *runner) run(path string) []result {
	srcBytes, err := os.ReadFile(path)
	if err != nil {
		return []result{{path, "fail", err.Error()}}
	}
	for _, f := range r.skipFiles {
		if strings.HasSuffix(filepath.ToSlash(path), f) {
			return []result{{path, "skip", "file"}}
		}
	}
	src := string(srcBytes)
	md := parseMetadata(src)
	for _, f := range md.features {
		if r.skipFeatures[f] {
			return []result{{path, "skip", "feature " + f}}
		}
	}
	var modes []bool // strict?
	switch {
	case md.flags["module"]:
		modes = []bool{true}
	case md.flags["onlyStrict"]:
		modes = []bool{true}
	case md.flags["noStrict"], md.flags["raw"]:
		modes = []bool{false}
	default:
		modes = []bool{false, true}
	}
	var results []result
	for _, strict := range modes {
		res := r.runOne(path, src, md, strict)
		if strict && len(modes) > 1 {
			res.path += " (strict)"
		}
		results = append(results, res)
		if res.outcome != "pass" {
			break
		}
	}
	return results
}

func (r *runner) runOne(path, src string, md metadata, strict bool) (res result) {
	res.path = path
	v := vm.New()
	var output []string
	var mu sync.Mutex
	v.Print = func(s string) {
		mu.Lock()
		output = append(output, s)
		mu.Unlock()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				if p == vm.ErrInterrupted {
					res.outcome, res.detail = "timeout", ""
					return
				}
				stack := string(debug.Stack())
				if lines := strings.Split(stack, "\n"); len(lines) > 30 {
					stack = strings.Join(lines[:30], "\n")
				}
				res.outcome, res.detail = "crash", fmt.Sprintf("%v\n%s", p, stack)
			}
		}()
		res.outcome, res.detail = r.execute(v, path, src, md, strict, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), output...)
		})
	}()
	select {
	case <-done:
	case <-time.After(r.timeout):
		v.Interrupt()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return result{path, "timeout", ""}
	}
	return res
}

func (r *runner) execute(v *vm.VM, path, src string, md metadata, strict bool, output func() []string) (string, string) {
	v.CanBlock = !md.flags["CanBlockIsFalse"]
	v.HostHooks.LoadModule = loadModule(filepath.Clean(path))
	hub := newAgentHub(r.timeout)
	defer hub.shutdown()
	installHost(v, v.Realm(), hub)
	if !md.flags["raw"] {
		includes := []string{"assert.js", "sta.js"}
		if md.flags["async"] {
			includes = append(includes, "doneprintHandle.js")
		}
		includes = append(includes, md.includes...)
		for _, inc := range includes {
			if _, err := v.Eval(harnessFile(r.root, inc), inc); err != nil {
				return "fail", "harness " + inc + ": " + err.Error()
			}
		}
	}
	if md.flags["module"] {
		return r.executeModule(v, path, src, md, output)
	}
	if strict {
		src = "\"use strict\";\n" + src
	}
	script, err := v.CompileScript(src, path)
	if err != nil {
		if md.negativePhase == "parse" || md.negativePhase == "early" {
			if errorName(v, err) == md.negativeType {
				return "pass", ""
			}
			return "fail", "expected " + md.negativeType + ", got " + err.Error()
		}
		return "fail", "compile: " + err.Error()
	}
	if md.negativePhase == "parse" || md.negativePhase == "early" {
		return "fail", "expected early " + md.negativeType + " but compiled"
	}
	_, err = v.RunScript(script)
	if err == nil {
		if exc := v.RunJobs(); exc != nil {
			err = exc
		}
	}
	if md.negativePhase == "runtime" || md.negativePhase == "resolution" {
		if err != nil && errorName(v, err) == md.negativeType {
			return "pass", ""
		}
		if err == nil {
			return "fail", "expected runtime " + md.negativeType
		}
		return "fail", "expected " + md.negativeType + ", got " + err.Error()
	}
	if err != nil {
		return "fail", err.Error()
	}
	if md.flags["async"] {
		out := output()
		for _, line := range out {
			if line == "Test262:AsyncTestComplete" {
				return "pass", ""
			}
			if strings.HasPrefix(line, "Test262:AsyncTestFailure:") {
				return "fail", line
			}
		}
		return "fail", "async test did not complete: " + strings.Join(out, "\n")
	}
	return "pass", ""
}

// errorName returns the name of the constructor of the thrown value.
func errorName(v *vm.VM, err error) string {
	exc, ok := err.(*vm.Exception)
	if !ok {
		return ""
	}
	o := exc.Value.AsObject()
	if o == nil {
		return ""
	}
	var name string
	v.Try(func() {
		c := o.GetStr("constructor").AsObject()
		if c != nil {
			if n := c.GetStr("name"); n.IsString() {
				name = n.StringRef().GoString()
			}
		}
	})
	return name
}

// installHost defines print and $262 in realm r.
func installHost(v *vm.VM, r *vm.Realm, hub *agentHub) *vm.Object {
	g := r.GlobalObject
	host := r.NewObject()
	host.CreateDataProperty(vm.StrKey("global"), vm.ObjectValue(r.GlobalThis))
	r.DefineFunction(host, "createRealm", 0, func(c *vm.Call) vm.Value {
		nr := v.NewRealm()
		return vm.ObjectValue(installHost(v, nr, hub))
	})
	r.DefineFunction(host, "evalScript", 1, func(c *vm.Call) vm.Value {
		src := c.VM.ToString(c.Arg(0)).String()
		var result vm.Value
		r.Enter(func() {
			s, err := v.CompileScript(src, "evalScript")
			if err != nil {
				panic(err)
			}
			result = r.RunScript(s)
		})
		return result
	})
	r.DefineFunction(host, "detachArrayBuffer", 1, func(c *vm.Call) vm.Value {
		return c.VM.DetachArrayBuffer(c.Arg(0))
	})
	r.DefineFunction(g, "setTimeout", 2, func(c *vm.Call) vm.Value {
		c.VM.SetTimeout(c.Arg(0), c.VM.ToNumber(c.Arg(1)))
		return vm.Undefined
	})
	host.CreateDataProperty(vm.StrKey("IsHTMLDDA"), vm.ObjectValue(r.NewHTMLDDA()))
	r.DefineFunction(host, "gc", 0, func(c *vm.Call) vm.Value { return vm.Undefined })
	agent := r.NewObject()
	hub.install(r, agent)
	host.CreateDataProperty(vm.StrKey("agent"), vm.ObjectValue(agent))
	g.CreateDataProperty(vm.StrKey("$262"), vm.ObjectValue(host))
	return host
}

func main() {
	jobs := flag.Int("j", runtime.NumCPU(), "parallel jobs")
	skip := flag.String("skip-features", "", "comma-separated features to skip")
	skipFiles := flag.String("skip-files", "", "comma-separated test files to skip (path suffixes, like built-ins/X/y.js)")
	verbose := flag.Bool("v", false, "print failure details")
	timeout := flag.Duration("timeout", 20*time.Second, "per-test timeout")
	failuresFile := flag.String("failures", "", "write failing test paths to `file`")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile to `file`")
	flag.Parse()
	if *cpuProfile != "" {
		if f, err := os.Create(*cpuProfile); err == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}

	r := &runner{skipFeatures: map[string]bool{}, timeout: *timeout}
	for _, f := range strings.Split(*skip, ",") {
		if f != "" {
			r.skipFeatures[f] = true
		}
	}
	for _, f := range strings.Split(*skipFiles, ",") {
		if f != "" {
			r.skipFiles = append(r.skipFiles, "/"+strings.TrimPrefix(f, "/"))
		}
	}
	var paths []string
	for _, arg := range flag.Args() {
		if r.root == "" {
			abs, _ := filepath.Abs(arg)
			for dir := abs; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
				if _, err := os.Stat(filepath.Join(dir, "harness", "assert.js")); err == nil {
					r.root = dir
					break
				}
			}
		}
		filepath.WalkDir(arg, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") && !strings.Contains(p, "_FIXTURE") {
				paths = append(paths, p)
			}
			return nil
		})
	}
	if r.root == "" {
		fmt.Fprintln(os.Stderr, "cannot find the test262 root (harness/assert.js)")
		os.Exit(2)
	}
	sort.Strings(paths)

	results := make([][]result, len(paths))
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for range *jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(paths) {
					return
				}
				results[i] = r.run(paths[i])
			}
		}()
	}
	wg.Wait()

	counts := map[string]int{}
	var failures []string
	for _, rs := range results {
		for _, res := range rs {
			counts[res.outcome]++
			if res.outcome != "pass" && res.outcome != "skip" {
				failures = append(failures, res.path)
				if *verbose {
					fmt.Printf("%s %s\n", strings.ToUpper(res.outcome), res.path)
					if res.detail != "" {
						fmt.Printf("    %s\n", strings.ReplaceAll(res.detail, "\n", "\n    "))
					}
				}
			}
		}
	}
	total := counts["pass"] + counts["fail"] + counts["crash"] + counts["timeout"]
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(counts["pass"]) / float64(total)
	}
	fmt.Printf("%d files, %d runs: %d pass (%.1f%%), %d fail, %d crash, %d timeout, %d skipped in %s\n",
		len(paths), total, counts["pass"], pct, counts["fail"], counts["crash"], counts["timeout"], counts["skip"],
		time.Since(start).Round(time.Millisecond))
	if *failuresFile != "" {
		os.WriteFile(*failuresFile, []byte(strings.Join(failures, "\n")+"\n"), 0o644)
	}
}

// loadModule resolves specifier relative to the importing file (or the test
// file for scripts) and reads it.
func loadModule(testPath string) func(specifier, referrer string) (string, string, error) {
	return func(specifier, referrer string) (string, string, error) {
		if referrer == "" {
			referrer = testPath
		}
		name := filepath.Clean(filepath.Join(filepath.Dir(referrer), specifier))
		b, err := os.ReadFile(name)
		if err != nil {
			return "", "", err
		}
		return string(b), name, nil
	}
}

// executeModule runs a test flagged module.
func (r *runner) executeModule(v *vm.VM, path, src string, md metadata, output func() []string) (string, string) {
	promise, err := v.RunModule(src, filepath.Clean(path))
	if err != nil {
		if md.negativePhase != "" {
			if errorName(v, err) == md.negativeType {
				return "pass", ""
			}
			return "fail", "expected " + md.negativeType + ", got " + err.Error()
		}
		return "fail", "module: " + err.Error()
	}
	if md.negativePhase == "parse" || md.negativePhase == "early" || md.negativePhase == "resolution" {
		return "fail", "expected " + md.negativePhase + " " + md.negativeType + " but the module linked"
	}
	if exc := v.RunJobs(); exc != nil {
		err = exc
	} else if settled, rejected, value := vm.PromiseResult(promise); settled && rejected {
		err = &vm.Exception{Value: value}
	}
	if md.negativePhase == "runtime" {
		if err != nil && errorName(v, err) == md.negativeType {
			return "pass", ""
		}
		if err == nil {
			return "fail", "expected runtime " + md.negativeType
		}
		return "fail", "expected " + md.negativeType + ", got " + err.Error()
	}
	if err != nil {
		return "fail", err.Error()
	}
	if md.flags["async"] {
		out := output()
		for _, line := range out {
			if line == "Test262:AsyncTestComplete" {
				return "pass", ""
			}
			if strings.HasPrefix(line, "Test262:AsyncTestFailure:") {
				return "fail", line
			}
		}
		return "fail", "async test did not complete: " + strings.Join(out, "\n")
	}
	return "pass", ""
}
