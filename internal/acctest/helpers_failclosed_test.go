package acctest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

// The resolvers under test take a *testing.T and end in t.Fatal or t.Skip, so
// their outcome is observed by re-running this test binary as a child process
// with failClosedChildEnv set. The child runs one resolver against the mock
// API and reports how its test ended.
const failClosedChildEnv = "ANYSCALE_ACCTEST_FAILCLOSED_CHILD"

func TestFailClosedResolverChild(t *testing.T) {
	switch os.Getenv(failClosedChildEnv) {
	case "":
		t.Skip("helper process for TestResolvers_APIErrorFailsNotSkips")
	case "cloud":
		GetTestCloudID(t)
	case "anycloud":
		GetAnyCloudID(t)
	case "computeconfig":
		GetComputeConfigCloudID(t)
	}
}

// TestResolvers_APIErrorFailsNotSkips pins that an API error while resolving
// a test cloud fails the test. These resolvers used to turn any
// error into a skip, so an API outage made the cloud-dependent tests skip and
// the acctest shards report green. A genuinely empty org still skips, which
// the last case checks so the failing cases cannot pass by always failing.
func TestResolvers_APIErrorFailsNotSkips(t *testing.T) {
	serverError := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}
	emptyOrg := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[],"metadata":{"next_paging_token":null}}`))
	}

	// Fails the first request only. The default-fixture lookup makes the
	// first request, so a resolver that swallowed that error would fall
	// through to auto-discovery, find an empty org, and skip.
	var calls atomic.Int64
	firstRequestFails := func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			serverError(w, r)
			return
		}
		emptyOrg(w, r)
	}

	cases := []struct {
		name     string
		resolver string
		handler  http.HandlerFunc
		wantSkip bool
	}{
		{"cloud resolution on 500", "cloud", serverError, false},
		{"any-cloud resolution on 500", "anycloud", serverError, false},
		{"default fixture lookup error is not a not-found", "cloud", firstRequestFails, false},
		{"cloud resolution in an empty org", "cloud", emptyOrg, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()

			cmd := exec.Command(os.Args[0], "-test.run=^TestFailClosedResolverChild$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(),
				failClosedChildEnv+"="+tc.resolver,
				"ANYSCALE_API_URL="+server.URL,
				"ANYSCALE_CLI_TOKEN=fake-token-failclosed",
				"ANYSCALE_TEST_CLOUD_ID=",
				"ANYSCALE_TEST_CLOUD_NAME=",
				"ANYSCALE_TEST_CREATE_CLOUD=",
			)
			out, err := cmd.CombinedOutput()
			output := string(out)

			skipped := strings.Contains(output, "--- SKIP: TestFailClosedResolverChild")
			failed := strings.Contains(output, "--- FAIL: TestFailClosedResolverChild")
			if tc.wantSkip {
				if !skipped || err != nil {
					t.Fatalf("want a skip, got err=%v:\n%s", err, output)
				}
				return
			}
			if !failed || err == nil {
				t.Fatalf("want a failure, got skipped=%v err=%v:\n%s", skipped, err, output)
			}
		})
	}
}
