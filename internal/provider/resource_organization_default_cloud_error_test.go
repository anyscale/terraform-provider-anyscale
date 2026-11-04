package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// A transport failure on the set-default POST must name the operation once. AddAPIError
// prints err.Error() for non-status errors, so a callee wrap that repeats the caller's op
// reads "Failed to set organization default cloud: set organization default cloud: ...".
func TestSetOrganizationDefaultCloud_TransportErrorNamesOperationOnce(t *testing.T) {
	const cloudID = "cld_00000000000000000000000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds/"+cloudID:
			_, _ = w.Write([]byte(`{"result": {"id": "` + cloudID + `"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/organizations/update_default_cloud":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	var sleeps []time.Duration
	client := newRetryTestClient(server.URL, &sleeps)

	err := setOrganizationDefaultCloud(context.Background(), client, cloudID)
	if err == nil {
		t.Fatal("expected an error from a dropped connection")
	}

	var diags diag.Diagnostics
	AddAPIError(&diags, "set organization default cloud", err)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	detail := diags.Errors()[0].Detail()
	if n := strings.Count(strings.ToLower(detail), "set organization default cloud"); n != 1 {
		t.Errorf("operation named %d times, want 1: %q", n, detail)
	}
}
