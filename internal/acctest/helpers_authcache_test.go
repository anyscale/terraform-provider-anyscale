package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// resetAuthProbeCache clears ValidateAuth's package-level cache and restores
// it after the test, so this test can never leak a cached answer into a real
// acceptance test sharing the same test binary run.
func resetAuthProbeCache(t *testing.T) {
	t.Helper()
	authProbeMutex.Lock()
	origDone, origStatus := authProbeDone, authProbeStatus
	authProbeDone, authProbeStatus = false, 0
	authProbeMutex.Unlock()

	t.Cleanup(func() {
		authProbeMutex.Lock()
		authProbeDone, authProbeStatus = origDone, origStatus
		authProbeMutex.Unlock()
	})
}

// recordingAuthT stands in for *testing.T so validateAuth's fail/skip outcome
// can be observed without failing the calling test. Fatalf and Skipf stop the
// call the way the real methods do, via a panic that runValidateAuth recovers.
type recordingAuthT struct {
	fataled, skipped bool
	msg              string
}

type authStop struct{}

func (r *recordingAuthT) Helper()             {}
func (r *recordingAuthT) Logf(string, ...any) {}
func (r *recordingAuthT) Fatalf(format string, args ...any) {
	r.fataled, r.msg = true, fmt.Sprintf(format, args...)
	panic(authStop{})
}
func (r *recordingAuthT) Skipf(format string, args ...any) {
	r.skipped, r.msg = true, fmt.Sprintf(format, args...)
	panic(authStop{})
}

func runValidateAuth() (r *recordingAuthT) {
	r = &recordingAuthT{}
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(authStop); !ok {
				panic(v)
			}
		}
	}()
	validateAuth(r)
	return r
}

// resetAllConfiguredCloudsCache clears GetAllConfiguredClouds's package-level
// cache and restores it after the test, for the same reason as
// resetAuthProbeCache above.
func resetAllConfiguredCloudsCache(t *testing.T) {
	t.Helper()
	allConfiguredCloudsMutex.Lock()
	origClouds, origCached := cachedAllConfiguredClouds, allConfiguredCloudsCached
	cachedAllConfiguredClouds, allConfiguredCloudsCached = nil, false
	allConfiguredCloudsMutex.Unlock()

	t.Cleanup(func() {
		allConfiguredCloudsMutex.Lock()
		cachedAllConfiguredClouds, allConfiguredCloudsCached = origClouds, origCached
		allConfiguredCloudsMutex.Unlock()
	})
}

// TestValidateAuth_RejectedTokenFailsAndIsCached pins the fail-closed
// contract: a token the API rejects must fail the test, never skip it. When
// this skipped, an expired CI secret made every acceptance test skip and both
// acctest shards reported green with nothing executed.
func TestValidateAuth_RejectedTokenFailsAndIsCached(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			resetAuthProbeCache(t)

			var requestCount int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&requestCount, 1)
				w.WriteHeader(status)
			}))
			defer server.Close()

			t.Setenv("ANYSCALE_API_URL", server.URL)
			t.Setenv("ANYSCALE_CLI_TOKEN", "rejected-token")

			for i, label := range []string{"live probe", "cached answer"} {
				r := runValidateAuth()
				if !r.fataled || r.skipped {
					t.Fatalf("call %d (%s): fataled=%v skipped=%v, want a failure, not a skip", i+1, label, r.fataled, r.skipped)
				}
			}
			if got := atomic.LoadInt64(&requestCount); got != 1 {
				t.Errorf("request count = %d, want exactly 1 - the second call should have used the cached answer, not reprobed", got)
			}
		})
	}
}

func TestValidateAuth_AcceptedTokenPasses(t *testing.T) {
	resetAuthProbeCache(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "good-token")

	if r := runValidateAuth(); r.fataled || r.skipped {
		t.Fatalf("fataled=%v skipped=%v (%s), want neither for an accepted token", r.fataled, r.skipped, r.msg)
	}
}

