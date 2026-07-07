package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// C11a: Update() previously sent a single PATCH /clouds/{id}, which 405s
// against the real API - there is no such route. The real API exposes three
// single-field PUT routes instead. C11b: name has no update route at all,
// so it's enforced immutable via a plan-time error instead of being sent.

func TestUpdateCloudBoolField_HitsCorrectPathAndQueryParam(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	if err := r.updateCloudBoolField(context.Background(), "cloud-1", "auto_add_user", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/api/v2/clouds/cloud-1/auto_add_user?auto_add_user=true"
	if gotPath != want {
		t.Errorf("request path+query = %q, want %q", gotPath, want)
	}
}

func TestUpdateCloudAggregatedLogsConfig_UsesIsEnabledParamName(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	if err := r.updateCloudAggregatedLogsConfig(context.Background(), "cloud-1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Regression: the real backend's query parameter is named is_enabled,
	// NOT is_aggregated_logs_enabled - using the schema's own field name here
	// would silently no-op against the real API (FastAPI would just ignore
	// an unrecognized query param rather than erroring).
	want := "/api/v2/clouds/cloud-1/update_customer_aggregated_logs_config?is_enabled=true"
	if gotPath != want {
		t.Errorf("request path+query = %q, want %q", gotPath, want)
	}
}

func TestUpdateMutableFields_OnlyCallsRoutesForChangedFields(t *testing.T) {
	var hitPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPaths = append(hitPaths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}

	// Only auto_add_user changes (false -> true); the other two are identical
	// between plan and state and must NOT trigger a PUT call.
	state := CloudResourceModel{
		AutoAddUser: types.BoolValue(false), EnableLineageTracking: types.BoolValue(false), EnableLogIngestion: types.BoolValue(false),
	}
	plan := state
	plan.AutoAddUser = types.BoolValue(true)

	if err := r.updateMutableFields(context.Background(), "cloud-1", plan, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hitPaths) != 1 || hitPaths[0] != "/api/v2/clouds/cloud-1/auto_add_user" {
		t.Errorf("hit paths = %v, want exactly [/api/v2/clouds/cloud-1/auto_add_user]", hitPaths)
	}
}

func TestUpdateMutableFields_NoChangesCallsNothing(t *testing.T) {
	var hitPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPaths = append(hitPaths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	same := CloudResourceModel{
		AutoAddUser: types.BoolValue(true), EnableLineageTracking: types.BoolValue(false), EnableLogIngestion: types.BoolValue(true),
	}

	if err := r.updateMutableFields(context.Background(), "cloud-1", same, same); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hitPaths) != 0 {
		t.Errorf("hit paths = %v, want none - nothing changed", hitPaths)
	}
}

func TestUpdateMutableFields_AllThreeChanged(t *testing.T) {
	var hitPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPaths = append(hitPaths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	state := CloudResourceModel{
		AutoAddUser: types.BoolValue(false), EnableLineageTracking: types.BoolValue(false), EnableLogIngestion: types.BoolValue(false),
	}
	plan := CloudResourceModel{
		AutoAddUser: types.BoolValue(true), EnableLineageTracking: types.BoolValue(true), EnableLogIngestion: types.BoolValue(true),
	}

	if err := r.updateMutableFields(context.Background(), "cloud-1", plan, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPaths := map[string]bool{
		"/api/v2/clouds/cloud-1/auto_add_user":                          false,
		"/api/v2/clouds/cloud-1/lineage_tracking_enabled":               false,
		"/api/v2/clouds/cloud-1/update_customer_aggregated_logs_config": false,
	}
	if len(hitPaths) != 3 {
		t.Fatalf("hit paths = %v, want exactly 3", hitPaths)
	}
	for _, p := range hitPaths {
		if _, ok := wantPaths[p]; !ok {
			t.Errorf("unexpected path hit: %s", p)
		}
		wantPaths[p] = true
	}
	for p, hit := range wantPaths {
		if !hit {
			t.Errorf("expected path never hit: %s", p)
		}
	}
}

func TestCloudNameImmutablePlanModifier(t *testing.T) {
	m := cloudNameImmutablePlanModifier{}

	tests := []struct {
		name      string
		state     types.String
		plan      types.String
		wantError bool
	}{
		{"fresh create: state null, no error regardless of plan", types.StringNull(), types.StringValue("anything"), false},
		{"post-import before first read: state unknown, no error", types.StringUnknown(), types.StringValue("anything"), false},
		{"unchanged name: no error", types.StringValue("prod"), types.StringValue("prod"), false},
		{"changed name: plan-time error, not silently allowed", types.StringValue("prod"), types.StringValue("prod-renamed"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := planmodifier.StringRequest{
				Path:       path.Root("name"),
				StateValue: tt.state,
				PlanValue:  tt.plan,
			}
			resp := &planmodifier.StringResponse{PlanValue: tt.plan}

			m.PlanModifyString(context.Background(), req, resp)

			if resp.Diagnostics.HasError() != tt.wantError {
				t.Errorf("HasError() = %v, want %v (diags: %v)", resp.Diagnostics.HasError(), tt.wantError, resp.Diagnostics)
			}
		})
	}
}
