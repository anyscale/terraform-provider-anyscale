package acctest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The shared test organization manages access through one of two stores: the
// legacy permission system (cloud and project collaborators, organization
// roles, auto_add_user) or role bindings. Conversion moves it from the first
// to the second, one way, and each store's writes answer 409 in the other mode.
//
// No API call reports the mode without side effects (the IAM migration status
// GET writes), so tests do not probe it. ANYSCALE_TEST_ORG_MODE declares it:
// "legacy" (the default when unset) or "converted". Tests that only work in one
// mode skip in the other with a stated reason. A test that hits the other
// mode's 409 fails with the setting to change, never skips: a wrong
// declaration must be visible, not silently shrink coverage.

const (
	testOrgModeEnv       = "ANYSCALE_TEST_ORG_MODE"
	testOrgModeLegacy    = "legacy"
	testOrgModeConverted = "converted"
)

// Detail substrings of the two mode refusals. Copied from
// internal/provider/org_access_mode.go (orgConvertedSubstring and
// orgNotConvertedSubstring), which cites the backend source; keep in sync.
const (
	testOrgConvertedDetail    = "This organization uses role bindings"
	testOrgNotConvertedDetail = "Switch this organization to role bindings"
)

// testOrgMode returns the declared access mode of the test organization.
func testOrgMode(t *testing.T) string {
	t.Helper()
	switch mode := strings.TrimSpace(os.Getenv(testOrgModeEnv)); mode {
	case "", testOrgModeLegacy:
		return testOrgModeLegacy
	case testOrgModeConverted:
		return testOrgModeConverted
	default:
		t.Fatalf("%s=%q: want %q or %q", testOrgModeEnv, mode, testOrgModeLegacy, testOrgModeConverted)
		return ""
	}
}

// skipUnlessTestOrgMode skips the test unless the test organization is
// declared to be in mode. reason says why the test needs that mode.
func skipUnlessTestOrgMode(t *testing.T, mode, reason string) {
	t.Helper()
	if got := testOrgMode(t); got != mode {
		t.Skipf("SKIP(org-mode): test organization is declared %s (%s); this test needs %s: %s",
			got, testOrgModeEnv, mode, reason)
	}
}

// orgModeMismatch returns an actionable message when text carries the 409
// detail of the mode the test organization is not declared to be in, or "".
// Whitespace is normalized first: in the ErrorCheck path text is Terraform's
// rendered diagnostic, which wraps long details across indented lines, so a
// detail phrase can span a line break.
func orgModeMismatch(declared, text string) string {
	text = strings.Join(strings.Fields(text), " ")
	switch {
	case declared == testOrgModeLegacy && strings.Contains(text, testOrgConvertedDetail):
		return fmt.Sprintf("test organization mode appears to be %s (the API answered %q); set %s=%s",
			testOrgModeConverted, testOrgConvertedDetail, testOrgModeEnv, testOrgModeConverted)
	case declared == testOrgModeConverted && strings.Contains(text, testOrgNotConvertedDetail):
		return fmt.Sprintf("test organization mode appears to be %s (the API answered %q); set %s=%s",
			testOrgModeLegacy, testOrgNotConvertedDetail, testOrgModeEnv, testOrgModeLegacy)
	}
	return ""
}

// failOnOrgModeMismatch fails the test with orgModeMismatch's message when
// text shows the declared mode is wrong. Call it before reporting an API
// failure, so the message names the setting instead of a bare 409.
func failOnOrgModeMismatch(t *testing.T, text string) {
	t.Helper()
	failOnOrgModeMismatchWith(t, testOrgMode(t), text)
}

// fataler is the part of *testing.T failOnOrgModeMismatchWith uses, so a test
// can confirm a mismatch fails rather than skips.
type fataler interface {
	Helper()
	Fatal(args ...any)
}

func failOnOrgModeMismatchWith(t fataler, declared, text string) {
	t.Helper()
	if msg := orgModeMismatch(declared, text); msg != "" {
		t.Fatal(msg)
	}
}

