// Command jetstream runs JetStream2's JavaScript benchmarks and scores them
// like JetStream2's driver: per benchmark the First iteration, the mean of
// the worst iterations and the Average (score = 5000 / ms), combined by
// geomean.
//
// jsc2go runs through cmd/jsrun. The other engines (goja, sobek, quickjs,
// ramune) run through the tools of the separate bench/engines module, which
// bench/build-engines.sh builds; only that downloads them.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type plan struct {
	Name                string            `json:"name"`
	Files               []string          `json:"files"`
	Preload             map[string]string `json:"preload"`
	Iterations          int               `json:"iterations"`
	WorstCaseCount      int               `json:"worstCaseCount"`
	DeterministicRandom bool              `json:"deterministicRandom"`
	Kind                string            `json:"kind"`
	Group               string            `json:"group"`
}

type result struct {
	Engine, Bench                string
	First, Worst, Average, Score float64
	Times                        []float64
	Err                          string
	Wall                         time.Duration
}

const deterministicRandom = `
Math.random = (function() {
    var seed = 49734321;
    return function() {
        // Robert Jenkins' 32 bit integer hash function.
        seed = ((seed + 0x7ed55d16) + (seed << 12))  & 0xffffffff;
        seed = ((seed ^ 0xc761c23c) ^ (seed >>> 19)) & 0xffffffff;
        seed = ((seed + 0x165667b1) + (seed << 5))   & 0xffffffff;
        seed = ((seed + 0xd3a2646c) ^ (seed << 9))   & 0xffffffff;
        seed = ((seed + 0xfd7046c5) + (seed << 3))   & 0xffffffff;
        seed = ((seed ^ 0xb55a4f09) ^ (seed >>> 16)) & 0xffffffff;
        return (seed & 0xfffffff) / 0x10000000;
    };
})();
`

func script(root string, p plan, iterations int) (string, error) {
	var b strings.Builder
	b.WriteString("const isInBrowser = false; let performance = {now: Date.now.bind(Date)};\n")
	// Like JetStream's runCode: console is a property of the global object
	// (benchmarks may declare their own), plus the shell functions some
	// benchmarks expect (d8's read, jsc's preciseTime).
	b.WriteString("globalThis.console = {log: print};\n")
	b.WriteString("globalThis.read = readFile; globalThis.preciseTime = function() { return Date.now() / 1000; };\n")
	b.WriteString("var top = {currentResolve: function(r) { print('JSRESULT ' + JSON.stringify(r)); }, currentReject: function(e) { print('JSERROR ' + e); }};\n")
	if p.DeterministicRandom {
		b.WriteString(deterministicRandom)
	}
	keys := make([]string, 0, len(p.Preload))
	for k := range p.Preload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "const %s = %q;\n", k, p.Preload[k])
	}
	for _, f := range p.Files {
		src, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return "", err
		}
		b.Write(src)
		b.WriteString("\n;\n")
	}
	if p.Kind == "AsyncBenchmark" {
		fmt.Fprintf(&b, `
async function doRun() {
    let __benchmark = new Benchmark();
    let results = [];
    for (let i = 0; i < %d; i++) {
        let start = Date.now();
        await __benchmark.runIteration();
        let end = Date.now();
        results.push(Math.max(1, end - start));
    }
    if (__benchmark.validate)
        __benchmark.validate();
    top.currentResolve(results);
}
doRun().catch(function(e) { print('JSERROR ' + e); });
`, iterations)
	} else {
		fmt.Fprintf(&b, `
let __benchmark = new Benchmark(%d);
let results = [];
for (let i = 0; i < %d; i++) {
    if (__benchmark.prepareForNextIteration)
        __benchmark.prepareForNextIteration();
    let start = Date.now();
    __benchmark.runIteration();
    let end = Date.now();
    results.push(Math.max(1, end - start));
}
if (__benchmark.validate)
    __benchmark.validate();
top.currentResolve(results);
`, iterations, iterations)
	}
	return b.String(), nil
}

func toScore(ms float64) float64 { return 5000 / math.Max(ms, 1) }

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func geomean(xs []float64) float64 {
	p := 0.0
	for _, x := range xs {
		p += math.Log(x)
	}
	return math.Exp(p / float64(len(xs)))
}

