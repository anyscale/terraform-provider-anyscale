package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const pkg = "github.com/anyscale/terraform-provider-anyscale/internal/acctest"

// realStream converts a testdata file - the `go test -v` output of a failed
// nightly run, IDs redacted (see testdata/README.md) - into the event stream
// `go test -json` produces, using the toolchain's own test2json.
func realStream(t *testing.T, name string) []byte {
	t.Helper()
	in, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	cmd := exec.Command("go", "tool", "test2json", "-p", pkg)
	cmd.Stdin = in
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go tool test2json: %v", err)
	}
	return out
}

// stream builds a test2json stream from (action, test, output) triples.
func stream(t *testing.T, events ...[3]string) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, e := range events {
		line, err := json.Marshal(map[string]string{"Action": e[0], "Package": pkg, "Test": e[1], "Output": e[2]})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func mustClassify(t *testing.T, in []byte) verdict {
	t.Helper()
	v, err := classify(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	return v
}

func wantRerun(t *testing.T, v verdict, want ...string) {
	t.Helper()
	if v.Reason != "" || !reflect.DeepEqual(v.Rerun, want) {
		t.Fatalf("verdict = %+v, want rerun of %v", v, want)
	}
}

func wantNoRerun(t *testing.T, v verdict, reasonContains string) {
	t.Helper()
	if len(v.Rerun) != 0 || !strings.Contains(v.Reason, reasonContains) {
		t.Fatalf("verdict = %+v, want no rerun with a reason containing %q", v, reasonContains)
	}
}

const hibernated = "Failed to create compute config (HTTP 599): Cloud\n        cld_REDACTED has been hibernated due to inactivity. The\n        cloud is being started and should be available in 5-10 minutes.\n"

// 2026-10-15: a top-level test failed on its own direct create.
func TestClassify_RealRun20261015(t *testing.T) {
	wantRerun(t, mustClassify(t, realStream(t, "run-38682175948.txt")),
		"TestAccComputeConfigResource_ImportRecoversWriteOnlyFields_RealAPI")
}

// 2026-10-22: only the AWS_VM subtest failed; the parent is what reruns.
func TestClassify_RealRun20261022SubtestFailure(t *testing.T) {
	wantRerun(t, mustClassify(t, realStream(t, "run-39723238449.txt")),
		"TestAccComputeConfigResource_Basic")
}

func TestClassify_AllHibernationRerunsExactlyTheFailedTests(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"run", "TestB", ""},
		[3]string{"run", "TestA", ""},
		[3]string{"run", "TestC", ""},
		[3]string{"output", "TestA", hibernated},
		[3]string{"fail", "TestA", ""},
		[3]string{"pass", "TestC", ""},
		[3]string{"output", "TestB", hibernated},
		[3]string{"fail", "TestB", ""},
		[3]string{"fail", "", ""},
	))
	wantRerun(t, v, "TestA", "TestB")
}

// With -parallel, a hibernation body can print right after another test's
// header in -v text. Per-test events attribute it correctly: here TestReal's
// own output is a real error, so nothing reruns even though the hibernation
// text sits next to TestReal's failure in the rendered log.
func TestClassify_InterleavedOutputAttributedToItsOwnTest(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"run", "TestSleepy", ""},
		[3]string{"run", "TestReal", ""},
		[3]string{"output", "TestReal", "=== NAME  TestReal\n"},
		[3]string{"output", "TestSleepy", hibernated},
		[3]string{"output", "TestReal", "    expected 3 workers, got 2\n"},
		[3]string{"output", "TestReal", "--- FAIL: TestReal (1.00s)\n"},
		[3]string{"fail", "TestReal", ""},
		[3]string{"output", "TestSleepy", "--- FAIL: TestSleepy (1.00s)\n"},
		[3]string{"fail", "TestSleepy", ""},
		[3]string{"fail", "", ""},
	))
	wantNoRerun(t, v, "TestReal failed for a reason other than hibernation")

	// The converse: the hibernation text belongs to TestSleepy, which is the
	// only failure, while the passing TestReal's output sits around it.
	v = mustClassify(t, stream(t,
		[3]string{"run", "TestSleepy", ""},
		[3]string{"run", "TestReal", ""},
		[3]string{"output", "TestReal", "=== NAME  TestReal\n"},
		[3]string{"output", "TestSleepy", hibernated},
		[3]string{"pass", "TestReal", ""},
		[3]string{"fail", "TestSleepy", ""},
		[3]string{"fail", "", ""},
	))
	wantRerun(t, v, "TestSleepy")
}

