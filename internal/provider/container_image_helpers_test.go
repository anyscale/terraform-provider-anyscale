package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// digestPollTestServer serves GET /api/v2/builds/{id} the same shape the real backend does:
// a build with a nil digest for the first nilResponses requests, then settledDigest on every
// request after that. Models the READY (digest null) -> SUCCEEDED (digest populated)
// transition that motivated waitForBuildDigest.
func digestPollTestServer(t *testing.T, nilResponses int32, settledDigest string) (server *httptest.Server, requestCount *int32) {
	t.Helper()
	var count int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&count, 1)

		result := BuildResult{ID: "bld_test", Status: "succeeded", IsBYOD: true}
		if n > nilResponses {
			digest := settledDigest
			result.Digest = &digest
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(BuildResponse{Result: result})
	}))
	t.Cleanup(server.Close)
	return server, &count
}

func TestWaitForBuildDigest_AlreadySettled(t *testing.T) {
	// build.Digest is already non-null - must return immediately without polling at all,
	// the common case once the settle window has already passed.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected poll: build already had a settled digest")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := NewClientWithToken(server.URL, "test-token")

	digest := "sha256:already-settled"
	build := &BuildResult{ID: "bld_test", Status: "succeeded", Digest: &digest}

	latest, settled := waitForBuildDigestWithTiming(context.Background(), client, build, time.Second, time.Millisecond)

	if !settled {
		t.Error("settled = false, want true")
	}
	if latest.Digest == nil || *latest.Digest != digest {
		t.Errorf("Digest = %v, want %v", latest.Digest, digest)
	}
}

func TestWaitForBuildDigest_SettlesAfterPoll(t *testing.T) {
	// Models the race this fix targets: the build reports "succeeded" with digest still
	// null on the first couple of polls (IMAGE_BUILD_STATE_READY), then digest populates
	// once the cache upload finishes (IMAGE_BUILD_STATE_SUCCEEDED).
	const wantDigest = "sha256:abc123settled"
	server, requestCount := digestPollTestServer(t, 2, wantDigest)
	client := NewClientWithToken(server.URL, "test-token")

	build := &BuildResult{ID: "bld_test", Status: "succeeded"}

	latest, settled := waitForBuildDigestWithTiming(context.Background(), client, build, 200*time.Millisecond, 5*time.Millisecond)

	if !settled {
		t.Fatal("settled = false, want true")
	}
	if latest.Digest == nil || *latest.Digest != wantDigest {
		t.Errorf("Digest = %v, want %v", latest.Digest, wantDigest)
	}
	if got := atomic.LoadInt32(requestCount); got < 3 {
		t.Errorf("requestCount = %d, want at least 3 (2 nil-digest polls before it settles)", got)
	}
}

func TestWaitForBuildDigest_TimesOutWithNullDigest(t *testing.T) {
	// The backend never settles within the window (e.g. a slow cache upload) - this must
	// give up gracefully rather than hang or error, returning the last-seen build so the
	// caller can proceed with digest null plus a warning instead of failing the apply.
	server, requestCount := digestPollTestServer(t, 1000, "unused")
	client := NewClientWithToken(server.URL, "test-token")

	build := &BuildResult{ID: "bld_test", Status: "succeeded"}

	latest, settled := waitForBuildDigestWithTiming(context.Background(), client, build, 17*time.Millisecond, 5*time.Millisecond)

	if settled {
		t.Error("settled = true, want false (server never populates digest)")
	}
	if latest == nil {
		t.Fatal("latest = nil, want the last-seen build")
	}
	if latest.Digest != nil {
		t.Errorf("Digest = %v, want nil", *latest.Digest)
	}
	if latest.ID != "bld_test" {
		t.Errorf("ID = %v, want bld_test (last-seen build must still be returned on timeout)", latest.ID)
	}
	if got := atomic.LoadInt32(requestCount); got == 0 {
		t.Error("requestCount = 0, want at least one poll before giving up")
	}
}

func TestWaitForBuildDigest_ContextCancelled(t *testing.T) {
	// An already-cancelled context (e.g. Terraform interrupting the apply) must stop the
	// wait immediately rather than spending the full timeout polling.
	server, requestCount := digestPollTestServer(t, 1000, "unused")
	client := NewClientWithToken(server.URL, "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	build := &BuildResult{ID: "bld_test", Status: "succeeded"}
	latest, settled := waitForBuildDigestWithTiming(ctx, client, build, time.Second, time.Millisecond)

	if settled {
		t.Error("settled = true, want false (context already cancelled)")
	}
	if latest != build {
		t.Error("latest build was changed, want the original build returned unchanged")
	}
	if got := atomic.LoadInt32(requestCount); got != 0 {
		t.Errorf("requestCount = %d, want 0 (must not poll once ctx is already done)", got)
	}
}

func TestAddDigestNotSettledWarning(t *testing.T) {
	var diags diag.Diagnostics

	AddDigestNotSettledWarning(&diags, "bld_test")

	if diags.HasError() {
		t.Fatal("expected a warning, not an error")
	}
	if got := diags.WarningsCount(); got != 1 {
		t.Fatalf("WarningsCount() = %d, want 1", got)
	}

	w := diags[0]
	if !strings.Contains(w.Detail(), "bld_test") {
		t.Errorf("Detail() = %q, want it to mention the build ID", w.Detail())
	}
	if !strings.Contains(strings.ToLower(w.Detail()), "digest") {
		t.Errorf("Detail() = %q, want it to mention digest", w.Detail())
	}
}
