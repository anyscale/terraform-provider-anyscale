package acctest

import (
	"strings"
	"testing"
)

type recordingSkipT struct {
	skipped bool
	msg     string
}

func (r *recordingSkipT) Helper() {}

func (r *recordingSkipT) Skipf(format string, args ...any) {
	r.skipped = true
	r.msg = format
}

func TestSkipIfCredentialsWithheld(t *testing.T) {
	cases := []struct {
		name      string
		flag      string
		available bool
		wantSkip  bool
	}{
		{name: "no credentials, flag set", flag: "1", available: false, wantSkip: true},
		{name: "no credentials, flag unset", flag: "", available: false, wantSkip: false},
		{name: "no credentials, flag not 1", flag: "true", available: false, wantSkip: false},
		{name: "credentials, flag set", flag: "1", available: true, wantSkip: false},
		{name: "credentials, flag unset", flag: "", available: true, wantSkip: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(credentialsWithheldEnv, tc.flag)
			rec := &recordingSkipT{}
			skipIfCredentialsWithheldWith(rec, func() bool { return tc.available })
			if rec.skipped != tc.wantSkip {
				t.Fatalf("skipped = %v, want %v", rec.skipped, tc.wantSkip)
			}
			if rec.skipped && !strings.HasPrefix(rec.msg, "SKIP(no-credentials)") {
				t.Fatalf("skip message %q lacks the SKIP(no-credentials) marker the CI summary counts", rec.msg)
			}
		})
	}
}
