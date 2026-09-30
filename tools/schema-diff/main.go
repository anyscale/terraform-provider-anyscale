// Command schema-diff compares two `terraform providers schema -json` dumps
// (base and head) for one provider and classifies every difference as
// breaking, needs-review, or non-breaking. It backs the schema-gate CI check
// (.github/workflows/schema-gate.yml, .github/scripts/schema-gate.sh).
//
// Usage:
//
//	schema-diff -base base.json -head head.json [-provider ADDR] [-summary FILE]
//
// The markdown report goes to stdout, and is also appended to -summary when
// set (CI passes $GITHUB_STEP_SUMMARY).
//
// Exit codes:
//
//	0  no breaking changes (needs-review and non-breaking changes may exist)
//	1  at least one breaking change
//	2  input error: unreadable or unparsable file, or the provider is absent
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tfjson "github.com/hashicorp/terraform-json"
)

const (
	exitClean    = 0
	exitBreaking = 1
	exitInput    = 2
)

const defaultProvider = "registry.terraform.io/anyscale/anyscale"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schema-diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	basePath := fs.String("base", "", "path to the base `terraform providers schema -json` output (required)")
	headPath := fs.String("head", "", "path to the head `terraform providers schema -json` output (required)")
	provider := fs.String("provider", defaultProvider, "provider address to compare")
	summaryPath := fs.String("summary", "", "also append the markdown report to this file (e.g. $GITHUB_STEP_SUMMARY)")
	if err := fs.Parse(args); err != nil {
		return exitInput
	}
	if *basePath == "" || *headPath == "" {
		fmt.Fprintln(stderr, "schema-diff: -base and -head are both required")
		return exitInput
	}

	base, err := loadProvider(*basePath, *provider)
	if err != nil {
		fmt.Fprintf(stderr, "schema-diff: base: %v\n", err)
		return exitInput
	}
	head, err := loadProvider(*headPath, *provider)
	if err != nil {
		fmt.Fprintf(stderr, "schema-diff: head: %v\n", err)
		return exitInput
	}

	changes := Diff(base, head)
	report := Render(*provider, changes)
	fmt.Fprint(stdout, report)
	if *summaryPath != "" {
		if err := appendFile(*summaryPath, report); err != nil {
			fmt.Fprintf(stderr, "schema-diff: writing summary: %v\n", err)
			return exitInput
		}
	}

	if countSeverity(changes, Breaking) > 0 {
		return exitBreaking
	}
	return exitClean
}

func loadProvider(path, addr string) (*tfjson.ProviderSchema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var schemas tfjson.ProviderSchemas
	if err := schemas.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	ps, ok := schemas.Schemas[addr]
	if !ok || ps == nil {
		return nil, fmt.Errorf("%s has no schema for provider %q", path, addr)
	}
	return ps, nil
}

func appendFile(path, content string) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	_, err = io.WriteString(f, content)
	return err
}
