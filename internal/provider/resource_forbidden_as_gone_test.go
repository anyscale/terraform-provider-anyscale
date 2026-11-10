package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The API answers HTTP 403 "Permission denied", not 404, for a deleted or nonexistent cloud,
// cloud resource, project, or service. These tests drive the real Read and Delete of those four
// resources against a 403 and assert both halves of the contract: the resource is treated as
// gone (removed from state, or a successful destroy), and the warning that names the 403 is
// present. The project's delete-time 403 retry is covered by TestProjectResourceDelete_403Retry.

// forbiddenStateFixture builds a state holding only attrs, on r's real schema.
func forbiddenStateFixture(t *testing.T, r resource.Resource, attrs map[string]any) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	state := tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	for name, value := range attrs {
		if diags := state.SetAttribute(ctx, path.Root(name), value); diags.HasError() {
			t.Fatalf("failed to set %s: %v", name, diags)
		}
	}
	return state
}

func runForbiddenRead(t *testing.T, r resource.Resource, attrs map[string]any) *resource.ReadResponse {
	t.Helper()
	state := forbiddenStateFixture(t, r, attrs)
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	return resp
}

func runForbiddenDelete(t *testing.T, r resource.Resource, attrs map[string]any) diag.Diagnostics {
	t.Helper()
	state := forbiddenStateFixture(t, r, attrs)
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
	return resp.Diagnostics
}

func diagsWarningSummaries(diags diag.Diagnostics) []string {
	var out []string
	for _, d := range diags.Warnings() {
		out = append(out, d.Summary())
	}
	return out
}

// assertTreatedAsDeleted fails unless diags carry no error and exactly the one 403 warning, worded
// for a refresh (duplicate on the next apply) or, with destroy, for a destroy (may still exist).
func assertTreatedAsDeleted(t *testing.T, diags diag.Diagnostics, kind, id string, destroy bool) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("want no error for a 403 treated as gone, got: %v", diags)
	}
	warnings := diags.Warnings()
	if len(warnings) != 1 || warnings[0].Summary() != kind+" Treated As Deleted" {
		t.Fatalf("warnings = %v, want exactly one %q", diagsWarningSummaries(diags), kind+" Treated As Deleted")
	}
	detail := warnings[0].Detail()
	if destroy {
		if !strings.Contains(detail, id+" returned HTTP 403 during destroy") || !strings.Contains(detail, "may still exist outside Terraform") {
			t.Errorf("warning detail = %q, want it to name %s, the 403 during destroy, and that it may still exist", detail, id)
		}
		return
	}
	if !strings.Contains(detail, id+" returned HTTP 403;") || !strings.Contains(detail, "the next apply would create a duplicate") {
		t.Errorf("warning detail = %q, want it to name %s, the 403, and the duplicate on the next apply", detail, id)
	}
}

func assertProjectTreatedAsDeleted(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("want no error for a 403 treated as gone, got: %v", diags)
	}
	if !diagsContainSummary(diags, "Project Treated As Deleted") {
		t.Fatalf("warnings = %v, want %q", diagsWarningSummaries(diags), "Project Treated As Deleted")
	}
}

