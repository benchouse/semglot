// Command semglot transpiles a source semantic-layer dialect into a target
// dialect through a neutral IR.
//
//	semglot build --source ./semantic --target-type cortex --target ./cortex/
//
// v1 supports dbt (source) -> cortex (target). Scoring (`semglot score`) is v2.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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

// snowflakeTargets are the target-type dialects that emit into a physical
// Snowflake database. They require a resolved database (via --database or
// --config); without one they'd emit invalid, unqualified DDL.
var snowflakeTargets = map[string]bool{"cortex": true, "snowflake-semantic-view": true}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: semglot build --source <dir> --target <dir> --target-type <dialect> [--config <file>] [--database --schema --name --description]")
	fmt.Fprintln(os.Stderr, "target-type is one of: "+strings.Join(layer.Names(), ", "))
}

func buildCmd(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	sourceType := fs.String("source-type", "dbt", "source dialect")
	source := fs.String("source", "", "source directory (required)")
	targetType := fs.String("target-type", "", "target dialect (required); one of: "+strings.Join(layer.Names(), ", "))
	target := fs.String("target", "", "output directory (required)")
	config := fs.String("config", "", "path to a config file (optional)")
	database := fs.String("database", "", "warehouse database (Snowflake targets)")
	schema := fs.String("schema", "", "warehouse schema (Snowflake targets; default MAIN)")
	name := fs.String("name", "", "model/view name (default: source basename)")
	description := fs.String("description", "", "model description")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *source == "" || *targetType == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "build: --source, --target and --target-type are required")
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	parser, err := layer.AsParser(*sourceType)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	emitter, err := layer.AsEmitter(*targetType)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	if c, ok := emitter.(layer.Configurable); ok {
		id, err := resolveIdentity(*source, *config, set,
			identity{Database: *database, Schema: *schema, Name: *name, Description: *description})
		if err != nil {
			fmt.Fprintln(os.Stderr, "build:", err)
			return 1
		}
		if snowflakeTargets[*targetType] && id.Database == "" {
			fmt.Fprintf(os.Stderr, "build: --target-type %s requires a database (via --database or --config)\n", *targetType)
			return 1
		}
		emitter = c.WithOptions(id.Database, id.Schema, id.Name, id.Description)
	}

	model, err := parser.Parse(*source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	if err := emitter.Emit(model, *target); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d item(s) could not be fully transpiled:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	fmt.Printf("wrote to %s (%s -> %s)\n", *target, *sourceType, *targetType)
	return 0
}
