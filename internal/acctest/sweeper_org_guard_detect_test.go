package acctest

import (
	"os"
	"testing"
)

// TestSweepRequestedDetection covers the arg-parsing half of the sweep-target
// org guard. The guard is worthless if it fails to notice a sweep, and it is
// actively harmful if it fires on an ordinary test run (it would abort the
// whole package). Both directions are asserted.
//
// Not a TestAcc* name on purpose: this is a pure unit test with no API calls, so
// carrying that prefix would misrepresent it as a shard-gated acceptance test.
func TestSweepRequestedDetection(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		// The form the Makefile actually uses. If this case ever fails, `make
		// sweep` is running unguarded.
		{"makefile form", []string{"-sweep=anyscale", "-sweep-run="}, true},
		{"double dash", []string{"--sweep=anyscale"}, true},
		{"separate arg", []string{"-sweep", "anyscale"}, true},
		{"separate arg among others", []string{"-v", "-sweep", "anyscale", "-timeout", "60m"}, true},

		// Must NOT fire: an ordinary acceptance-test run would be aborted.
		{"no sweep flag", []string{"-v", "-timeout", "120m"}, false},
		{"empty value", []string{"-sweep="}, false},
		{"bare flag, no value", []string{"-sweep"}, false},
		{"bare flag then another flag", []string{"-sweep", "-v"}, false},
		{"no args", []string{}, false},

		// sweep-run is a DIFFERENT flag. Matching it by prefix would fire the
		// guard on `-sweep-run=X` alone, which does not run sweepers.
		{"sweep-run only is not a sweep", []string{"-sweep-run=TestFoo"}, false},
		{"sweep-run with value form", []string{"-sweep-run", "TestFoo"}, false},

		// A test name that merely contains the word must not trigger it.
		{"run filter mentioning sweep", []string{"-run", "TestAccSweeperThing"}, false},
	}

	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = append([]string{"acctest.test"}, tc.args...)
			if got := sweepRequested(); got != tc.want {
				t.Fatalf("sweepRequested() with args %v = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
