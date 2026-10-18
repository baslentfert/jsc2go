// Command test262compile compiles every test262 test (and all nested
// functions) to bytecode and validates the result; it checks the bytecode
// generator for crashes and malformed output before the interpreter exists.
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
	"sort"
	"strings"
	"sync"

	"runtime/debug"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
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

// compileAll compiles cb's nested functions recursively and validates every
// code block.
func compileAll(cb *bytecode.UnlinkedCodeBlock, name string, count *int, recurse bool) error {
	*count++
	if err := bytecode.Validate(cb); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	if !recurse {
		return nil
	}
	for _, list := range [][]*bytecode.UnlinkedFunctionExecutable{cb.FunctionDecls, cb.FunctionExprs} {
		for _, fn := range list {
			kinds := []bytecode.CodeSpecializationKind{bytecode.CodeForCall}
			if fn.ConstructAbility == bytecode.CanConstruct {
				kinds = append(kinds, bytecode.CodeForConstruct)
			}
			for _, kind := range kinds {
				fcb, err := bytecompiler.CodeBlockForFunction(fn, kind, 0)
				if err != nil {
					return fmt.Errorf("%s/%s: %v", name, fn.EcmaName(), err)
				}
				// Recurse from the call variant only: recursing from both
				// is exponential in the nesting depth.
				if err := compileAll(fcb, name+"/"+fn.EcmaName().String(), count, kind == bytecode.CodeForCall); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func run(path string, skipFeatures map[string]bool, debugMode bool) []result {
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
	if md.negativePhase == "parse" {
		return nil // nothing to compile
	}

	var mode bytecode.CodeGenerationMode
	if debugMode {
		mode = bytecode.CodeGenerationModeDebugger | bytecode.CodeGenerationModeTypeProfiler | bytecode.CodeGenerationModeControlFlowProfiler
	}

	check := func(label, code string, module bool) (r result) {
		r = result{path: path, mode: label}
		defer func() {
			if p := recover(); p != nil {
				r.message = fmt.Sprintf("PANIC: %v\n%s", p, debug.Stack())
				r.pass = false
			}
		}()
		sourceType := parser.SourceTypeProgram
		if module {
			sourceType = parser.SourceTypeModule
		}
		sc := parser.NewSourceCode(parser.NewSourceProvider(code, path, sourceType))
		var cb *bytecode.UnlinkedCodeBlock
		var cerr *bytecompiler.Error
		if module {
			cb, cerr = bytecompiler.CompileModule(sc, mode)
		} else {
			cb, cerr = bytecompiler.CompileProgram(sc, parser.NoLexicallyScopedFeatures, mode)
		}
		if cerr != nil {
			r.message = cerr.Error()
			return r
		}
		count := 0
		if err := compileAll(cb, "<program>", &count, true); err != nil {
			r.message = err.Error()
			return r
		}
		r.pass = true
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
	debugMode := flag.Bool("debug", false, "compile with debugger and profiler hooks")
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
				results <- run(p, skipFeatures, *debugMode)
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

	var failures []result
	pass, fail := 0, 0
	for rs := range results {
		for _, r := range rs {
			if r.pass {
				pass++
			} else {
				fail++
				failures = append(failures, r)
			}
		}
	}
	fmt.Printf("TOTAL: %d compiled, %d failed\n", pass, fail)

	sort.Slice(failures, func(i, j int) bool { return failures[i].path < failures[j].path })
	if *out != "" {
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
