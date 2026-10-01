package provider

import (
	"strings"
	"testing"
)

func TestSanitizeJSONForLog(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantAbsent  []string
		wantPresent []string
	}{
		{"object", `{"name":"c1","credentials":"arn:aws:iam::1:role/x"}`, []string{"arn:aws"}, []string{`"name":"c1"`, "[REDACTED]"}},
		{"nested", `{"aws_config":{"anyscale_iam_role_id":"role-123","zones":["a"]}}`, []string{"role-123"}, []string{`"zones":["a"]`}},
		// A top-level array (a list endpoint's raw body) used to be returned
		// verbatim, so every redaction above was skipped for it.
		{"top-level array", `[{"name":"c1","credentials":"arn:aws:iam::1:role/x"}]`, []string{"arn:aws"}, []string{`"name":"c1"`, "[REDACTED]"}},
		{"array of arrays", `[[{"tenant_id":"t-999"}]]`, []string{"t-999"}, []string{"[REDACTED]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeJSONForLog(tc.in)
			for _, s := range tc.wantAbsent {
				if strings.Contains(got, s) {
					t.Errorf("output still contains %q: %s", s, got)
				}
			}
			for _, s := range tc.wantPresent {
				if !strings.Contains(got, s) {
					t.Errorf("output should contain %q: %s", s, got)
				}
			}
		})
	}

	// Non-JSON and empty input pass through untouched.
	if got := SanitizeJSONForLog("not json"); got != "not json" {
		t.Errorf("got %q", got)
	}
	if got := SanitizeJSONForLog(""); got != "" {
		t.Errorf("got %q", got)
	}
}
