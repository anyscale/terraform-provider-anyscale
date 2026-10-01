package acctest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestExplicitTestCloudID_IsValidated pins that an ANYSCALE_TEST_CLOUD_ID the
// API does not recognize fails the test up front, in both resolvers that honor
// it. Each 404 case has a 200 control through the same resolver, so a resolver
// that failed on every input could not pass.
func TestExplicitTestCloudID_IsValidated(t *testing.T) {
	const cloudID = "cld_explicitvalidationtest00000"
	// GET /api/v2/clouds/{id} answers with cloudStatus; every other request
	// (the wake probe) gets a 200, which reads as an awake cloud.
	handler := func(cloudStatus int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/clouds/") {
				w.WriteHeader(cloudStatus)
				if cloudStatus == http.StatusOK {
					_, _ = w.Write([]byte(`{"result":{"id":"` + cloudID + `"}}`))
				} else {
					_, _ = w.Write([]byte(`{"error":{"detail":"not found"}}`))
				}
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{}}`))
		}
	}

	cases := []struct {
		name        string
		resolver    string
		cloudStatus int
		wantFail    string // substring of the failure; empty means the child must pass
	}{
		{"cloud resolver, unknown ID", "cloud", http.StatusNotFound, "does not name a cloud visible to this token"},
		{"cloud resolver, lookup error", "cloud", http.StatusInternalServerError, "returned status 500"},
		{"cloud resolver, known ID", "cloud", http.StatusOK, ""},
		{"compute config resolver, unknown ID", "computeconfig", http.StatusNotFound, "does not name a cloud visible to this token"},
		{"compute config resolver, known ID", "computeconfig", http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(handler(tc.cloudStatus))
			defer server.Close()

			cmd := exec.Command(os.Args[0], "-test.run=^TestFailClosedResolverChild$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(),
				failClosedChildEnv+"="+tc.resolver,
				"ANYSCALE_API_URL="+server.URL,
				"ANYSCALE_CLI_TOKEN=fake-token-failclosed",
				"ANYSCALE_TEST_CLOUD_ID="+cloudID,
				"ANYSCALE_TEST_CLOUD_NAME=",
				"ANYSCALE_TEST_CREATE_CLOUD=",
			)
			out, err := cmd.CombinedOutput()
			output := string(out)

			if tc.wantFail == "" {
				if err != nil || !strings.Contains(output, "--- PASS: TestFailClosedResolverChild") {
					t.Fatalf("want a pass, got err=%v:\n%s", err, output)
				}
				return
			}
			if err == nil || !strings.Contains(output, "--- FAIL: TestFailClosedResolverChild") {
				t.Fatalf("want a failure, got err=%v:\n%s", err, output)
			}
			if !strings.Contains(output, tc.wantFail) {
				t.Fatalf("failure does not mention %q:\n%s", tc.wantFail, output)
			}
		})
	}
}