// forbiddenServer answers 403 to every request matching method and path, 200 with okBody to any
// other request listed in ok (keyed "METHOD path"), and fails the test on anything else.
func forbiddenServer(t *testing.T, method, forbiddenPath string, ok map[string]string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == method && r.URL.Path == forbiddenPath {
			hits.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "Permission denied"}}`)
			return
		}
		if body, found := ok[r.Method+" "+r.URL.Path]; found {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, body)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestForbiddenTreatedAsGone_Read(t *testing.T) {
	tests := []struct {
		name          string
		resource      func(*Client) resource.Resource
		attrs         map[string]any
		forbiddenPath string
		kind, id      string
	}{
		{
			name:          "anyscale_cloud",
			resource:      func(c *Client) resource.Resource { return &CloudResource{client: c} },
			attrs:         map[string]any{"id": "cld_gone"},
			forbiddenPath: "/api/v2/clouds/cld_gone",
			kind:          "Cloud", id: "cld_gone",
		},
		{
			name:          "anyscale_cloud_resource",
			resource:      func(c *Client) resource.Resource { return &CloudResourceResource{client: c} },
			attrs:         map[string]any{"id": "cld_gone:secondary"},
			forbiddenPath: "/api/v2/clouds/cld_gone/resources",
			kind:          "Cloud Resource", id: "cld_gone:secondary",
		},
		{
			name:          "anyscale_project",
			resource:      func(c *Client) resource.Resource { return &ProjectResource{client: c} },
			attrs:         map[string]any{"id": "prj_gone"},
			forbiddenPath: "/api/v2/projects/prj_gone",
			kind:          "Project", id: "prj_gone",
		},
		{
			name:          "anyscale_service",
			resource:      func(c *Client) resource.Resource { return &ServiceResource{client: c} },
			attrs:         map[string]any{"id": "svc_gone"},
			forbiddenPath: "/api/v2/services-v2/svc_gone",
			kind:          "Service", id: "svc_gone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			server := forbiddenServer(t, http.MethodGet, tt.forbiddenPath, nil, &hits)

			resp := runForbiddenRead(t, tt.resource(NewClientWithToken(server.URL, "test-token")), tt.attrs)

			assertTreatedAsDeleted(t, resp.Diagnostics, tt.kind, tt.id, false)
			if !resp.State.Raw.IsNull() {
				t.Errorf("state was not removed after a 403 read")
			}
			if hits.Load() == 0 {
				t.Errorf("the 403 endpoint was never requested")
			}
		})
	}
}

// A 403 from a delete call is ambiguous: the object may be gone, or the credentials may see it but
// not be allowed to delete it. Each Delete reads the object back to tell which. Only a read-back
// that can no longer see it ends the destroy; a still-readable object fails it, so a refused
// delete never drops a live object from state.
func TestForbiddenDelete_ReadBack(t *testing.T) {
	const listed = `{"results": [{"name": "secondary", "cloud_resource_id": "cldrsrc_x"}], "metadata": {}}`
	const notListed = `{"results": [{"name": "main", "cloud_resource_id": "cldrsrc_main"}], "metadata": {}}`

	resources := []struct {
		name          string
		resource      func(*Client) resource.Resource
		attrs         map[string]any
		method        string
		forbiddenPath string
		readBackPath  string
		// existsBody is the read-back's 200 body while the object exists; goneBody, when set, is a
		// 200 body that shows it gone (a listing without it) in place of a 404.
		existsBody, goneBody string
		ok                   map[string]string
		kind, id             string
	}{
		{
			name:          "anyscale_cloud",
			resource:      func(c *Client) resource.Resource { return &CloudResource{client: c} },
			attrs:         map[string]any{"id": "cld_x"},
			method:        http.MethodDelete,
			forbiddenPath: "/api/v2/clouds/cld_x",
			readBackPath:  "/api/v2/clouds/cld_x",
			existsBody:    `{"result": {"id": "cld_x"}}`,
			ok: map[string]string{
				"GET /api/v2/machine_pools/": `{"result": {"machine_pools": []}}`,
				// No resources: Delete skips the load balancer teardown.
				"GET /api/v2/clouds/cld_x/resources": `{"results": [], "metadata": {"next_paging_token": null}}`,
			},
			kind: "Cloud", id: "cld_x",
		},
		{
			name:          "anyscale_cloud_resource",
			resource:      func(c *Client) resource.Resource { return &CloudResourceResource{client: c} },
			attrs:         map[string]any{"id": "cld_x:secondary", "is_default": false},
			method:        http.MethodDelete,
			forbiddenPath: "/api/v2/clouds/cld_x/remove_resource",
			readBackPath:  "/api/v2/clouds/cld_x/resources",
			existsBody:    listed,
			goneBody:      notListed,
			kind:          "Cloud Resource", id: "cld_x:secondary",
		},
		{
			// Outside the delete-time retry window, so the first 403 is final.
			name:          "anyscale_project",
			resource:      func(c *Client) resource.Resource { return &ProjectResource{client: c} },
			attrs:         map[string]any{"id": "prj_x", "created_at": "2026-01-01T00:00:00Z"},
			method:        http.MethodDelete,
			forbiddenPath: "/api/v2/projects/prj_x",
			readBackPath:  "/api/v2/projects/prj_x",
			existsBody:    `{"result": {"id": "prj_x"}}`,
			kind:          "Project", id: "prj_x",
		},
		{
			name:          "anyscale_service terminate",
			resource:      func(c *Client) resource.Resource { return &ServiceResource{client: c} },
			attrs:         map[string]any{"id": "svc_x"},
			method:        http.MethodPost,
			forbiddenPath: "/api/v2/services-v2/svc_x/terminate",
			readBackPath:  "/api/v2/services-v2/svc_x",
			existsBody:    `{"result": {"id": "svc_x", "current_state": "RUNNING", "goal_state": "RUNNING"}}`,
			kind:          "Service", id: "svc_x",
		},
	}

	readBacks := []struct {
		name    string
		status  int
		gone    bool   // a 404, or the resource's goneBody when it has one
		outcome string // "refused", "warned", "silent", "unconfirmed"
	}{
		{name: "still readable", status: http.StatusOK, outcome: "refused"},
		{name: "read-back 403", status: http.StatusForbidden, outcome: "warned"},
		{name: "read-back 404", status: http.StatusNotFound, gone: true, outcome: "silent"},
		{name: "read-back fails", status: http.StatusBadRequest, outcome: "unconfirmed"},
	}

	for _, rt := range resources {
		for _, rb := range readBacks {
			t.Run(rt.name+"/"+rb.name, func(t *testing.T) {
				var deletes, readBackHits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == rt.method && r.URL.Path == rt.forbiddenPath:
						deletes.Add(1)
						w.WriteHeader(http.StatusForbidden)
						_, _ = fmt.Fprint(w, `{"error": {"detail": "Permission denied"}}`)
					case r.Method == http.MethodGet && r.URL.Path == rt.readBackPath:
						readBackHits.Add(1)
						status, body := rb.status, `{"error": {"detail": "read-back says no"}}`
						switch {
						case rb.status == http.StatusOK:
							body = rt.existsBody
						case rb.gone && rt.goneBody != "":
							status, body = http.StatusOK, rt.goneBody
						}
						w.WriteHeader(status)
						_, _ = fmt.Fprint(w, body)
					default:
						if body, found := rt.ok[r.Method+" "+r.URL.Path]; found {
							w.WriteHeader(http.StatusOK)
							_, _ = fmt.Fprint(w, body)
							return
						}
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusTeapot)
					}
				}))
				t.Cleanup(server.Close)

				diags := runForbiddenDelete(t, rt.resource(NewClientWithToken(server.URL, "test-token")), rt.attrs)

				if got := deletes.Load(); got != 1 {
					t.Errorf("403 endpoint requests = %d, want 1", got)
				}
				if readBackHits.Load() == 0 {
					t.Fatalf("the object was never read back after the 403")
				}
				assertForbiddenDeleteOutcome(t, diags, rt.kind, rt.id, rb.outcome)
			})
		}
	}
}

// assertForbiddenDeleteOutcome checks how a Delete that got a 403 ended. "refused" and
// "unconfirmed" fail the destroy, so the framework keeps the resource in state; "warned" and
// "silent" let it finish.
func assertForbiddenDeleteOutcome(t *testing.T, diags diag.Diagnostics, kind, id, outcome string) {
	t.Helper()
	noun := strings.ToLower(kind)
	switch outcome {
	case "refused":
		if !diags.HasError() {
			t.Fatalf("a refused delete of a still-readable %s ended the destroy; want an error, got %v", noun, diags)
		}
		errs := diags.Errors()
		if errs[0].Summary() != kind+" Was Not Deleted" {
			t.Errorf("summary = %q, want %q", errs[0].Summary(), kind+" Was Not Deleted")
		}
		for _, want := range []string{id, "HTTP 403 (Permission denied)", "still exists", "terraform state rm"} {
			if !strings.Contains(errs[0].Detail(), want) {
				t.Errorf("detail %q does not contain %q", errs[0].Detail(), want)
			}
		}
	case "warned":
		assertTreatedAsDeleted(t, diags, kind, id, true)
	case "silent":
		if len(diags) != 0 {
			t.Errorf("a 404 read-back is a confirmed deletion; want no diagnostics, got %v", diags)
		}
	case "unconfirmed":
		if !diags.HasError() {
			t.Fatalf("a failed read-back ended the destroy; want an error, got %v", diags)
		}
		if got := diags.Errors()[0].Summary(); got != "Could Not Confirm "+kind+" Deletion" {
			t.Errorf("summary = %q, want %q", got, "Could Not Confirm "+kind+" Deletion")
		}
		if !strings.Contains(diags.Errors()[0].Detail(), "Retry the destroy") {
			t.Errorf("detail %q does not say to retry", diags.Errors()[0].Detail())
		}
	default:
		t.Fatalf("unknown outcome %q", outcome)
	}
}

// The service's final DELETE, after termination has landed, is a second place the 403 can arrive.
// The terminated service can still be read, so the 403 is a refusal: the destroy fails rather
// than dropping a service that still exists.
func TestForbiddenTreatedAsGone_ServiceFinalDelete(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/services-v2/svc_gone/terminate":
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"result": {}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/svc_gone":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result": {"id": "svc_gone", "current_state": "TERMINATED", "goal_state": "TERMINATED"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/services-v2/svc_gone":
			hits.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "Permission denied"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)

	diags := runForbiddenDelete(t, &ServiceResource{client: NewClientWithToken(server.URL, "test-token")}, map[string]any{"id": "svc_gone"})

	assertForbiddenDeleteOutcome(t, diags, "Service", "svc_gone", "refused")
	if got := hits.Load(); got != 1 {
		t.Errorf("DELETE requests = %d, want 1", got)
	}
}

// The backend can remove a service while it terminates. The termination wait's GET then answers
// 404, or 403 under the newer access model; either way the destroy is done and the final DELETE
// is never sent. Only the 403 warns.
func TestForbiddenTreatedAsGone_ServiceGoneDuringTerminationWait(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		warns  bool
	}{
		{"403", http.StatusForbidden, true},
		{"404", http.StatusNotFound, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/services-v2/svc_gone/terminate":
					w.WriteHeader(http.StatusAccepted)
					_, _ = fmt.Fprint(w, `{"result": {}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/svc_gone":
					w.WriteHeader(tt.status)
					_, _ = fmt.Fprint(w, `{"error": {"detail": "Permission denied"}}`)
				case r.Method == http.MethodDelete:
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusTeapot)
				}
			}))
			t.Cleanup(server.Close)

			diags := runForbiddenDelete(t, &ServiceResource{client: NewClientWithToken(server.URL, "test-token")}, map[string]any{"id": "svc_gone"})

			if tt.warns {
				assertTreatedAsDeleted(t, diags, "Service", "svc_gone", true)
			} else if diags.HasError() || len(diags.Warnings()) != 0 {
				t.Errorf("a 404 during the wait: diags = %v, want none", diags)
			}
			if got := deletes.Load(); got != 0 {
				t.Errorf("DELETE requests = %d, want 0 once the service is gone", got)
			}
		})
	}
}

