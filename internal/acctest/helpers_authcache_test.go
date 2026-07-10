package acctest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// resetAuthProbeCache clears ValidateAuthOrSkip's package-level cache and
// restores it after the test, so this test can never leak a cached answer
// into a real acceptance test sharing the same test binary run.
func resetAuthProbeCache(t *testing.T) {
	t.Helper()
	authProbeMutex.Lock()
	origDone, origInvalid := authProbeDone, authProbeInvalid
	authProbeDone, authProbeInvalid = false, false
	authProbeMutex.Unlock()

	t.Cleanup(func() {
		authProbeMutex.Lock()
		authProbeDone, authProbeInvalid = origDone, origInvalid
		authProbeMutex.Unlock()
	})
}

func TestValidateAuthOrSkip_CachesA401AndDoesNotReprobe(t *testing.T) {
	resetAuthProbeCache(t)

	var requestCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requestCount, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "invalid-token")

	t.Run("first call probes live and skips", func(t *testing.T) {
		ValidateAuthOrSkip(t)
		t.Fatal("expected ValidateAuthOrSkip to skip on 401, execution continued")
	})

	t.Run("second call uses the cached answer, no new request", func(t *testing.T) {
		ValidateAuthOrSkip(t)
		t.Fatal("expected ValidateAuthOrSkip to skip from cache on 401, execution continued")
	})

	if got := atomic.LoadInt64(&requestCount); got != 1 {
		t.Errorf("request count = %d, want exactly 1 - the second call should have used the cached answer, not reprobed", got)
	}
}

func TestValidateAuthOrSkip_DoesNotCacheARequestError(t *testing.T) {
	resetAuthProbeCache(t)

	// Point at a server that immediately closes the connection, so every
	// request fails at the transport level (a stand-in for a transient
	// network blip) rather than returning any HTTP status.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // closed before any request - guarantees a connection error

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "some-token")

	// Neither call should skip: a request error is logged and tolerated, not
	// treated as a confirmed-invalid token, and must not be cached as one -
	// otherwise a single transient blip would silently suppress the real 401
	// check for every later test in the run.
	ValidateAuthOrSkip(t)
	ValidateAuthOrSkip(t)

	authProbeMutex.Lock()
	done := authProbeDone
	authProbeMutex.Unlock()
	if done {
		t.Error("authProbeDone = true after only request errors, want false so a later call still gets a real chance to probe")
	}
}

func TestGetAllConfiguredClouds_CachesAndDoesNotReprobe(t *testing.T) {
	allConfiguredCloudsMutex.Lock()
	origClouds, origCached := cachedAllConfiguredClouds, allConfiguredCloudsCached
	cachedAllConfiguredClouds, allConfiguredCloudsCached = nil, false
	allConfiguredCloudsMutex.Unlock()
	t.Cleanup(func() {
		allConfiguredCloudsMutex.Lock()
		cachedAllConfiguredClouds, allConfiguredCloudsCached = origClouds, origCached
		allConfiguredCloudsMutex.Unlock()
	})

	var listCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/clouds":
			atomic.AddInt64(&listCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":"cld_1","name":"test-cloud","provider":"AWS","compute_stack":"VM"}]}`))
		case "/api/v2/clouds/cld_1/resources":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":"res_1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")

	first := GetAllConfiguredClouds(t)
	second := GetAllConfiguredClouds(t)

	if len(first) != 1 || first[0].ID != "cld_1" {
		t.Fatalf("first call = %+v, want one cloud cld_1", first)
	}
	if len(second) != 1 || second[0].ID != "cld_1" {
		t.Fatalf("second call = %+v, want the same cached cloud", second)
	}
	if got := atomic.LoadInt64(&listCalls); got != 1 {
		t.Errorf("list-clouds request count = %d, want exactly 1 - the second call should have used the cache", got)
	}
}
