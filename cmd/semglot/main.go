// Command semglot transpiles a source semantic-layer dialect into a target
// dialect through a neutral IR.
//
//	semglot build --profile <name> [--config semglot.yaml]
//
// Builds are configured with named profiles in semglot.yaml. Scoring
// (`semglot score`) is v2.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/benchouse/semglot/dialect"
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
	fmt.Fprintln(os.Stderr, "usage: semglot build --profile <name> [--config <file>]")
	fmt.Fprintln(os.Stderr, "profiles are defined in semglot.yaml (override the path with --config)")
}

func buildCmd(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	profileName := fs.String("profile", "", "profile name (required)")
	config := fs.String("config", "semglot.yaml", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profileName == "" {
		fmt.Fprintln(os.Stderr, "build: --profile is required")
		return 2
	}
	spec, err := loadProfile(*config, *profileName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}

	parser, err := dialect.AsParser(spec.SourceDialect)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	emitter, err := dialect.AsEmitter(spec.TargetDialect)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	if c, ok := emitter.(dialect.Configurable); ok {
		emitter = c.WithOptions(dialect.Options{
			Database:    spec.Database,
			Schema:      spec.Schema,
			ViewSchema:  spec.ViewSchema,
			Name:        spec.ModelName,
			Description: spec.Description,
		})
	}

	model, err := parser.Parse(spec.Sources...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	if err := emitter.Emit(model, spec.Output); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d item(s) could not be fully transpiled:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	if spec.TargetDialect == "cortex" {
		if gaps := dialect.CortexTypeGaps(model); len(gaps) > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d Cortex column(s) had no source data_type; inferred a type (add data_type in dbt to fix):\n", len(gaps))
			for _, g := range gaps {
				fmt.Fprintln(os.Stderr, "  - "+g)
			}
		}
	}
	fmt.Printf("wrote to %s (%s -> %s)\n", spec.Output, spec.SourceDialect, spec.TargetDialect)
	return 0
}
