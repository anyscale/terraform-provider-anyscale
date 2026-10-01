package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// runCloudResourceDelete drives CloudResourceResource's real Delete() against
// a state holding only id and is_default=false - the stale-is_default case
// where Delete reaches the API instead of short-circuiting.
func runCloudResourceDelete(t *testing.T, serverURL, id string) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()
	r := &CloudResourceResource{client: NewClientWithToken(serverURL, "test-token")}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfState := tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	diags := tfState.SetAttribute(ctx, path.Root("id"), id)
	diags.Append(tfState.SetAttribute(ctx, path.Root("is_default"), false)...)
	if diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}

	deleteResp := &resource.DeleteResponse{State: tfState}
	r.Delete(ctx, resource.DeleteRequest{State: tfState}, deleteResp)
	return deleteResp.Diagnostics
}

// newRemoveResourceServer answers DELETE remove_resource with status/body and
// counts the calls, so a passing no-op case is proven to have reached the API.
func newRemoveResourceServer(t *testing.T, cloudID string, status int, body string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/clouds/"+cloudID+"/remove_resource" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// The backend refuses to remove a cloud's primary resource with a 400
// (clouds_resource.py remove_resource). That resource's lifecycle belongs to
// the cloud, so Delete must treat it as the same no-op as the is_default
// branch - not fail destroy.
func TestCloudResourceDelete_PrimaryResource400IsNoOp(t *testing.T) {
	const cloudID = "cld_primary400"
	var calls atomic.Int32
	server := newRemoveResourceServer(t, cloudID, http.StatusBadRequest,
		`{"error": {"detail": "Cloud resource main is the primary resource and cannot be removed without deleting the cloud."}}`, &calls)

	diags := runCloudResourceDelete(t, server.URL, cloudID+":main")
	if diags.HasError() {
		t.Fatalf("Delete returned errors for the primary-resource 400, want a no-op: %v", diags)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("remove_resource calls = %d, want 1 (the stale-is_default path must reach the API)", got)
	}
}

// Positive control for the case above: any other 400 is a real failure.
func TestCloudResourceDelete_Other400Errors(t *testing.T) {
	const cloudID = "cld_other400"
	var calls atomic.Int32
	server := newRemoveResourceServer(t, cloudID, http.StatusBadRequest,
		`{"error": {"detail": "Cloud resource main is still in use by 2 clusters."}}`, &calls)

	diags := runCloudResourceDelete(t, server.URL, cloudID+":main")
	if !diags.HasError() {
		t.Fatal("Delete returned no error for a non-primary 400, want an API error")
	}
	if got := diags.Errors()[0].Detail(); !strings.Contains(got, "(HTTP 400)") {
		t.Fatalf("error detail = %q, want it to carry the 400", got)
	}
}

// A 429 while polling must back off and retry, never be parsed as a cloud.
// The 429 body here is plain text, as rate limiters commonly send; before the
// fix 429 was an accepted status, so this body reached json.Unmarshal and the
// wait failed with "failed to parse cloud response". Takes one initial backoff
// interval (5s) to run.
func TestWaitForCloudReady_RetriesOn429(t *testing.T) {
	const cloudID = "cld_ratelimited"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, "Too Many Requests")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "status": "ready", "state": "ACTIVE"}}`, cloudID)
	}))
	t.Cleanup(server.Close)

	client := NewClientWithToken(server.URL, "test-token")
	if err := waitForCloudReady(context.Background(), client, cloudID, time.Minute); err != nil {
		t.Fatalf("waitForCloudReady returned %v, want nil after one 429 then ready", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("GET calls = %d, want 2 (one 429, one ready)", got)
	}
}
