package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestExtractArchivedValue is a direct table-driven unit test for the JSON
// dotted-path truthiness walk that backs every NewAPIArchivedDestroyCheck*
// helper. Before this, extractArchivedValue had zero direct test coverage --
// only indirect exercise via real acceptance tests gated behind TF_ACC and
// live credentials, meaning the normal `make test` unit lane never ran this
// code at all.
func TestExtractArchivedValue(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		path    string
		want    bool
		wantErr bool
	}{
		{name: "nested truthy bool", body: `{"result":{"archived_at":true}}`, path: "result.archived_at", want: true},
		{name: "nested truthy non-empty string", body: `{"result":{"archived_at":"2024-01-01T00:00:01Z"}}`, path: "result.archived_at", want: true},
		{name: "nested falsy bool", body: `{"result":{"archived_at":false}}`, path: "result.archived_at", want: false},
		{name: "nested empty string is not truthy", body: `{"result":{"archived_at":""}}`, path: "result.archived_at", want: false},
		{name: "nested null is not truthy", body: `{"result":{"archived_at":null}}`, path: "result.archived_at", want: false},
		{name: "missing leaf key", body: `{"result":{}}`, path: "result.archived_at", want: false},
		{name: "missing intermediate key", body: `{}`, path: "result.archived_at", want: false},
		{name: "intermediate is not an object", body: `{"result":"not-an-object"}`, path: "result.archived_at", want: false},
		{name: "single-segment top-level path", body: `{"is_archived":true}`, path: "is_archived", want: true},
		{name: "number leaf is not truthy", body: `{"result":{"archived_at":0}}`, path: "result.archived_at", want: false},
		{name: "array leaf is not truthy", body: `{"result":{"archived_at":[]}}`, path: "result.archived_at", want: false},
		{name: "invalid JSON errors", body: `not json`, path: "result.archived_at", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractArchivedValue([]byte(tc.body), tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for body %q, got none", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("extractArchivedValue(%q, %q) = %v, want %v", tc.body, tc.path, got, tc.want)
			}
		})
	}
}

// buildlessRegistryState hand-builds the exact terraform.State shape
// CheckDestroy sees for a registry resource whose Create() failed after call
// 1 (application_templates/byod) but before call 2 (builds/byod) ever
// succeeded -- see the orphan-prevention proof in
// internal/provider/resource_container_image_registry_orphan_prevention_test.go.
// Deliberately carries ONLY cluster_environment_id; there is no build_id key
// at all, not even an empty one, matching what the resource's defensive
// early State.Set actually produces.
func buildlessRegistryState(templateID string) *terraform.State {
	return &terraform.State{
		Modules: []*terraform.ModuleState{
			{
				Path: []string{"root"},
				Resources: map[string]*terraform.ResourceState{
					"anyscale_container_image_registry.test": {
						Type: "anyscale_container_image_registry",
						Primary: &terraform.InstanceState{
							ID:         templateID,
							Attributes: map[string]string{"cluster_environment_id": templateID},
						},
					},
				},
			},
		},
	}
}

// TestRegistryCheckDestroy_BuildlessTemplate_ArchivedSucceeds is the GATE
// proof for part B of the orphan-prevention gate: the registry's real
// CheckDestroy call
// (NewAPIArchivedDestroyCheckByAttr("anyscale_container_image_registry",
// "cluster_environment_id", "/api/v2/application_templates/%s",
// "result.archived_at") -- see resource_container_image_registry_acc_test.go)
// must correctly confirm cleanup of a template that never had a build,
// using nothing but the template's own id. It never needs, and must never
// require, a build_id.
func TestRegistryCheckDestroy_BuildlessTemplate_ArchivedSucceeds(t *testing.T) {
	const templateID = "apptemp_buildless_archived"
	var requestCount int
	var requestedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"id": templateID, "archived_at": "2024-01-01T00:00:01Z"},
		})
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-checkdestroy-buildless-archived")

	checkFn := NewAPIArchivedDestroyCheckByAttr(
		"anyscale_container_image_registry", "cluster_environment_id",
		"/api/v2/application_templates/%s", "result.archived_at",
	)

	if err := checkFn(buildlessRegistryState(templateID)); err != nil {
		t.Fatalf("CheckDestroy reported an error for a properly-archived build-less template: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly 1 GET request, got %d", requestCount)
	}
	wantPath := "/api/v2/application_templates/" + templateID
	if requestedPath != wantPath {
		t.Errorf("requested path = %q, want %q -- CheckDestroy must key on the template id, not a build id", requestedPath, wantPath)
	}
}

// TestRegistryCheckDestroy_BuildlessTemplate_GoneSucceeds covers the other
// success path: the template was hard-deleted (404) rather than found
// already-archived. Same build-less state as above.
func TestRegistryCheckDestroy_BuildlessTemplate_GoneSucceeds(t *testing.T) {
	const templateID = "apptemp_buildless_gone"
	var requestCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-checkdestroy-buildless-gone")

	checkFn := NewAPIArchivedDestroyCheckByAttr(
		"anyscale_container_image_registry", "cluster_environment_id",
		"/api/v2/application_templates/%s", "result.archived_at",
	)

	if err := checkFn(buildlessRegistryState(templateID)); err != nil {
		t.Fatalf("CheckDestroy reported an error for a 404'd build-less template: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly 1 GET request, got %d", requestCount)
	}
}

// TestRegistryCheckDestroy_KeyingOnBuildIDWouldSilentlySkipBuildlessOrphan is
// the regression guard for part B: it proves WHY the real call site keys on
// cluster_environment_id rather than build_id. A build-less orphan's state
// has no build_id attribute at all, so rs.Primary.Attributes["build_id"]
// evaluates to "" (Go's zero value for a missing map key, not an error or
// panic) and newAPIDestroyCheckImpl's `if id == "" { continue }` guard
// silently skips the resource entirely -- CheckDestroy reports success (nil)
// even though the mock server below never receives a single request and
// would have reported the template as a live, unarchived leak if asked. A
// false green, not a caught leak. This is the concrete failure mode the
// real code's choice of attribute avoids.
func TestRegistryCheckDestroy_KeyingOnBuildIDWouldSilentlySkipBuildlessOrphan(t *testing.T) {
	const templateID = "apptemp_buildless_wouldleak"
	var requestCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// A genuine, un-archived leak: 200 with no archived_at set.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"id": templateID}})
	}))
	defer server.Close()
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-checkdestroy-buildless-wouldleak")

	wrongKeyCheckFn := NewAPIArchivedDestroyCheckByAttr(
		"anyscale_container_image_registry", "build_id",
		"/api/v2/application_templates/%s", "result.archived_at",
	)

	err := wrongKeyCheckFn(buildlessRegistryState(templateID))
	if err != nil {
		t.Fatalf("expected the build_id-keyed check to silently report success (that's the bug being demonstrated), got an error instead: %v", err)
	}
	if requestCount != 0 {
		t.Fatalf("expected the build_id-keyed check to never even call the API (empty id -> skipped), got %d requests -- if this now fails, newAPIDestroyCheckImpl's empty-id handling changed and this test should be revisited", requestCount)
	}
}
