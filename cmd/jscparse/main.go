// Command jscparse parses JavaScript files (or -e source) and reports syntax errors.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/baslentfert/jsc2go/parser"
)

func main() {
	expr := flag.String("e", "", "source text to parse instead of files")
	module := flag.Bool("module", false, "parse as a module")
	flag.Parse()

	parse := func(name, src string) bool {
		provider := parser.NewSourceProvider(src, name, parser.SourceTypeProgram)
		code := parser.NewSourceCode(provider)
		var err *parser.ParserError
		if *module {
			_, err = parser.ParseModule(code, parser.ModuleEvaluateMode)
		} else {
			_, err = parser.ParseProgram(code, parser.NoLexicallyScopedFeatures)
		}
		if err != nil {
			fmt.Printf("%s: %v\n", name, err)
			return false
		}
		fmt.Printf("%s: ok\n", name)
		return true
	}

	ok := true
	if *expr != "" {
		ok = parse("<expr>", *expr)
	}
	for _, f := range flag.Args() {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			ok = false
			continue
		}
		ok = parse(f, string(data)) && ok
	}
	if !ok {
		os.Exit(1)
	}
}
