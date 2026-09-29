package acctest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A project deleted right after create can 403 until its owner grant
// propagates. The fixture's cleanup must retry that, not leak the project.
func TestCreateEphemeralTestProjectForCloud_CleanupRetries403(t *testing.T) {
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects":
			_, _ = w.Write([]byte(`{"result":{"id":"prj_cleanup","name":"x"}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/projects/prj_cleanup":
			if deletes.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"detail":"permission denied"}}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")
	t.Setenv("ANYSCALE_TEST_KEEP", "")

	// The subtest's end runs the fixture's t.Cleanup.
	t.Run("fixture", func(t *testing.T) {
		if _, _, err := CreateEphemeralTestProjectForCloud(t, "cld_x"); err != nil {
			t.Fatalf("create: %v", err)
		}
	})

	if got := deletes.Load(); got != 2 {
		t.Fatalf("cleanup sent %d DELETEs, want 2 (a 403, then a retry that succeeds)", got)
	}
}
