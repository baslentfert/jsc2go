// Command compare compares JetStream2 results (bench/jetstream -out files)
// with the Mac baseline in bench/vps/baseline: per benchmark the scores of
// this machine and of the Mac, and the geomeans.
//
//	go run ./bench/vps/compare -run out/jit.json -nojit out/nojit.json [-others out/others.json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type result struct {
	Engine, Bench string
	Score         float64
	Err           string
}

// scores reads a results file: score by engine and benchmark (failures
// left out).
func scores(path string) map[string]map[string]float64 {
	m := map[string]map[string]float64{}
	if path == "" {
		return m
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var rs []result
	if err := json.Unmarshal(data, &rs); err != nil {
		fmt.Fprintln(os.Stderr, path, err)
		os.Exit(1)
	}
	for _, r := range rs {
		if r.Err != "" || r.Score <= 0 {
			continue
		}
		if m[r.Engine] == nil {
			m[r.Engine] = map[string]float64{}
		}
		m[r.Engine][r.Bench] = r.Score
	}
	return m
}

func geomean(s map[string]float64, benches []string) (float64, int) {
	sum, n := 0.0, 0
	for _, b := range benches {
		if v, ok := s[b]; ok {
			sum += math.Log(v)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return math.Exp(sum / float64(n)), n
}

func main() {
	run := flag.String("run", "", "results of jsc2go with the JIT (required)")
	nojit := flag.String("nojit", "", "results of jsc2go with JSC2GO_JIT=0")
	others := flag.String("others", "", "results of the other engines on this machine")
	base := flag.String("baseline", "bench/vps/baseline/mac-arm64.json", "the Mac's results")
	list := flag.Bool("list", false, "print the benchmarks run.sh measures, comma-separated")
	flag.Parse()
	if *list {
		printList()
		return
	}
	if *run == "" {
		flag.Usage()
		os.Exit(2)
	}
	here := scores(*run)["jsc2go"]
	hereNoJIT := scores(*nojit)["jsc2go"]
	mac := scores(*base)
	otherEngines := scores(*others)

	var benches []string
	for b := range here {
		benches = append(benches, b)
	}
	sort.Strings(benches)

	fmt.Printf("%-26s %9s %9s %7s %9s %9s %7s\n", "benchmark", "jit", "nojit", "jit/no", "mac jit", "mac nojit", "here/mac")
	for _, b := range benches {
		j, nj := here[b], hereNoJIT[b]
		mj, mnj := mac["jsc2go"][b], mac["jsc2go-nojit"][b]
		ratio := func(a, c float64) string {
			if a == 0 || c == 0 {
				return "-"
			}
			return fmt.Sprintf("%.2f", a/c)
		}
		num := func(v float64) string {
			if v == 0 {
				return "-"
			}
			return fmt.Sprintf("%.2f", v)
		}
		fmt.Printf("%-26s %9s %9s %7s %9s %9s %7s\n", b, num(j), num(nj), ratio(j, nj), num(mj), num(mnj), ratio(j, mj))
	}

	fmt.Println()
	g, n := geomean(here, benches)
	fmt.Printf("geomean here, JIT:      %7.2f over %d benchmarks\n", g, n)
	if len(hereNoJIT) > 0 {
		gn, nn := geomean(hereNoJIT, benches)
		fmt.Printf("geomean here, no JIT:   %7.2f over %d (JIT %.2fx)\n", gn, nn, g/gn)
	}
	gm, nm := geomean(mac["jsc2go"], benches)
	fmt.Printf("geomean Mac, JIT:       %7.2f over %d (here/Mac %.2f)\n", gm, nm, g/gm)

	// Standing against the other engines: on this machine if they ran here,
	// else (less meaningful) against their Mac results.
	src, label := otherEngines, "this machine"
	if len(src) == 0 {
		src, label = mac, "the Mac (no other engines ran here)"
		g = gm
		here = mac["jsc2go"]
	}
	fmt.Printf("\njsc2go (JIT) against the other engines on %s:\n", label)
	for _, e := range []string{"quickjs", "goja", "sobek", "ramune"} {
		s := src[e]
		var common []string
		faster := 0
		for _, b := range benches {
			if _, ok := s[b]; ok {
				if _, ok := here[b]; ok {
					common = append(common, b)
					if here[b] > s[b] {
						faster++
					}
				}
			}
		}
		if len(common) == 0 {
			continue
		}
		gj, _ := geomean(here, common)
		ge, _ := geomean(s, common)
		fmt.Printf("  vs %-8s %+5.0f%% geomean, faster on %d of %d\n", e, (gj/ge-1)*100, faster, len(common))
	}
}

// printList prints JetStream2's JavaScript benchmarks (those bench/jetstream
// runs by default) without octane-zlib and float-mm.c, which take minutes
// per iteration: the set the Mac baseline has.
func printList() {
	data, err := os.ReadFile("bench/jetstream/plans.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var plans []struct{ Name, Kind, Group string }
	if err := json.Unmarshal(data, &plans); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var names []string
	for _, p := range plans {
		if p.Kind != "DefaultBenchmark" && p.Kind != "AsyncBenchmark" || p.Group == "WorkerTests" ||
			p.Group == "LuaJSFight" || p.Name == "octane-zlib" || p.Name == "float-mm.c" {
			continue
		}
		names = append(names, p.Name)
	}
	fmt.Println(strings.Join(names, ","))
}