func score(r *result, worstCount int) {
	t := r.Times
	r.First = toScore(t[0])
	rest := append([]float64(nil), t[1:]...)
	sort.Sort(sort.Reverse(sort.Float64Slice(rest)))
	worstCount = min(worstCount, len(rest))
	r.Worst = toScore(mean(rest[:worstCount]))
	r.Average = toScore(mean(rest))
	r.Score = geomean([]float64{r.First, r.Worst, r.Average})
}

func main() {
	root := flag.String("root", "", "JetStream2 directory")
	plans := flag.String("plans", "bench/jetstream/plans.json", "test plans (JSON, made by plans_extract.js from JetStreamDriver.js)")
	engines := flag.String("engines", "jsc2go", "engines to run (jsc2go, quickjs, goja, sobek, ramune)")
	only := flag.String("only", "", "comma-separated benchmark names (default: all JS benchmarks)")
	maxIter := flag.Int("iterations", 15, "cap on iterations per benchmark")
	timeout := flag.Duration("timeout", 10*time.Minute, "per benchmark and engine")
	out := flag.String("out", "bench/results.json", "results file (rewritten after every benchmark)")
	bin := flag.String("bin", "bench/bin", "directory with jsrun and, for the other engines, jsbench and ramunebench")
	dump := flag.String("dump", "", "only write the generated scripts to `dir`")
	flag.Parse()
	if abs, err := filepath.Abs(*bin); err == nil {
		*bin = abs
	}

	data, err := os.ReadFile(*plans)
	if err != nil {
		panic(err)
	}
	var all []plan
	if err := json.Unmarshal(data, &all); err != nil {
		panic(err)
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n != "" {
			want[n] = true
		}
	}
	tmp, _ := os.MkdirTemp("", "jetstream")
	defer os.RemoveAll(tmp)

	var results []result
	for _, p := range all {
		if p.Kind != "DefaultBenchmark" && p.Kind != "AsyncBenchmark" || p.Group == "WorkerTests" || p.Group == "LuaJSFight" {
			continue
		}
		if len(want) > 0 && !want[p.Name] {
			continue
		}
		iters := min(p.Iterations, *maxIter)
		src, err := script(*root, p, iters)
		if err != nil {
			fmt.Fprintln(os.Stderr, p.Name, err)
			continue
		}
		file := filepath.Join(tmp, p.Name+".js")
		os.WriteFile(file, []byte(src), 0o644)
		if *dump != "" {
			os.MkdirAll(*dump, 0o755)
			os.WriteFile(filepath.Join(*dump, p.Name+".js"), []byte(src), 0o644)
			continue
		}
		for _, e := range strings.Split(*engines, ",") {
			r := run(*bin, e, *root, file, *timeout)
			r.Bench = p.Name
			if r.Err == "" {
				score(&r, p.WorstCaseCount)
			}
			results = append(results, r)
			if r.Err != "" {
				fmt.Printf("%-28s %-8s ERROR %s\n", p.Name, e, r.Err)
			} else {
				fmt.Printf("%-28s %-8s score %8.2f  (first %.1f worst %.1f avg %.1f)  %v\n", p.Name, e, r.Score, r.First, r.Worst, r.Average, r.Wall.Round(time.Millisecond))
			}
			b, _ := json.MarshalIndent(results, "", " ")
			os.WriteFile(*out, b, 0o644)
		}
	}
}

func run(bin, engine, root, file string, timeout time.Duration) result {
	r := result{Engine: engine}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	switch engine {
	case "jsc2go":
		cmd = exec.CommandContext(ctx, tool(bin, "jsrun"), "-root", root, file)
	case "ramune":
		cmd = exec.CommandContext(ctx, tool(bin, "ramunebench"), "-root", root, file)
	default:
		cmd = exec.CommandContext(ctx, tool(bin, "jsbench"), "-engine", engine, "-root", root, file)
	}
	start := time.Now()
	outb, err := cmd.Output()
	r.Wall = time.Since(start)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		r.Err = "timeout"
		return r
	}
	sc := bufio.NewScanner(strings.NewReader(string(outb)))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := sc.Text()
		if t, ok := strings.CutPrefix(line, "JSRESULT "); ok {
			if json.Unmarshal([]byte(t), &r.Times) == nil && len(r.Times) > 0 {
				return r
			}
		}
		if t, ok := strings.CutPrefix(line, "JSERROR "); ok {
			r.Err = firstLine(t)
			return r
		}
	}
	if err != nil {
		r.Err = firstLine(err.Error())
	} else {
		r.Err = "no result"
	}
	return r
}

// tool is the path of a runner binary in bin.
func tool(bin, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(bin, name)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
