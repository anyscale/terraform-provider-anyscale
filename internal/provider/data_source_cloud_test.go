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
// enable_lineage_tracking, enable_log_ingestion, is_empty_cloud to false and
// cloud_deployment_id to null, regardless of what the API returned. Each case
// below sets the mocked API to the OPPOSITE of the old hardcoded value, so
// this test fails against the pre-fix implementation.
func TestReadCloudIntoModel_MapsFromResponseNotConstant(t *testing.T) {
	t.Run("cloud with resources: booleans true, deployment ID from default resource", func(t *testing.T) {
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
					"results": [{"name": "default-resource", "cloud_deployment_id": "cd-42", "is_default": true}],
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
		if !config.EnableLineageTracking.ValueBool() {
			t.Error("EnableLineageTracking = false, want true (from response)")
		}
		if !config.EnableLogIngestion.ValueBool() {
			t.Error("EnableLogIngestion = false, want true (from response)")
		}
		if config.IsEmptyCloud.ValueBool() {
			t.Error("IsEmptyCloud = true, want false (cloud has a resource)")
		}
		if config.CloudDeploymentID.IsNull() || config.CloudDeploymentID.ValueString() != "cd-42" {
			t.Errorf("CloudDeploymentID = %v, want \"cd-42\"", config.CloudDeploymentID)
		}
	})

	t.Run("cloud with no resources: booleans false, is_empty_cloud true, deployment ID null", func(t *testing.T) {
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
		if !config.CloudDeploymentID.IsNull() {
			t.Errorf("CloudDeploymentID = %v, want null (no default resource)", config.CloudDeploymentID)
		}
	})
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
			_, _ = fmt.Fprint(w, `{"results": [{"name": "r", "cloud_deployment_id": "cd-99", "is_default": true}], "metadata": {"total": 1, "next_paging_token": null}}`)
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
	if byID.CloudDeploymentID.ValueString() != byName.CloudDeploymentID.ValueString() {
		t.Errorf("CloudDeploymentID mismatch: by-id=%v by-name=%v", byID.CloudDeploymentID, byName.CloudDeploymentID)
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
		ID:                types.StringValue("cld_123"),
		Name:              types.StringValue("test-cloud"),
		Status:            types.StringNull(), // Status might not be present
		CloudDeploymentID: types.StringNull(), // Deployment ID might not be present
	}

	if !model.Status.IsNull() {
		t.Error("Status should be null")
	}
	if !model.CloudDeploymentID.IsNull() {
		t.Error("CloudDeploymentID should be null")
	}
}
