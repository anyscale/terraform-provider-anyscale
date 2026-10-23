// Command acctest-rerun backs the one-time hibernation rerun in
// .github/workflows/scheduled-acctest.yml (see
// .github/scripts/acctest-with-hibernation-rerun.sh).
//
// The static test cloud hibernates after a week idle, and can do so between
// the wake gate's probe and a test's first create (internal/acctest/cloud_wake.go).
// Such a test fails with HTTP 599 "hibernated due to inactivity". This tool
// decides, from a `go test -json` stream, whether a failed run failed only
// that way, so the workflow can re-run exactly those tests once.
//
// Usage:
//
//	acctest-rerun render < events.jsonl
//	acctest-rerun classify events.jsonl
//
// render prints the human-readable test log carried in the stream's output
// events; lines that are not JSON (a build error on stderr) pass through.
//
// classify works from per-test events, never from scraped -v text: with
// -parallel, output interleaves and a line under one test's header can belong
// to another.
//
// Exit codes for classify:
//
//	0  rerun: every failed top-level test failed on hibernation. The anchored
//	   -run pattern for exactly those tests is printed on stdout.
//	3  no rerun: the reason is printed on stderr.
//	2  input error.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	exitRerun   = 0
	exitInput   = 2
	exitNoRerun = 3
)

// hibernationDetail is the part of the backend's 599 detail that identifies a
// hibernated cloud: "Cloud <id> has been hibernated due to inactivity. ...".
const hibernationDetail = "hibernated due to inactivity"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: acctest-rerun render < events.jsonl | acctest-rerun classify events.jsonl")
		return exitInput
	}
	switch args[0] {
	case "render":
		if err := render(stdin, stdout); err != nil {
			fmt.Fprintf(stderr, "acctest-rerun: render: %v\n", err)
			return exitInput
		}
		return 0
	case "classify":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: acctest-rerun classify events.jsonl")
			return exitInput
		}
		f, err := os.Open(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "acctest-rerun: %v\n", err)
			return exitInput
		}
		defer func() { _ = f.Close() }()
		v, err := classify(f)
		if err != nil {
			fmt.Fprintf(stderr, "acctest-rerun: classify: %v\n", err)
			return exitInput
		}
		if v.Reason != "" {
			fmt.Fprintf(stderr, "no rerun: %s\n", v.Reason)
			return exitNoRerun
		}
		fmt.Fprintln(stdout, runPattern(v.Rerun))
		return exitRerun
	default:
		fmt.Fprintf(stderr, "acctest-rerun: unknown command %q\n", args[0])
		return exitInput
	}
}

// event is the subset of a test2json event this tool reads.
type event struct {
	Action string
	Test   string
	Output string
}

// newScanner returns a line scanner sized for long output lines (a Terraform
// plan or an API body can exceed bufio's 64KiB default).
func newScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return s
}

// render writes the text of every output event, and every non-JSON line
// verbatim, flushing per line so the CI log streams.
func render(r io.Reader, w io.Writer) error {
	bw := bufio.NewWriter(w)
	s := newScanner(r)
	for s.Scan() {
		line := s.Bytes()
		var e event
		if json.Unmarshal(line, &e) == nil && e.Action != "" {
			_, _ = bw.WriteString(e.Output)
		} else {
			_, _ = bw.Write(line)
			_ = bw.WriteByte('\n')
		}
		if err := bw.Flush(); err != nil {
			return err
		}
	}
	return s.Err()
}

// verdict is classify's result: either the top-level tests to rerun, or the
// reason not to rerun anything.
type verdict struct {
	Rerun  []string
	Reason string
}

// classify reads a `go test -json` stream. It returns a rerun set only when
// the run completed (every started test reported a result), at least one
// test failed, and every failed top-level test's own output - subtests
// included - contains the hibernation detail.
func classify(r io.Reader) (verdict, error) {
	output := map[string]*strings.Builder{} // top-level test -> its output
	started := map[string]bool{}            // every test and subtest that ran
	finished := map[string]bool{}
	failed := map[string]bool{} // top-level tests
	sawEvent := false

	s := newScanner(r)
	for s.Scan() {
		var e event
		if err := json.Unmarshal(s.Bytes(), &e); err != nil || e.Action == "" {
			// Not a test event: a build error or other stderr text. It is
			// not attributable to a test, so it never adds to a rerun set.
			continue
		}
		sawEvent = true
		if e.Test == "" {
			continue
		}
		top, _, _ := strings.Cut(e.Test, "/")
		switch e.Action {
		case "run":
			started[e.Test] = true
		case "pass", "skip":
			finished[e.Test] = true
		case "fail":
			finished[e.Test] = true
			failed[top] = true
		case "output":
			b := output[top]
			if b == nil {
				b = &strings.Builder{}
				output[top] = b
			}
			b.WriteString(e.Output)
		}
	}
	if err := s.Err(); err != nil {
		return verdict{}, err
	}
	if !sawEvent {
		return verdict{}, errors.New("no test2json events in input")
	}

	// A panic or timeout kills the test binary: the tests running at that
	// moment never report, and tests not yet started never run. Rerunning
	// only the reported failures would drop them.
	var unfinished []string
	for name := range started {
		if !finished[name] {
			unfinished = append(unfinished, name)
		}
	}
	if len(unfinished) > 0 {
		sort.Strings(unfinished)
		return verdict{Reason: fmt.Sprintf("the test binary did not complete (no result for %s)", strings.Join(unfinished, ", "))}, nil
	}

	if len(failed) == 0 {
		return verdict{Reason: "no test failed; the failure came from the build or the harness"}, nil
	}

	var names, other []string
	for name := range failed {
		names = append(names, name)
		if !containsHibernation(output[name]) {
			other = append(other, name)
		}
	}
	sort.Strings(names)
	if len(other) > 0 {
		sort.Strings(other)
		return verdict{Reason: fmt.Sprintf("%s failed for a reason other than hibernation", strings.Join(other, ", "))}, nil
	}
	return verdict{Rerun: names}, nil
}

// containsHibernation reports whether b holds the hibernation detail. Spaces
// are normalized first because Terraform wraps diagnostic text, so the phrase
// can span lines and indentation.
func containsHibernation(b *strings.Builder) bool {
	if b == nil {
		return false
	}
	text := strings.ToLower(strings.Join(strings.Fields(b.String()), " "))
	return strings.Contains(text, hibernationDetail)
}

// runPattern returns a -run pattern matching exactly the named top-level
// tests and, through go test's per-level matching, all of their subtests.
func runPattern(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}
