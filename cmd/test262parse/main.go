// Command test262parse runs the test262 suite against the parser only: a test
// passes if it fails to parse exactly when it expects a parse-phase error.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/baslentfert/jsc2go/parser"
)

type metadata struct {
	flags         map[string]bool
	features      []string
	negativePhase string
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
	lines := strings.Split(m[1], "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "flags:"):
			for _, f := range splitList(line) {
				md.flags[f] = true
			}
		case strings.HasPrefix(line, "features:"):
			md.features = splitList(line)
			if len(md.features) == 0 { // YAML block list
				for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "- ") {
					i++
					md.features = append(md.features, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "- ")))
				}
			}
		case strings.HasPrefix(line, "negative:"):
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t")) {
				i++
				if p, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "phase:"); ok {
					md.negativePhase = strings.TrimSpace(p)
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
	mode    string
	pass    bool
	message string
}

func run(path string, skipFeatures map[string]bool) []result {
	data, err := os.ReadFile(path)
	if err != nil {
		return []result{{path: path, message: err.Error()}}
	}
	src := string(data)
	md := parseMetadata(src)
	for _, f := range md.features {
		if skipFeatures[f] {
			return nil
		}
	}
	expectParseError := md.negativePhase == "parse"

	check := func(mode, code string, module bool) result {
		provider := parser.NewSourceProvider(code, path, parser.SourceTypeProgram)
		sc := parser.NewSourceCode(provider)
		var perr *parser.ParserError
		func() {
			defer func() {
				if r := recover(); r != nil {
					perr = &parser.ParserError{Message: fmt.Sprintf("PANIC: %v", r)}
				}
			}()
			if module {
				_, perr = parser.ParseModule(sc, parser.ModuleEvaluateMode)
			} else {
				_, perr = parser.ParseProgram(sc, parser.NoLexicallyScopedFeatures)
			}
		}()
		gotError := perr != nil
		r := result{path: path, mode: mode, pass: gotError == expectParseError}
		if perr != nil {
			r.message = perr.Message
			if strings.HasPrefix(perr.Message, "PANIC") {
				r.pass = false
			}
		} else {
			r.message = "parsed successfully"
		}
		return r
	}

	var results []result
	switch {
	case md.flags["module"]:
		results = append(results, check("module", src, true))
	case md.flags["raw"]:
		results = append(results, check("raw", src, false))
	default:
		if !md.flags["onlyStrict"] {
			results = append(results, check("sloppy", src, false))
		}
		if !md.flags["noStrict"] {
			results = append(results, check("strict", "\"use strict\";\n"+src, false))
		}
	}
	return results
}

func main() {
	root := flag.String("root", "test262/test", "test262 test directory")
	out := flag.String("failures", "", "write failing tests to this file")
	skip := flag.String("skip-features", "", "comma-separated test262 features to skip")
	flag.Parse()

	skipFeatures := map[string]bool{}
	for _, f := range strings.Split(*skip, ",") {
		if f != "" {
			skipFeatures[f] = true
		}
	}

	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = []string{*root}
	}
	var paths []string
	for _, d := range dirs {
		filepath.WalkDir(d, func(path string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.HasSuffix(path, ".js") && !strings.Contains(path, "_FIXTURE") {
				paths = append(paths, path)
			}
			return nil
		})
	}
	sort.Strings(paths)

	jobs := make(chan string)
	results := make(chan []result)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				results <- run(p, skipFeatures)
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	type tally struct{ pass, fail int }
	byDir := map[string]*tally{}
	var failures []result
	var total tally
	for rs := range results {
		for _, r := range rs {
			dir := filepath.ToSlash(filepath.Dir(r.path))
			parts := strings.Split(dir, "/")
			if len(parts) > 4 {
				dir = strings.Join(parts[:5], "/")
			}
			t := byDir[dir]
			if t == nil {
				t = &tally{}
				byDir[dir] = t
			}
			if r.pass {
				t.pass++
				total.pass++
			} else {
				t.fail++
				total.fail++
				failures = append(failures, r)
			}
		}
	}

	keys := make([]string, 0, len(byDir))
	for k := range byDir {
		if byDir[k].fail > 0 {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int { return byDir[b].fail - byDir[a].fail })
	for i, k := range keys {
		if i == 40 {
			break
		}
		fmt.Printf("%6d failing / %6d  %s\n", byDir[k].fail, byDir[k].pass+byDir[k].fail, k)
	}
	fmt.Printf("\nTOTAL: %d passed, %d failed (%.2f%%)\n", total.pass, total.fail, 100*float64(total.pass)/float64(total.pass+total.fail))

	if *out != "" {
		sort.Slice(failures, func(i, j int) bool { return failures[i].path < failures[j].path })
		f, err := os.Create(*out)
		if err == nil {
			w := bufio.NewWriter(f)
			for _, r := range failures {
				fmt.Fprintf(w, "%s [%s]: %s\n", r.path, r.mode, r.message)
			}
			w.Flush()
			f.Close()
		}
	}
}
