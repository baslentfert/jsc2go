// Command ramunebench runs a script on Ramune's qjswasm backend
// (QuickJS-NG on wazero); build with -tags qjswasm.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/i2y/ramune"
)

func main() {
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
	start := time.Now()
	r, err := ramune.New()
	if err != nil {
		panic(err)
	}
	defer r.Close()
	if err := r.RegisterFunc("print", func(args []any) (any, error) {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = fmt.Sprint(a)
		}
		fmt.Println(strings.Join(parts, " "))
		return nil, nil
	}); err != nil {
		panic(err)
	}
	if err := r.RegisterFunc("readFile", func(args []any) (any, error) {
		b, err := os.ReadFile(fmt.Sprint(args[0]))
		return string(b), err
	}); err != nil {
		panic(err)
	}
	if _, err := r.Eval(string(src)); err != nil {
		fmt.Println("JSERROR", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "total %v\n", time.Since(start).Round(time.Millisecond))
}
