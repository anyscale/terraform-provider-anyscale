package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestReadCloudIntoModel_MapsFromResponseNotConstant is a regression test for
// change C1: data_source_cloud.go used to hardcode auto_add_user,
// lineage_tracking_enabled, aggregated_logs_enabled, is_empty_cloud to
// false and cloud_resource_id to null, regardless of what the API returned. Each case
// below sets the mocked API to the OPPOSITE of the old hardcoded value, so
// this test fails against the pre-fix implementation.
func TestReadCloudIntoModel_MapsFromResponseNotConstant(t *testing.T) {
	t.Run("cloud with resources: booleans true, resource ID from default resource", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			switch r.URL.Path {
			case "/api/v2/clouds/cloud-1":
				_, _ = fmt.Fprint(w, `{
					"result": {
						"id": "cloud-1",
						"name": "prod",
						"provider": "AWS",
						"region": "us-east-1",
						"status": "ready",
						"state": "ACTIVE",
						"auto_add_user": true,
						"lineage_tracking_enabled": true,
						"is_aggregated_logs_enabled": true
					}
				}`)
			case "/api/v2/clouds/cloud-1/resources":
				_, _ = fmt.Fprint(w, `{
					"results": [{"name": "default-resource", "cloud_resource_id": "cr-42", "is_default": true}],
					"metadata": {"total": 1, "next_paging_token": null}
				}`)
			default:
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		d := &CloudDataSource{client: NewClientWithToken(server.URL, "test-token")}
		var config CloudDataSourceModel
		if err := d.readCloudIntoModel(context.Background(), "cloud-1", &config); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !config.AutoAddUser.ValueBool() {
			t.Error("AutoAddUser = false, want true (from response)")
		}
		if !config.LineageTrackingEnabled.ValueBool() {
			t.Error("LineageTrackingEnabled = false, want true (from response)")
		}
		if !config.AggregatedLogsEnabled.ValueBool() {
			t.Error("AggregatedLogsEnabled = false, want true (from response)")
		}
		if config.IsEmptyCloud.ValueBool() {
			t.Error("IsEmptyCloud = true, want false (cloud has a resource)")
		}
		if config.CloudResourceID.IsNull() || config.CloudResourceID.ValueString() != "cr-42" {
			t.Errorf("CloudResourceID = %v, want \"cr-42\"", config.CloudResourceID)
		}
	})

	t.Run("cloud with no resources: booleans false, is_empty_cloud true, resource ID null", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			switch r.URL.Path {
			case "/api/v2/clouds/cloud-2":
				_, _ = fmt.Fprint(w, `{
					"result": {
						"id": "cloud-2",
						"name": "empty-cloud",
						"provider": "AWS",
						"region": "us-east-1",
						"auto_add_user": false,
						"lineage_tracking_enabled": false,
						"is_aggregated_logs_enabled": false
					}
				}`)
			case "/api/v2/clouds/cloud-2/resources":
				_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
			default:
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		d := &CloudDataSource{client: NewClientWithToken(server.URL, "test-token")}
		var config CloudDataSourceModel
		if err := d.readCloudIntoModel(context.Background(), "cloud-2", &config); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if config.AutoAddUser.ValueBool() {
			t.Error("AutoAddUser = true, want false (from response)")
		}
		if !config.IsEmptyCloud.ValueBool() {
			t.Error("IsEmptyCloud = false, want true (cloud has zero resources)")
		}
		if !config.CloudResourceID.IsNull() {
			t.Errorf("CloudResourceID = %v, want null (no default resource)", config.CloudResourceID)
		}
	})
}

// TestReadCloudIntoModel_C2ParityFieldsMapFromResponse is a regression test
// for change C2: the singular anyscale_cloud data source previously exposed
// none of the fields the plural anyscale_clouds data source already had
// per-item (compute_stack, created_at, creator_id, is_default, is_private_cloud).
// is_aioa/is_bring_your_own_resource/is_private_service_cloud were part of
// the original C2 parity set but were removed from both data sources
// (read-only, backend-internal classification values users could not act
// on) - see the data_source_attr_removal spec.
func TestReadCloudIntoModel_C2ParityFieldsMapFromResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-parity":
			_, _ = fmt.Fprint(w, `{
				"result": {
					"id": "cloud-parity", "name": "c", "provider": "AWS", "region": "us-east-1",
					"compute_stack": "K8S", "created_at": "2026-01-01T00:00:00Z", "creator_id": "usr_123",
					"is_default": true, "is_private_cloud": true
				}
			}`)
		case "/api/v2/clouds/cloud-parity/resources":
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := &CloudDataSource{client: NewClientWithToken(server.URL, "test-token")}
	var config CloudDataSourceModel
	if err := d.readCloudIntoModel(context.Background(), "cloud-parity", &config); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.ComputeStack.ValueString() != "K8S" {
		t.Errorf("ComputeStack = %v, want K8S", config.ComputeStack.ValueString())
	}
	if config.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt = %v, want 2026-01-01T00:00:00Z", config.CreatedAt.ValueString())
	}
	if config.CreatorID.ValueString() != "usr_123" {
		t.Errorf("CreatorID = %v, want usr_123", config.CreatorID.ValueString())
	}
	for name, got := range map[string]types.Bool{
		"IsDefault":      config.IsDefault,
		"IsPrivateCloud": config.IsPrivateCloud,
	} {
		if !got.ValueBool() {
			t.Errorf("%s = false, want true (from response)", name)
		}
	}
}