func TestClassify_MixedFailuresNoRerun(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"run", "TestSleepy", ""},
		[3]string{"run", "TestReal", ""},
		[3]string{"output", "TestSleepy", hibernated},
		[3]string{"fail", "TestSleepy", ""},
		[3]string{"output", "TestReal", "provider produced inconsistent result after apply\n"},
		[3]string{"fail", "TestReal", ""},
		[3]string{"fail", "", ""},
	))
	wantNoRerun(t, v, "TestReal failed for a reason other than hibernation")
}

func TestClassify_SubtestFailureRerunsParent(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"run", "TestX", ""},
		[3]string{"run", "TestX/AWS_VM", ""},
		[3]string{"output", "TestX/AWS_VM", hibernated},
		[3]string{"fail", "TestX/AWS_VM", ""},
		[3]string{"fail", "TestX", ""},
		[3]string{"fail", "", ""},
	))
	wantRerun(t, v, "TestX")
	if got := runPattern(v.Rerun); got != "^(TestX)$" {
		t.Fatalf("runPattern = %q", got)
	}
}

// A build failure produces no test results at all.
func TestClassify_BuildFailureNoRerun(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"build-output", "", "internal/acctest/x.go:1:1: syntax error\n"},
		[3]string{"fail", "", ""},
	))
	wantNoRerun(t, v, "no test failed")
}

// A panic kills the binary: the panicking test and any test still running
// never report a result, so nothing reruns.
func TestClassify_PanicNoRerun(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"run", "TestSleepy", ""},
		[3]string{"run", "TestCrash", ""},
		[3]string{"output", "TestSleepy", hibernated},
		[3]string{"fail", "TestSleepy", ""},
		[3]string{"output", "TestCrash", "panic: runtime error: index out of range\n"},
		[3]string{"fail", "", ""},
	))
	wantNoRerun(t, v, "did not complete (no result for TestCrash)")
}

// The real 10-15 log with an unrelated failure appended: the mixed case on
// real data.
func TestClassify_RealRunPlusRealFailureNoRerun(t *testing.T) {
	in := append(realStream(t, "run-38682175948.txt"), stream(t,
		[3]string{"run", "TestAccProjectResource_Basic2", ""},
		[3]string{"output", "TestAccProjectResource_Basic2", "Error: API Request Failed (HTTP 500)\n"},
		[3]string{"fail", "TestAccProjectResource_Basic2", ""},
	)...)
	wantNoRerun(t, mustClassify(t, in), "TestAccProjectResource_Basic2 failed for a reason other than hibernation")
}

func TestClassify_EmptyInputIsAnError(t *testing.T) {
	if _, err := classify(strings.NewReader("not json\n")); err == nil {
		t.Fatal("expected an error for input with no test2json events")
	}
}

func TestRunPatternAnchorsEveryName(t *testing.T) {
	if got := runPattern([]string{"TestA", "TestB"}); got != "^(TestA|TestB)$" {
		t.Fatalf("runPattern = %q", got)
	}
}

func TestRender(t *testing.T) {
	in := string(stream(t,
		[3]string{"start", "", ""},
		[3]string{"output", "TestA", "=== RUN   TestA\n"},
		[3]string{"output", "TestA", "--- PASS: TestA (0.00s)\n"},
	)) + "# a build error on stderr\n"
	var out bytes.Buffer
	if err := render(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	want := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n# a build error on stderr\n"
	if out.String() != want {
		t.Fatalf("render = %q, want %q", out.String(), want)
	}
}

func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rerun := write("rerun.jsonl", stream(t,
		[3]string{"run", "TestA", ""},
		[3]string{"output", "TestA", hibernated},
		[3]string{"fail", "TestA", ""},
	))
	noRerun := write("norerun.jsonl", stream(t,
		[3]string{"run", "TestA", ""},
		[3]string{"output", "TestA", "boom\n"},
		[3]string{"fail", "TestA", ""},
	))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"classify", rerun}, nil, &stdout, &stderr); code != exitRerun || stdout.String() != "^(TestA)$\n" {
		t.Fatalf("rerun: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"classify", noRerun}, nil, &stdout, &stderr); code != exitNoRerun || stdout.Len() != 0 || !strings.Contains(stderr.String(), "no rerun: TestA") {
		t.Fatalf("no rerun: code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if code := run([]string{"classify", filepath.Join(dir, "missing")}, nil, &stdout, &stderr); code != exitInput {
		t.Fatalf("missing file: code %d, want %d", code, exitInput)
	}
}

// Terraform wraps diagnostic text, so the detail can span a line break and
// indentation.
func TestClassify_DetailWrappedAcrossLines(t *testing.T) {
	v := mustClassify(t, stream(t,
		[3]string{"run", "TestA", ""},
		[3]string{"output", "TestA", "        Cloud cld_REDACTED has been hibernated due\n"},
		[3]string{"output", "TestA", "        to inactivity. The cloud is being started.\n"},
		[3]string{"fail", "TestA", ""},
	))
	wantRerun(t, v, "TestA")
}
