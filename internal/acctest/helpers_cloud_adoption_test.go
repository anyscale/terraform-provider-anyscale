package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCloudResolverServer serves a clouds list containing the given names, and
// records whether an ephemeral-cloud CREATE was attempted.
//
// The pinned fixture name is deliberately never among them: every test here is
// about what happens when it does NOT resolve, which is the wrong-org signal.
func newCloudResolverServer(t *testing.T, cloudNames ...string) (*httptest.Server, func() bool) {
	t.Helper()
	created := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result":{"id":"cld_created","name":"tfacc-ephemeral-x"}}`))
			return
		}
		items := make([]string, 0, len(cloudNames))
		for i, n := range cloudNames {
			items = append(items, fmt.Sprintf(
				`{"id":"cld_%d","name":%q,"created_at":"2026-01-0%dT00:00:00Z"}`, i, n, i+1))
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(items, ","))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, func() bool { return created }
}

// pointResolverAt redirects the acctest client at a mock and clears every
// resolution override, so the code under test takes the pinned-fixture ->
// auto-discovery path rather than an env shortcut.
func pointResolverAt(t *testing.T, url string) {
	t.Helper()
	t.Setenv("ANYSCALE_API_URL", url)
	t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")
	t.Setenv("ANYSCALE_TEST_CLOUD_ID", "")
	t.Setenv("ANYSCALE_TEST_CLOUD_NAME", "")
	t.Setenv("ANYSCALE_TEST_CREATE_CLOUD", "")
	t.Setenv("ANYSCALE_TEST_ALLOW_CLOUD_ADOPTION", "")
}

// TestAutoDiscoverRefusesToAdoptExistingCloud is the guard against the 2026-08-02
// incident: the pinned fixture did not resolve, the resolver fell through, and
// four tfacc- clouds were created in a user's own working organization.
//
// The refusal is keyed on ADOPTION, not on the pinned lookup failing — see the
// create-path test below for the half that must still work.
func TestAutoDiscoverRefusesToAdoptExistingCloud(t *testing.T) {
	// A tfacc- name specifically: auto-discovery scores it priority 9, so this is
	// exactly the "previous wrong-org run left a plausible fixture behind" case
	// that made the leak self-concealing.
	server, wasCreated := newCloudResolverServer(t, "somebody-elses-prod", "tfacc-leftover-from-a-bad-run")
	pointResolverAt(t, server.URL)

	_, _, err := autoDiscoverTestCloud(t)

	if err == nil {
		t.Fatal("expected a refusal: the pinned fixture is absent and this org already has clouds, " +
			"so adopting one would run the suite against someone else's infrastructure")
	}
	if wasCreated() {
		t.Error("refusal must not fall back to CREATING a cloud in an org that already has some")
	}
	for _, want := range []string{defaultKnownGoodCloudName, "ANYSCALE_TEST_ALLOW_CLOUD_ADOPTION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q so the reader can act on it; got: %v", want, err)
		}
	}
}

// TestAutoDiscoverStillCreatesInEmptyOrg pins the half that must NOT regress.
//
// CI sets ANYSCALE_TEST_CREATE_CLOUD=1 (ci.yml:194,:207) precisely so a fresh org
// can bootstrap itself. A guard that refused whenever the pinned fixture was
// missing would kill that path — which is why the refusal is placed after the
// empty-org branch rather than at the fall-through.
func TestAutoDiscoverStillCreatesInEmptyOrg(t *testing.T) {
	server, wasCreated := newCloudResolverServer(t) // no clouds at all
	pointResolverAt(t, server.URL)
	t.Setenv("ANYSCALE_TEST_CREATE_CLOUD", "1")

	_, _, err := autoDiscoverTestCloud(t)

	if err != nil {
		t.Fatalf("an EMPTY org with ANYSCALE_TEST_CREATE_CLOUD=1 must still bootstrap, got: %v", err)
	}
	if !wasCreated() {
		t.Error("expected an ephemeral cloud to be CREATED; nothing was POSTed")
	}
}

// TestAutoDiscoverAdoptionOptIn pins the escape hatch, so someone who genuinely
// means to run against a cloud this suite did not create still can.
func TestAutoDiscoverAdoptionOptIn(t *testing.T) {
	server, _ := newCloudResolverServer(t, "tfacc-deliberate-target")
	pointResolverAt(t, server.URL)
	t.Setenv("ANYSCALE_TEST_ALLOW_CLOUD_ADOPTION", "1")

	_, name, err := autoDiscoverTestCloud(t)

	if err != nil {
		t.Fatalf("explicit opt-in must be honoured, got refusal: %v", err)
	}
	if name != "tfacc-deliberate-target" {
		t.Errorf("adopted cloud = %q, want the one in the org", name)
	}
}