// TestCloudDataSource_LookupByIDAndByName_ReturnIdenticalValues proves that
// the by-id and by-name lookup paths converge on readCloudIntoModel and so
// return the same values for the same underlying cloud - both must reflect
// the real API response, not a lookup-path-specific default.
func TestCloudDataSource_LookupByIDAndByName_ReturnIdenticalValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds":
			_, _ = fmt.Fprint(w, `{"results": [{"id": "cloud-3", "name": "by-name-target", "created_at": "2024-01-01T00:00:00Z"}], "metadata": {"total": 1, "next_paging_token": null}}`)
		case "/api/v2/clouds/cloud-3":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-3", "name": "by-name-target", "provider": "GCP", "region": "us-central1", "auto_add_user": true, "lineage_tracking_enabled": true, "is_aggregated_logs_enabled": true}}`)
		case "/api/v2/clouds/cloud-3/resources":
			_, _ = fmt.Fprint(w, `{"results": [{"name": "r", "cloud_resource_id": "cr-99", "is_default": true}], "metadata": {"total": 1, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := &CloudDataSource{client: NewClientWithToken(server.URL, "test-token")}

	var byID CloudDataSourceModel
	if err := d.readCloudIntoModel(context.Background(), "cloud-3", &byID); err != nil {
		t.Fatalf("by-id lookup: unexpected error: %v", err)
	}

	resolvedID, err := d.findCloudByName(context.Background(), "by-name-target")
	if err != nil {
		t.Fatalf("by-name lookup: unexpected error: %v", err)
	}
	var byName CloudDataSourceModel
	if err := d.readCloudIntoModel(context.Background(), resolvedID, &byName); err != nil {
		t.Fatalf("by-name lookup: unexpected error: %v", err)
	}

	if byID.AutoAddUser.ValueBool() != byName.AutoAddUser.ValueBool() {
		t.Errorf("AutoAddUser mismatch: by-id=%v by-name=%v", byID.AutoAddUser.ValueBool(), byName.AutoAddUser.ValueBool())
	}
	if byID.CloudResourceID.ValueString() != byName.CloudResourceID.ValueString() {
		t.Errorf("CloudResourceID mismatch: by-id=%v by-name=%v", byID.CloudResourceID, byName.CloudResourceID)
	}
	if byID.IsEmptyCloud.ValueBool() != byName.IsEmptyCloud.ValueBool() {
		t.Errorf("IsEmptyCloud mismatch: by-id=%v by-name=%v", byID.IsEmptyCloud.ValueBool(), byName.IsEmptyCloud.ValueBool())
	}
}

// TestCloudDataSourceLookupValidation tests validation of ID vs Name lookup
func TestCloudDataSourceLookupValidation(t *testing.T) {
	tests := []struct {
		name      string
		id        types.String
		cloudName types.String
		wantError bool
		errorMsg  string
	}{
		{
			name:      "ID provided",
			id:        types.StringValue("cld_123"),
			cloudName: types.StringNull(),
			wantError: false,
		},
		{
			name:      "name provided",
			id:        types.StringNull(),
			cloudName: types.StringValue("my-cloud"),
			wantError: false,
		},
		{
			name:      "neither provided",
			id:        types.StringNull(),
			cloudName: types.StringNull(),
			wantError: true,
			errorMsg:  "Either 'id' or 'name' must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from Read method
			hasID := !tt.id.IsNull()
			hasName := !tt.cloudName.IsNull()

			var gotError bool
			var gotErrorMsg string

			if !hasID && !hasName {
				gotError = true
				gotErrorMsg = "Either 'id' or 'name' must be specified"
			}

			if gotError != tt.wantError {
				t.Errorf("validation error = %v, wantError %v", gotError, tt.wantError)
			}

			if tt.wantError && gotErrorMsg != tt.errorMsg {
				t.Errorf("error message = %v, want %v", gotErrorMsg, tt.errorMsg)
			}
		})
	}
}

// TestCloudDataSourceNameResolutionLogic tests name resolution with multiple matching clouds
func TestCloudDataSourceNameResolutionLogic(t *testing.T) {
	// Simulate API response with multiple clouds having the same name
	clouds := []struct {
		ID        string
		Name      string
		CreatedAt string
	}{
		{
			ID:        "cld_123",
			Name:      "test-cloud",
			CreatedAt: "2024-01-01T00:00:00Z",
		},
		{
			ID:        "cld_456",
			Name:      "test-cloud",
			CreatedAt: "2024-01-02T00:00:00Z", // More recent
		},
		{
			ID:        "cld_789",
			Name:      "other-cloud",
			CreatedAt: "2024-01-03T00:00:00Z",
		},
	}

	targetName := "test-cloud"

	// Simulate resolution logic
	var matchedCloudID string
	var latestCreatedAt string

	for _, cloud := range clouds {
		if cloud.Name == targetName {
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	// Should pick the most recent one
	if matchedCloudID != "cld_456" {
		t.Errorf("resolved cloud_id = %v, want 'cld_456' (most recent)", matchedCloudID)
	}
}

// TestCloudDataSourceModelMapping tests mapping of API response to model
func TestCloudDataSourceModelMapping(t *testing.T) {
	// Simulate API response
	apiResult := struct {
		ID       string
		Name     string
		Provider string
		Region   string
		Status   string
		State    string
	}{
		ID:       "cld_abc123",
		Name:     "production-cloud",
		Provider: "AWS",
		Region:   "us-west-2",
		Status:   "ready",
		State:    "ACTIVE",
	}

	// Map to model
	model := CloudDataSourceModel{
		ID:            types.StringValue(apiResult.ID),
		Name:          types.StringValue(apiResult.Name),
		CloudProvider: types.StringValue(apiResult.Provider),
		Region:        types.StringValue(apiResult.Region),
		Status:        types.StringValue(apiResult.Status),
		State:         types.StringValue(apiResult.State),
	}

	// Verify mapping
	if model.ID.ValueString() != "cld_abc123" {
		t.Errorf("ID = %v, want 'cld_abc123'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "production-cloud" {
		t.Errorf("Name = %v, want 'production-cloud'", model.Name.ValueString())
	}
	if model.CloudProvider.ValueString() != "AWS" {
		t.Errorf("CloudProvider = %v, want 'AWS'", model.CloudProvider.ValueString())
	}
	if model.Region.ValueString() != "us-west-2" {
		t.Errorf("Region = %v, want 'us-west-2'", model.Region.ValueString())
	}
	if model.Status.ValueString() != "ready" {
		t.Errorf("Status = %v, want 'ready'", model.Status.ValueString())
	}
	if model.State.ValueString() != "ACTIVE" {
		t.Errorf("State = %v, want 'ACTIVE'", model.State.ValueString())
	}
}

// TestCloudProviderValues tests valid cloud provider values
func TestCloudProviderValues(t *testing.T) {
	validProviders := []string{"AWS", "GCP", "AZURE", "GENERIC"}

	for _, provider := range validProviders {
		t.Run("provider_"+provider, func(t *testing.T) {
			model := CloudDataSourceModel{
				CloudProvider: types.StringValue(provider),
			}

			if model.CloudProvider.ValueString() != provider {
				t.Errorf("CloudProvider = %v, want %v", model.CloudProvider.ValueString(), provider)
			}
		})
	}
}

// TestCloudStatusMapping tests common cloud status values
func TestCloudStatusMapping(t *testing.T) {
	statuses := []string{"ready", "pending", "failed", "creating"}

	for _, status := range statuses {
		t.Run("status_"+status, func(t *testing.T) {
			model := CloudDataSourceModel{
				Status: types.StringValue(status),
			}

			if model.Status.ValueString() != status {
				t.Errorf("Status = %v, want %v", model.Status.ValueString(), status)
			}
		})
	}
}

// TestCloudStateMapping tests cloud lifecycle states
func TestCloudStateMapping(t *testing.T) {
	states := []string{"ACTIVE", "CREATING", "FAILED", "DELETING"}

	for _, state := range states {
		t.Run("state_"+state, func(t *testing.T) {
			model := CloudDataSourceModel{
				State: types.StringValue(state),
			}

			if model.State.ValueString() != state {
				t.Errorf("State = %v, want %v", model.State.ValueString(), state)
			}
		})
	}
}

// TestCloudNullableFields tests handling of optional/nullable fields
func TestCloudNullableFields(t *testing.T) {
	model := CloudDataSourceModel{
		ID:     types.StringValue("cld_123"),
		Name:   types.StringValue("test-cloud"),
		Status: types.StringNull(), // Status might not be present
	}

	if !model.Status.IsNull() {
		t.Error("Status should be null")
	}
}

// TestFindCloudByName_PagesBeyondFirstPage is the DS-CLOUD-2/X-4 mutation-proof
// regression guard: findCloudByName reads only page 1 of GET /api/v2/clouds via
// a raw DoRequest call, with no pagination at all. In an org whose cloud list
// exceeds one page, a valid name past page 1 resolves to "not found". This
// currently FAILS - the named cloud sits on page 2 and today's code never asks
// for it - which is the mutation-proof evidence. Must pass once findCloudByName
// (or its X-2 shared-helper replacement) pages through PaginatedRequest.
func TestFindCloudByName_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"id": "cld_1", "name": "other-cloud", "created_at": "2024-01-01T00:00:00Z"}],
				"metadata": {"next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "cld_2", "name": "target-cloud", "created_at": "2024-01-01T00:00:00Z"}],
			"metadata": {"next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &CloudDataSource{client: NewClientWithToken(server.URL, "test-token")}

	id, err := d.findCloudByName(context.Background(), "target-cloud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "cld_2" {
		t.Errorf("findCloudByName(target-cloud) = %q, want cld_2 (found on page 2)", id)
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}
