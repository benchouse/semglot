// Command semglot transpiles a source semantic-layer dialect into a target
// dialect through a neutral IR.
//
//	semglot build --from dbt --reference ./semantic --layer cortex --out ./cortex/
//
// v1 supports dbt (source) -> cortex (target). Scoring (`semglot score`) is v2.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/benchouse/semglot/layer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(buildCmd(os.Args[2:]))
	case "score":
		fmt.Fprintln(os.Stderr, "score is not implemented yet (v2)")
		os.Exit(1)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: semglot <build|score> [flags]")
}

func buildCmd(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	from := fs.String("from", "dbt", "source dialect")
	reference := fs.String("reference", "", "source dialect directory (required)")
	target := fs.String("layer", "", "target dialect (required)")
	out := fs.String("out", "", "output directory (required)")
	database := fs.String("database", "", "Cortex base_table database")
	schema := fs.String("schema", "MAIN", "Cortex base_table schema")
	name := fs.String("name", "semantic_model", "Cortex model name")
	description := fs.String("description", "", "Cortex model description")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reference == "" || *target == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "build: --reference, --layer and --out are required")
		return 2
	}

	parser, err := layer.AsParser(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	emitter, err := layer.AsEmitter(*target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	if c, ok := emitter.(layer.Configurable); ok {
		emitter = c.WithOptions(*database, *schema, *name, *description)
	}

	model, err := parser.Parse(*reference)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	if err := emitter.Emit(model, *out); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d item(s) could not be fully transpiled:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	fmt.Printf("wrote to %s (%s -> %s)\n", *out, *from, *target)
	return 0
}