func TestValidateAuth_DoesNotCacheARequestError(t *testing.T) {
	resetAuthProbeCache(t)

	// Point at a server that immediately closes the connection, so every
	// request fails at the transport level (a stand-in for a transient
	// network blip) rather than returning any HTTP status.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // closed before any request - guarantees a connection error

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "some-token")

	// A request error is logged and tolerated, not treated as a rejected
	// token, and must not be cached as one - otherwise a single transient
	// blip would suppress the real auth check for every later test.
	for i := 0; i < 2; i++ {
		if r := runValidateAuth(); r.fataled || r.skipped {
			t.Fatalf("call %d: fataled=%v skipped=%v, want neither on a request error", i+1, r.fataled, r.skipped)
		}
	}

	authProbeMutex.Lock()
	done := authProbeDone
	authProbeMutex.Unlock()
	if done {
		t.Error("authProbeDone = true after only request errors, want false so a later call still gets a real chance to probe")
	}
}

func TestGetAllConfiguredClouds_CachesAndDoesNotReprobe(t *testing.T) {
	resetAllConfiguredCloudsCache(t)

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

// TestGetAllConfiguredClouds_TransientResourcesCheckFailureNotCached
// reproduces the exact gap found in review: a per-cloud resources
// check that fails transiently (not a confirmed absence of resources) must
// not get baked into the cache, or a single blip would silently and
// permanently exclude a genuinely healthy cloud for the rest of the whole
// test binary run.
func TestGetAllConfiguredClouds_TransientResourcesCheckFailureNotCached(t *testing.T) {
	resetAllConfiguredCloudsCache(t)

	var resourcesCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/clouds":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":"cld_1","name":"test-cloud","provider":"AWS","compute_stack":"VM"}]}`))
		case "/api/v2/clouds/cld_1/resources":
			call := atomic.AddInt64(&resourcesCalls, 1)
			if call == 1 {
				// First check: a transient failure, not a real "no resources"
				// answer.
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Every later check: the cloud is genuinely healthy.
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
	if len(first) != 0 {
		t.Fatalf("first call = %+v, want zero clouds - the transient 500 was correctly inconclusive this round", first)
	}

	second := GetAllConfiguredClouds(t)
	if len(second) != 1 || second[0].ID != "cld_1" {
		t.Fatalf("second call = %+v, want cld_1 - the first round must not have been cached, so this call gets a real re-check and sees the now-healthy cloud", second)
	}

	if got := atomic.LoadInt64(&resourcesCalls); got != 2 {
		t.Errorf("resources-check request count = %d, want exactly 2 - the second GetAllConfiguredClouds call must have actually re-probed, not served a stale cached empty result", got)
	}
}

// TestGetAllConfiguredClouds_ConfirmedUnhealthyCloudIsCached proves the fix
// above did not overcorrect: a real, definitive "no resources" answer (a
// clean 200 with an empty list, not an error) is a genuine result and must
// still be cached like any other definitive outcome.
func TestGetAllConfiguredClouds_ConfirmedUnhealthyCloudIsCached(t *testing.T) {
	resetAllConfiguredCloudsCache(t)

	var resourcesCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/clouds":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":"cld_1","name":"test-cloud","provider":"AWS","compute_stack":"VM"}]}`))
		case "/api/v2/clouds/cld_1/resources":
			atomic.AddInt64(&resourcesCalls, 1)
			// Confirmed, definitive: this cloud genuinely has no resources.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")

	first := GetAllConfiguredClouds(t)
	second := GetAllConfiguredClouds(t)

	if len(first) != 0 || len(second) != 0 {
		t.Fatalf("first = %+v, second = %+v, want both empty - the cloud is confirmed to have no resources", first, second)
	}
	if got := atomic.LoadInt64(&resourcesCalls); got != 1 {
		t.Errorf("resources-check request count = %d, want exactly 1 - a confirmed definitive answer must still be cached, not re-probed every call", got)
	}
}