// orgModeErrorCheck is a resource.TestCase ErrorCheck that keeps every step
// error a failure and prefixes a mode mismatch with the setting to change.
func orgModeErrorCheck(t *testing.T) func(error) error {
	declared := testOrgMode(t)
	return func(err error) error {
		if err == nil {
			return nil
		}
		if msg := orgModeMismatch(declared, err.Error()); msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
}

func TestOrgModeMismatch(t *testing.T) {
	cases := []struct {
		name, declared, text string
		wantSetting          string // "" = no mismatch
	}{
		{"legacy, converted refusal", testOrgModeLegacy, "409: " + testOrgConvertedDetail + ". Manage access in Users & IAM", "ANYSCALE_TEST_ORG_MODE=converted"},
		{"converted, not-converted refusal", testOrgModeConverted, "409: " + testOrgNotConvertedDetail + " to change role bindings.", "ANYSCALE_TEST_ORG_MODE=legacy"},
		{"legacy, its own refusal", testOrgModeLegacy, testOrgNotConvertedDetail, ""},
		{"converted, its own refusal", testOrgModeConverted, testOrgConvertedDetail, ""},
		{"legacy, unrelated 409", testOrgModeLegacy, "409: name already exists", ""},
		{"converted, unrelated error", testOrgModeConverted, "500: internal error", ""},
		{"legacy, refusal wrapped by Terraform", testOrgModeLegacy, "Error: API Request Failed\n\n  with anyscale_cloud_access.live,\n\nHTTP 409: This organization uses\n        role bindings. Manage access in Users & IAM", "ANYSCALE_TEST_ORG_MODE=converted"},
		{"converted, refusal wrapped by Terraform", testOrgModeConverted, "HTTP 409: Switch this organization to role\n        bindings to change role bindings.", "ANYSCALE_TEST_ORG_MODE=legacy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orgModeMismatch(tc.declared, tc.text)
			if tc.wantSetting == "" {
				if got != "" {
					t.Fatalf("orgModeMismatch = %q, want no mismatch", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSetting) {
				t.Fatalf("orgModeMismatch = %q, want it to name %s", got, tc.wantSetting)
			}
		})
	}
}

// A mismatch must fail the step, never pass or skip it: ErrorCheck returning
// nil would turn the step green.
func TestOrgModeErrorCheckKeepsFailures(t *testing.T) {
	t.Setenv(testOrgModeEnv, testOrgModeLegacy)
	check := orgModeErrorCheck(t)
	mismatch := check(fmt.Errorf("Error: 409: %s", testOrgConvertedDetail))
	if mismatch == nil || !strings.Contains(mismatch.Error(), "ANYSCALE_TEST_ORG_MODE=converted") {
		t.Fatalf("mismatch error = %v, want a failure naming ANYSCALE_TEST_ORG_MODE=converted", mismatch)
	}
	other := fmt.Errorf("Error: unrelated")
	if got := check(other); got != other {
		t.Fatalf("unrelated error = %v, want it returned unchanged", got)
	}
}

// recordingFataler records Fatal instead of stopping the test.
type recordingFataler struct{ fatal string }

func (r *recordingFataler) Helper()           {}
func (r *recordingFataler) Fatal(args ...any) { r.fatal = fmt.Sprint(args...) }

// The out-of-band API helpers report a mode mismatch through
// failOnOrgModeMismatchWith: it must fail with the setting to change.
func TestFailOnOrgModeMismatchFails(t *testing.T) {
	var r recordingFataler
	failOnOrgModeMismatchWith(&r, testOrgModeLegacy, `{"error":{"detail":"`+testOrgConvertedDetail+`. Manage access in Users & IAM."}}`)
	if !strings.Contains(r.fatal, "ANYSCALE_TEST_ORG_MODE=converted") {
		t.Fatalf("Fatal message = %q, want a failure naming ANYSCALE_TEST_ORG_MODE=converted", r.fatal)
	}
	r = recordingFataler{}
	failOnOrgModeMismatchWith(&r, testOrgModeLegacy, `{"error":{"detail":"not found"}}`)
	if r.fatal != "" {
		t.Fatalf("unrelated body produced Fatal(%q), want none", r.fatal)
	}
}

func TestTestOrgModeDefaultsToLegacy(t *testing.T) {
	t.Setenv(testOrgModeEnv, "")
	if got := testOrgMode(t); got != testOrgModeLegacy {
		t.Fatalf("testOrgMode with %s unset = %q, want %q", testOrgModeEnv, got, testOrgModeLegacy)
	}
}
