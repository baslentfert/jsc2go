// Command jscbc compiles JavaScript to bytecode and dumps it, including the
// code blocks of all nested functions.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/parser"
)

func main() {
	expr := flag.String("e", "", "compile this source text instead of a file")
	module := flag.Bool("module", false, "compile as a module")
	flag.Parse()

	var text, url string
	switch {
	case *expr != "":
		text, url = *expr, "<eval>"
	case flag.NArg() == 1:
		data, err := os.ReadFile(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		text, url = string(data), flag.Arg(0)
	default:
		fmt.Fprintln(os.Stderr, "usage: jscbc [-module] (-e source | file)")
		os.Exit(2)
	}

	sourceType := parser.SourceTypeProgram
	if *module {
		sourceType = parser.SourceTypeModule
	}
	source := parser.NewSourceCode(parser.NewSourceProvider(text, url, sourceType))

	var cb *bytecode.UnlinkedCodeBlock
	var err *bytecompiler.Error
	if *module {
		cb, err = bytecompiler.CompileModule(source, 0)
	} else {
		cb, err = bytecompiler.CompileProgram(source, parser.NoLexicallyScopedFeatures, 0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dumpRecursively(cb, "")
}

func dumpRecursively(cb *bytecode.UnlinkedCodeBlock, name string) {
	if name != "" {
		fmt.Printf("\n=== %s ===\n", name)
	}
	bytecode.Dump(os.Stdout, cb)
	for _, list := range [][]*bytecode.UnlinkedFunctionExecutable{cb.FunctionDecls, cb.FunctionExprs} {
		for _, fn := range list {
			kind := bytecode.CodeForCall
			if fn.ConstructAbility == bytecode.CanConstruct && fn.ConstructorKind != parser.ConstructorKindNone {
				kind = bytecode.CodeForConstruct
			}
			fcb, err := bytecompiler.CodeBlockForFunction(fn, kind, 0)
			fnName := fn.EcmaName().String()
			if fnName == "" {
				fnName = "<anonymous>"
			}
			if err != nil {
				fmt.Printf("\n=== %s: %v ===\n", fnName, err)
				continue
			}
			dumpRecursively(fcb, fnName)
		}
	}
}
