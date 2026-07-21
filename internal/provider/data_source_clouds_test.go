package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// runCloudsDataSourceRead drives CloudsDataSource's real Read() method
// end-to-end, same construction pattern as runProjectsDataSourceRead in
// data_source_projects_test.go. Replaces the old TestCloudsFilterParameterBuilding,
// which reimplemented Read()'s param-building logic inline in the test instead
// of calling Read() - it could never catch a bug in the real function because
// it was only ever comparing its own copy against itself, and it hardcoded the
// pre-fix "name_contains" key as the expected/correct value.
func runCloudsDataSourceRead(t *testing.T, d *CloudsDataSource, model CloudsDataSourceModel) (CloudsDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	setDiags := state.Set(ctx, &model)
	if setDiags.HasError() {
		t.Fatalf("failed to build config fixture: %v", setDiags)
	}
	config := tfsdk.Config(state)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State(config),
	}
	d.Read(ctx, datasource.ReadRequest{Config: config}, readResp)

	if readResp.Diagnostics.HasError() {
		return CloudsDataSourceModel{}, readResp.Diagnostics
	}

	var result CloudsDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestCloudsDataSourceRead_NameFilterSendsCorrectQueryKey is the DS-CLOUD-1
// mutation-proof guard for the server-side substring filter. The real backend
// list_clouds handler (product/routers/clouds_router.py) only recognizes
// "name" (TextQuery contains); it silently drops an unrecognized "name_contains"
// key rather than erroring. This currently FAILS - Read() sends name_contains,
// never name - which is the mutation-proof evidence the query key is wrong.
func TestCloudsDataSourceRead_NameFilterSendsCorrectQueryKey(t *testing.T) {
	var gotQuery url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"next_paging_token": null}}`)
	}))
	defer server.Close()

	d := &CloudsDataSource{client: NewClientWithToken(server.URL, "test-token")}

	_, diags := runCloudsDataSourceRead(t, d, CloudsDataSourceModel{
		NameContains: types.StringValue("prod"),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if got := gotQuery.Get("name"); got != "prod" {
		t.Errorf(`query param "name" = %q, want "prod" (the real backend only recognizes "name", not "name_contains")`, got)
	}
	if got := gotQuery.Get("name_contains"); got != "" {
		t.Errorf(`query still sends "name_contains"=%q; the real backend silently ignores this key, so the filter is a no-op`, got)
	}
}

// TestCloudsDataSourceRead_ProviderNarrowsClientSide is the DS-CLOUD-1
// mutation-proof guard for the cloud_provider filter. The real list_clouds
// endpoint has no provider param at all (confirmed by both architect and
// forge's independent traces), so filtering must happen client-side over the
// full paginated result. The mock here simulates that reality - it returns
// every cloud regardless of query params, exactly like the real backend
// silently ignoring a param it does not recognize. This currently FAILS:
// Read() has no client-side filter yet, so all 3 clouds come back instead of
// just the 1 matching GCP cloud.
func TestCloudsDataSourceRead_ProviderNarrowsClientSide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [
				{"id": "cld_1", "name": "aws-one", "provider": "AWS", "region": "us-east-1"},
				{"id": "cld_2", "name": "aws-two", "provider": "AWS", "region": "us-east-2"},
				{"id": "cld_3", "name": "gcp-one", "provider": "GCP", "region": "us-central1"}
			],
			"metadata": {"next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &CloudsDataSource{client: NewClientWithToken(server.URL, "test-token")}

	result, diags := runCloudsDataSourceRead(t, d, CloudsDataSourceModel{
		CloudProvider: types.StringValue("GCP"),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.Clouds) != 1 {
		t.Fatalf("cloud_provider=GCP returned %d cloud(s), want 1 (2 AWS clouds should be excluded): %+v", len(result.Clouds), result.Clouds)
	}
	if got := result.Clouds[0].CloudProvider.ValueString(); got != "GCP" {
		t.Errorf("returned cloud provider = %q, want GCP", got)
	}
}

// TestCloudsDataSourceRead_RegionNarrowsClientSide is the region-filter sibling
// of the provider test above - same no-op-on-the-backend shape, same
// client-side-filter fix. Currently FAILS the same way.
func TestCloudsDataSourceRead_RegionNarrowsClientSide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [
				{"id": "cld_1", "name": "east-one", "provider": "AWS", "region": "us-east-1"},
				{"id": "cld_2", "name": "east-two", "provider": "AWS", "region": "us-east-2"},
				{"id": "cld_3", "name": "west-one", "provider": "AWS", "region": "us-west-2"}
			],
			"metadata": {"next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &CloudsDataSource{client: NewClientWithToken(server.URL, "test-token")}

	result, diags := runCloudsDataSourceRead(t, d, CloudsDataSourceModel{
		Region: types.StringValue("us-west-2"),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.Clouds) != 1 {
		t.Fatalf("region=us-west-2 returned %d cloud(s), want 1 (2 us-east clouds should be excluded): %+v", len(result.Clouds), result.Clouds)
	}
	if got := result.Clouds[0].Region.ValueString(); got != "us-west-2" {
		t.Errorf("returned cloud region = %q, want us-west-2", got)
	}
}

// TestCloudsDataSourceRead_NoFilterReturnsAll guards the other side of the
// provider/region fixes: with no filter set, nothing should be excluded.
func TestCloudsDataSourceRead_NoFilterReturnsAll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [
				{"id": "cld_1", "name": "aws-one", "provider": "AWS", "region": "us-east-1"},
				{"id": "cld_2", "name": "gcp-one", "provider": "GCP", "region": "us-central1"}
			],
			"metadata": {"next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &CloudsDataSource{client: NewClientWithToken(server.URL, "test-token")}

	result, diags := runCloudsDataSourceRead(t, d, CloudsDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.Clouds) != 2 {
		t.Fatalf("no filters returned %d cloud(s), want 2 (unfiltered)", len(result.Clouds))
	}
}

// TestCloudSummaryMapping tests mapping API response to CloudSummaryModel
func TestCloudSummaryMapping(t *testing.T) {
	apiCloud := struct {
		ID                      string
		Name                    string
		Provider                string
		ComputeStack            string
		Region                  string
		Status                  string
		State                   string
		CreatedAt               string
		CreatorID               string
		IsDefault               bool
		IsK8s                   bool
		IsPrivateCloud          bool
		AutoAddUser             bool
		LineageTrackingEnabled  bool
		IsAggregatedLogsEnabled bool
	}{
		ID:                      "cld_abc",
		Name:                    "production",
		Provider:                "AWS",
		ComputeStack:            "VM",
		Region:                  "us-west-2",
		Status:                  "ready",
		State:                   "ACTIVE",
		CreatedAt:               "2024-01-01T00:00:00Z",
		CreatorID:               "user_123",
		IsDefault:               true,
		IsK8s:                   false,
		IsPrivateCloud:          false,
		AutoAddUser:             true,
		LineageTrackingEnabled:  true,
		IsAggregatedLogsEnabled: true,
	}

	model := CloudSummaryModel{
		ID:                      types.StringValue(apiCloud.ID),
		Name:                    types.StringValue(apiCloud.Name),
		CloudProvider:           types.StringValue(apiCloud.Provider),
		ComputeStack:            types.StringValue(apiCloud.ComputeStack),
		Region:                  types.StringValue(apiCloud.Region),
		Status:                  types.StringValue(apiCloud.Status),
		State:                   types.StringValue(apiCloud.State),
		CreatedAt:               types.StringValue(apiCloud.CreatedAt),
		CreatorID:               types.StringValue(apiCloud.CreatorID),
		IsDefault:               types.BoolValue(apiCloud.IsDefault),
		IsK8s:                   types.BoolValue(apiCloud.IsK8s),
		IsPrivateCloud:          types.BoolValue(apiCloud.IsPrivateCloud),
		AutoAddUser:             types.BoolValue(apiCloud.AutoAddUser),
		LineageTrackingEnabled:  types.BoolValue(apiCloud.LineageTrackingEnabled),
		IsAggregatedLogsEnabled: types.BoolValue(apiCloud.IsAggregatedLogsEnabled),
	}

	// Verify all fields
	if model.ID.ValueString() != "cld_abc" {
		t.Errorf("ID = %v, want 'cld_abc'", model.ID.ValueString())
	}
	if model.IsDefault.ValueBool() != true {
		t.Errorf("IsDefault = %v, want true", model.IsDefault.ValueBool())
	}
	if model.IsK8s.ValueBool() != false {
		t.Errorf("IsK8s = %v, want false", model.IsK8s.ValueBool())
	}
	if model.LineageTrackingEnabled.ValueBool() != true {
		t.Errorf("LineageTrackingEnabled = %v, want true", model.LineageTrackingEnabled.ValueBool())
	}
}

// TestCloudsPaginationHandling tests pagination token handling
func TestCloudsPaginationHandling(t *testing.T) {
	tests := []struct {
		name          string
		nextToken     *string
		expectHasMore bool
	}{
		{
			name: "has next page",
			nextToken: func() *string {
				s := "next_token_123"
				return &s
			}(),
			expectHasMore: true,
		},
		{
			name:          "no next page - nil token",
			nextToken:     nil,
			expectHasMore: false,
		},
		{
			name: "no next page - empty token",
			nextToken: func() *string {
				s := ""
				return &s
			}(),
			expectHasMore: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate pagination logic
			hasMore := tt.nextToken != nil && *tt.nextToken != ""

			if hasMore != tt.expectHasMore {
				t.Errorf("hasMore = %v, want %v", hasMore, tt.expectHasMore)
			}
		})
	}
}

// TestComputeStackValues tests valid compute stack values
func TestComputeStackValues(t *testing.T) {
	validStacks := []string{"VM", "K8S"}

	for _, stack := range validStacks {
		t.Run("stack_"+stack, func(t *testing.T) {
			model := CloudSummaryModel{
				ComputeStack: types.StringValue(stack),
			}

			if model.ComputeStack.ValueString() != stack {
				t.Errorf("ComputeStack = %v, want %v", model.ComputeStack.ValueString(), stack)
			}
		})
	}
}

// TestCloudBooleanFlags tests all boolean flags in CloudSummaryModel
func TestCloudBooleanFlags(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value bool
	}{
		{"is_default true", "is_default", true},
		{"is_default false", "is_default", false},
		{"is_k8s true", "is_k8s", true},
		{"is_k8s false", "is_k8s", false},
		{"auto_add_user true", "auto_add_user", true},
		{"auto_add_user false", "auto_add_user", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := CloudSummaryModel{}

			switch tt.field {
			case "is_default":
				model.IsDefault = types.BoolValue(tt.value)
				if model.IsDefault.ValueBool() != tt.value {
					t.Errorf("IsDefault = %v, want %v", model.IsDefault.ValueBool(), tt.value)
				}
			case "is_k8s":
				model.IsK8s = types.BoolValue(tt.value)
				if model.IsK8s.ValueBool() != tt.value {
					t.Errorf("IsK8s = %v, want %v", model.IsK8s.ValueBool(), tt.value)
				}
			case "auto_add_user":
				model.AutoAddUser = types.BoolValue(tt.value)
				if model.AutoAddUser.ValueBool() != tt.value {
					t.Errorf("AutoAddUser = %v, want %v", model.AutoAddUser.ValueBool(), tt.value)
				}
			}
		})
	}
}
