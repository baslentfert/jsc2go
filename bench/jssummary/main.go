// Command jssummary summarizes jetstream results: per engine the geomean
// score over the benchmarks every engine completed, plus failures.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

type result struct {
	Engine, Bench string
	Score         float64
	Err           string
}

func main() {
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var rs []result
	json.Unmarshal(data, &rs)
	engines := []string{}
	seen := map[string]bool{}
	byBench := map[string]map[string]result{}
	var benches []string
	for _, r := range rs {
		if !seen[r.Engine] {
			seen[r.Engine] = true
			engines = append(engines, r.Engine)
		}
		if byBench[r.Bench] == nil {
			byBench[r.Bench] = map[string]result{}
			benches = append(benches, r.Bench)
		}
		byBench[r.Bench][r.Engine] = r
	}
	fmt.Printf("%-28s", "benchmark")
	for _, e := range engines {
		fmt.Printf(" %9s", e)
	}
	fmt.Println()
	common := []string{}
	for _, b := range benches {
		fmt.Printf("%-28s", b)
		ok := len(byBench[b]) == len(engines)
		for _, e := range engines {
			r, has := byBench[b][e]
			switch {
			case !has:
				fmt.Printf(" %9s", "-")
				ok = false
			case r.Err != "":
				msg := r.Err
				if len(msg) > 9 {
					msg = msg[:9]
				}
				fmt.Printf(" %9s", msg)
				ok = false
			default:
				fmt.Printf(" %9.2f", r.Score)
			}
		}
		fmt.Println()
		if ok {
			common = append(common, b)
		}
	}
	fmt.Printf("\nGeomean over the %d benchmarks every engine completed:\n", len(common))
	type tot struct {
		e string
		g float64
	}
	var tots []tot
	for _, e := range engines {
		s := 0.0
		for _, b := range common {
			s += math.Log(byBench[b][e].Score)
		}
		tots = append(tots, tot{e, math.Exp(s / float64(len(common)))})
	}
	sort.Slice(tots, func(i, j int) bool { return tots[i].g > tots[j].g })
	for _, t := range tots {
		done, failed := 0, 0
		for _, b := range benches {
			if r, ok := byBench[b][t.e]; ok {
				if r.Err == "" {
					done++
				} else {
					failed++
				}
			}
		}
		fmt.Printf("  %-8s %7.2f   (completed %d, failed %d)\n", t.e, t.g, done, failed)
	}
}