// A 404 keeps its silent removal: the warning is for 403 only.
func TestForbiddenTreatedAsGone_404StaysSilent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "Cloud not found"}}`)
	}))
	t.Cleanup(server.Close)

	resp := runForbiddenRead(t, &CloudResource{client: NewClientWithToken(server.URL, "test-token")}, map[string]any{"id": "cld_gone"})
	if len(resp.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %v, want none for a 404", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("state was not removed after a 404 read")
	}
}

// Positive control: any status other than 403 and 404 stays a Read error.
func TestForbiddenTreatedAsGone_OtherStatusStaysError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "bad"}}`)
	}))
	t.Cleanup(server.Close)

	resp := runForbiddenRead(t, &ProjectResource{client: NewClientWithToken(server.URL, "test-token")}, map[string]any{"id": "prj_x"})
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error for a 400 read")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state was removed after a 400 read")
	}
}

func TestIsGoneOrForbidden(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"404 sentinel", fmt.Errorf("%w: %w", ErrNotFound, &UnexpectedStatusError{StatusCode: http.StatusNotFound}), true},
		{"wrapped 403", fmt.Errorf("read: %w", &UnexpectedStatusError{StatusCode: http.StatusForbidden}), true},
		{"400", &UnexpectedStatusError{StatusCode: http.StatusBadRequest, Body: "status 403"}, false},
		{"500", &UnexpectedStatusError{StatusCode: http.StatusInternalServerError}, false},
		{"untyped text mentioning 403", errors.New("unexpected status 403: denied"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGoneOrForbidden(tt.err); got != tt.want {
				t.Errorf("isGoneOrForbidden(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestAddForbiddenTreatedAsDeletedWarning_OnlyFor403(t *testing.T) {
	var diags diag.Diagnostics
	addForbiddenTreatedAsDeletedWarning(&diags, "Cloud", "cld_x", fmt.Errorf("%w: gone", ErrNotFound))
	if len(diags) != 0 {
		t.Fatalf("a 404 added %v, want nothing", diags)
	}
	addForbiddenTreatedAsDeletedWarning(&diags, "Cloud", "cld_x", &UnexpectedStatusError{StatusCode: http.StatusForbidden})
	assertTreatedAsDeleted(t, diags, "Cloud", "cld_x", false)
}

func TestAddForbiddenTreatedAsDeletedOnDestroyWarning_OnlyFor403(t *testing.T) {
	var diags diag.Diagnostics
	addForbiddenTreatedAsDeletedOnDestroyWarning(&diags, "Cloud", "cld_x", fmt.Errorf("%w: gone", ErrNotFound))
	if len(diags) != 0 {
		t.Fatalf("a 404 added %v, want nothing", diags)
	}
	addForbiddenTreatedAsDeletedOnDestroyWarning(&diags, "Cloud Resource", "cld_x:r", &UnexpectedStatusError{StatusCode: http.StatusForbidden})
	assertTreatedAsDeleted(t, diags, "Cloud Resource", "cld_x:r", true)
	if detail := diags.Warnings()[0].Detail(); !strings.Contains(detail, "the cloud resource may still exist") {
		t.Errorf("warning detail = %q, want the lower-cased kind", detail)
	}
}

// readCloudState's hand-rolled status check must surface a 403 as a typed status so Read can
// recognize it, and keep every other non-200 error text as it was.
func TestReadCloudState_ForbiddenIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/clouds/cld_forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `denied`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `bad`)
		}
	}))
	t.Cleanup(server.Close)
	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}

	_, err := r.readCloudState(context.Background(), "cld_forbidden", &CloudResourceModel{}, nil)
	if !isHTTPStatus(err, http.StatusForbidden) {
		t.Fatalf("403 error = %v, want an UnexpectedStatusError with status 403", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("a 403 must not wrap ErrNotFound: %v", err)
	}

	// Any other status is an error carrying its own status, not a 403.
	_, err = r.readCloudState(context.Background(), "cld_bad", &CloudResourceModel{}, nil)
	if !isHTTPStatus(err, http.StatusBadRequest) {
		t.Errorf("400 error = %v, want an UnexpectedStatusError with status 400", err)
	}
}
