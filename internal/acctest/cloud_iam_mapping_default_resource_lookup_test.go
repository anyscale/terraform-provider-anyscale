package acctest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

func newCloudResourcesListServer(t *testing.T, status int, body string) *provider.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return provider.NewClientWithToken(server.URL, "test-token")
}

// The real-infra IAM mapping tests skip when the test cloud has no default
// cloud_resource. A backend failure must fail instead: read as an empty list,
// it would silently turn every test in the suite into a skip.
func TestFindDefaultCloudResourceID_Non200IsAnError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		client := newCloudResourcesListServer(t, status, `{"error":{"detail":"nope"}}`)
		id, found, err := findDefaultCloudResourceID(context.Background(), client, "cld_x")
		if err == nil {
			t.Errorf("status %d: expected an error, got id=%q found=%v", status, id, found)
		}
	}
}

// The "not applicable" skip condition survives: a 200 with no default
// resource is found=false with no error.
func TestFindDefaultCloudResourceID_NoDefaultIsNotFound(t *testing.T) {
	client := newCloudResourcesListServer(t, http.StatusOK,
		`{"results":[{"cloud_resource_id":"cldrsrc_a","is_default":false}]}`)
	id, found, err := findDefaultCloudResourceID(context.Background(), client, "cld_x")
	if err != nil || found || id != "" {
		t.Fatalf("got id=%q found=%v err=%v, want not found and no error", id, found, err)
	}
}

// Positive control: the default resource is returned.
func TestFindDefaultCloudResourceID_ReturnsDefault(t *testing.T) {
	client := newCloudResourcesListServer(t, http.StatusOK,
		`{"results":[{"cloud_resource_id":"cldrsrc_a","is_default":false},{"cloud_resource_id":"cldrsrc_b","is_default":true}]}`)
	id, found, err := findDefaultCloudResourceID(context.Background(), client, "cld_x")
	if err != nil || !found || id != "cldrsrc_b" {
		t.Fatalf("got id=%q found=%v err=%v, want cldrsrc_b", id, found, err)
	}
}
