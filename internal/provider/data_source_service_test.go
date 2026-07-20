package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// diagsContainDetailSubstring checks whether any diagnostic's Detail contains the given substring.
// Used where the exact detail text is dynamic (e.g. embeds a count or name), unlike
// diagsContainSummary's exact-match check on the (static) Summary.
func diagsContainDetailSubstring(diags diag.Diagnostics, substr string) bool {
	for _, d := range diags {
		if strings.Contains(d.Detail(), substr) {
			return true
		}
	}
	return false
}

// runServiceDataSourceRead drives ServiceDataSource's real Read() method end-to-end against a
// config model, the same pattern as runProjectDataSourceRead.
func runServiceDataSourceRead(t *testing.T, d *ServiceDataSource, model ServiceDataSourceModel) (ServiceDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	// primary_version and service_observability_urls are types.Object, whose Go zero value
	// carries no attribute-type information (unlike types.String's zero value, which the
	// framework can marshal fine as an initial config fixture). None of this helper's callers
	// pre-populate these themselves - they are Computed-only output Read() fills in - so it's
	// always safe to default them to a properly-typed null here.
	if model.PrimaryVersion.IsNull() || model.PrimaryVersion.IsUnknown() {
		model.PrimaryVersion = types.ObjectNull(serviceVersionAttrTypes)
	}
	if model.ServiceObservabilityURLs.IsNull() || model.ServiceObservabilityURLs.IsUnknown() {
		model.ServiceObservabilityURLs = types.ObjectNull(serviceObservabilityURLsAttrTypes)
	}

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
		return ServiceDataSourceModel{}, readResp.Diagnostics
	}

	var result ServiceDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

func TestServiceDataSourceRead_LookupValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s: validation must short-circuit before any API call", r.Method, r.URL.String())
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{})

	if !diags.HasError() {
		t.Fatal("expected a diagnostic error, got none")
	}
	if !diagsContainSummary(diags, "Missing Required Attribute") {
		t.Errorf("expected 'Missing Required Attribute' diagnostic, got: %v", diags)
	}
}

// fullServiceJSON returns a realistic, fully-populated service JSON body (no nulls) for the
// happy-path field-mapping test. id/name are parameterized since several tests reuse this with
// different identities.
func fullServiceJSON(id, name string) string {
	return `{
		"id": "` + id + `",
		"name": "` + name + `",
		"description": "A test service",
		"project_id": "prj_123",
		"cloud_id": "cld_456",
		"creator_id": "user_789",
		"created_at": "2024-01-01T00:00:00Z",
		"ended_at": "2024-02-01T00:00:00Z",
		"hostname": "` + name + `.example.com",
		"base_url": "https://` + name + `.example.com",
		"current_state": "RUNNING",
		"goal_state": "RUNNING",
		"auto_rollout_enabled": true,
		"is_multi_version": false,
		"error_message": "a transient warning",
		"service_observability_urls": {
			"service_dashboard_url": "https://dash/service",
			"service_dashboard_embedding_url": "https://dash/service/embed",
			"serve_deployment_dashboard_url": "https://dash/deployment",
			"serve_deployment_dashboard_embedding_url": "https://dash/deployment/embed"
		},
		"primary_version": {
			"id": "ver_primary",
			"created_at": "2024-01-01T00:00:00Z",
			"version": "v1",
			"current_state": "RUNNING",
			"weight": 100,
			"current_weight": 100,
			"target_weight": 100,
			"build_id": "bld_1",
			"compute_config_id": "cc_1",
			"production_job_ids": ["job_1", "job_2"],
			"connection_ids": ["conn_1"],
			"ray_serve_config": {"applications": [{"name": "app1"}]}
		},
		"canary_version": {
			"id": "ver_canary",
			"created_at": "2024-01-02T00:00:00Z",
			"version": "v2",
			"current_state": "STARTING",
			"weight": 0,
			"current_weight": 0,
			"target_weight": 20,
			"build_id": "bld_2",
			"compute_config_id": "cc_2",
			"production_job_ids": [],
			"connection_ids": null,
			"ray_serve_config": {"applications": [{"name": "app2"}]}
		},
		"service_status_checklist": {
			"shared": [
				{"kind": "LOAD_BALANCER", "label": "Load Balancer", "state": "RUNNING", "message": "", "version_id": null, "observed_at": "2024-01-01T00:05:00Z"}
			],
			"per_version": [
				{
					"version_id": "ver_primary",
					"items": [
						{"kind": "SERVICE_VERSION", "label": "Cluster", "state": "RUNNING", "message": "healthy", "version_id": "ver_primary", "observed_at": "2024-01-01T00:05:00Z"}
					]
				}
			]
		}
	}`
}

// TestServiceDataSourceRead_ByID exercises the full field mapping, including nested
// primary_version/canary_version, service_observability_urls, and service_status_checklist.
func TestServiceDataSourceRead_ByID(t *testing.T) {
	const serviceID = "service2_abc"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/"+serviceID {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": ` + fullServiceJSON(serviceID, "my-service") + `}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{ID: types.StringValue(serviceID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if result.Name.ValueString() != "my-service" {
		t.Errorf("name = %q, want %q", result.Name.ValueString(), "my-service")
	}
	if result.ProjectID.ValueString() != "prj_123" || result.CloudID.ValueString() != "cld_456" {
		t.Errorf("project_id/cloud_id = %q/%q, want prj_123/cld_456", result.ProjectID.ValueString(), result.CloudID.ValueString())
	}
	if result.BaseURL.ValueString() != "https://my-service.example.com" {
		t.Errorf("base_url = %q", result.BaseURL.ValueString())
	}
	if !result.AutoRolloutEnabled.ValueBool() {
		t.Error("auto_rollout_enabled = false, want true")
	}
	if result.IsMultiVersion.ValueBool() {
		t.Error("is_multi_version = true, want false")
	}

	// service_observability_urls
	var obsURLs ServiceObservabilityURLsModel
	if d := result.ServiceObservabilityURLs.As(context.Background(), &obsURLs, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode service_observability_urls: %v", d)
	}
	if obsURLs.ServiceDashboardURL.ValueString() != "https://dash/service" {
		t.Errorf("service_dashboard_url = %q", obsURLs.ServiceDashboardURL.ValueString())
	}
	if obsURLs.ServeDeploymentDashboardEmbeddingURL.ValueString() != "https://dash/deployment/embed" {
		t.Errorf("serve_deployment_dashboard_embedding_url = %q", obsURLs.ServeDeploymentDashboardEmbeddingURL.ValueString())
	}

	// primary_version
	var primaryVersion ServiceVersionModel
	if d := result.PrimaryVersion.As(context.Background(), &primaryVersion, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode primary_version: %v", d)
	}
	if primaryVersion.ID.ValueString() != "ver_primary" {
		t.Errorf("primary_version.id = %q", primaryVersion.ID.ValueString())
	}
	if primaryVersion.Weight.ValueInt64() != 100 {
		t.Errorf("primary_version.weight = %d, want 100", primaryVersion.Weight.ValueInt64())
	}
	if primaryVersion.CurrentWeight.ValueInt64() != 100 {
		t.Errorf("primary_version.current_weight = %d, want 100", primaryVersion.CurrentWeight.ValueInt64())
	}
	var jobIDs []string
	if d := primaryVersion.ProductionJobIDs.ElementsAs(context.Background(), &jobIDs, false); d.HasError() {
		t.Fatalf("failed to decode production_job_ids: %v", d)
	}
	if len(jobIDs) != 2 || jobIDs[0] != "job_1" || jobIDs[1] != "job_2" {
		t.Errorf("primary_version.production_job_ids = %v, want [job_1 job_2]", jobIDs)
	}
	if primaryVersion.RayServeConfig.ValueString() == "" {
		t.Error("primary_version.ray_serve_config is empty, want the raw JSON blob")
	}

	// canary_version (present in this fixture)
	if result.CanaryVersion == nil {
		t.Fatal("canary_version is nil, want present")
	}
	if result.CanaryVersion.ID.ValueString() != "ver_canary" {
		t.Errorf("canary_version.id = %q", result.CanaryVersion.ID.ValueString())
	}
	if result.CanaryVersion.CurrentState.ValueString() != "STARTING" {
		t.Errorf("canary_version.current_state = %q, want STARTING", result.CanaryVersion.CurrentState.ValueString())
	}
	if !result.CanaryVersion.ConnectionIDs.IsNull() {
		t.Errorf("canary_version.connection_ids should be null when the API sends null, got %#v", result.CanaryVersion.ConnectionIDs)
	}

	// service_status_checklist
	if result.ServiceStatusChecklist == nil {
		t.Fatal("service_status_checklist is nil, want present")
	}
	if len(result.ServiceStatusChecklist.Shared) != 1 {
		t.Fatalf("service_status_checklist.shared count = %d, want 1", len(result.ServiceStatusChecklist.Shared))
	}
	if result.ServiceStatusChecklist.Shared[0].Kind.ValueString() != "LOAD_BALANCER" {
		t.Errorf("service_status_checklist.shared[0].kind = %q, want LOAD_BALANCER", result.ServiceStatusChecklist.Shared[0].Kind.ValueString())
	}
	if len(result.ServiceStatusChecklist.PerVersion) != 1 || len(result.ServiceStatusChecklist.PerVersion[0].Items) != 1 {
		t.Fatalf("service_status_checklist.per_version shape = %+v, want 1 group with 1 item", result.ServiceStatusChecklist.PerVersion)
	}
	if result.ServiceStatusChecklist.PerVersion[0].Items[0].State.ValueString() != "RUNNING" {
		t.Errorf("service_status_checklist.per_version[0].items[0].state = %q, want RUNNING", result.ServiceStatusChecklist.PerVersion[0].Items[0].State.ValueString())
	}
}

// TestServiceDataSourceRead_EnumWireValues is architect's AC-3: assert EXACT wire strings for
// every enum-shaped field round-trip unchanged. This repo has shipped an enum wire-value bug
// hidden by a skipping acctest before (a mismatched constant sent the wrong wire value); this
// unit assertion is the load-bearing guard mentioned in that ruling, and does not depend on any
// acctest/real-infra path to catch a future regression.
func TestServiceDataSourceRead_EnumWireValues(t *testing.T) {
	const serviceID = "service2_enum"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/"+serviceID {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": ` + fullServiceJSON(serviceID, "enum-service") + `}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{ID: types.StringValue(serviceID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	var primaryVersion ServiceVersionModel
	if d := result.PrimaryVersion.As(context.Background(), &primaryVersion, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode primary_version: %v", d)
	}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"current_state", result.CurrentState.ValueString(), "RUNNING"},
		{"goal_state", result.GoalState.ValueString(), "RUNNING"},
		{"primary_version.current_state", primaryVersion.CurrentState.ValueString(), "RUNNING"},
		{"canary_version.current_state", result.CanaryVersion.CurrentState.ValueString(), "STARTING"},
		{"service_status_checklist.shared[0].kind", result.ServiceStatusChecklist.Shared[0].Kind.ValueString(), "LOAD_BALANCER"},
		{"service_status_checklist.shared[0].state", result.ServiceStatusChecklist.Shared[0].State.ValueString(), "RUNNING"},
		{"service_status_checklist.per_version[0].items[0].kind", result.ServiceStatusChecklist.PerVersion[0].Items[0].Kind.ValueString(), "SERVICE_VERSION"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want exact wire value %q", c.name, c.got, c.want)
		}
	}
}

// TestServiceDataSourceRead_NullableFields is architect's AC-2 mutation-proof guard: every
// nullable field must map to Terraform null when the API sends JSON null, never to a zero value
// like "" or 0. Uses a raw JSON body (not a Go struct literal) since a non-pointer field cannot
// represent "absent" - this must actually exercise the null-decoding path, not just default-zero
// a struct.
func TestServiceDataSourceRead_NullableFields(t *testing.T) {
	const serviceID = "service2_nulls"

	body := `{
		"id": "` + serviceID + `",
		"name": "sparse-service",
		"description": null,
		"project_id": "prj_1",
		"cloud_id": "cld_1",
		"creator_id": "user_1",
		"created_at": "2024-01-01T00:00:00Z",
		"ended_at": null,
		"hostname": "sparse.example.com",
		"base_url": "https://sparse.example.com",
		"current_state": "RUNNING",
		"goal_state": "RUNNING",
		"auto_rollout_enabled": false,
		"is_multi_version": false,
		"error_message": null,
		"service_observability_urls": {
			"service_dashboard_url": null,
			"service_dashboard_embedding_url": null,
			"serve_deployment_dashboard_url": null,
			"serve_deployment_dashboard_embedding_url": null
		},
		"primary_version": {
			"id": "ver_1",
			"created_at": "2024-01-01T00:00:00Z",
			"version": "v1",
			"current_state": "RUNNING",
			"weight": 100,
			"current_weight": null,
			"target_weight": null,
			"build_id": "bld_1",
			"compute_config_id": "cc_1",
			"production_job_ids": [],
			"connection_ids": null,
			"ray_serve_config": {}
		},
		"canary_version": null,
		"service_status_checklist": null
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/"+serviceID {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": ` + body + `}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{ID: types.StringValue(serviceID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if !result.Description.IsNull() {
		t.Errorf("description should be null, got %q", result.Description.ValueString())
	}
	if !result.EndedAt.IsNull() {
		t.Errorf("ended_at should be null, got %q", result.EndedAt.ValueString())
	}
	if !result.ErrorMessage.IsNull() {
		t.Errorf("error_message should be null, got %q", result.ErrorMessage.ValueString())
	}
	var obsURLsNullable ServiceObservabilityURLsModel
	if d := result.ServiceObservabilityURLs.As(context.Background(), &obsURLsNullable, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode service_observability_urls: %v", d)
	}
	if !obsURLsNullable.ServiceDashboardURL.IsNull() {
		t.Errorf("service_dashboard_url should be null, got %q", obsURLsNullable.ServiceDashboardURL.ValueString())
	}
	var primaryVersionNullable ServiceVersionModel
	if d := result.PrimaryVersion.As(context.Background(), &primaryVersionNullable, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode primary_version: %v", d)
	}
	if !primaryVersionNullable.CurrentWeight.IsNull() {
		t.Errorf("primary_version.current_weight should be null, got %v", primaryVersionNullable.CurrentWeight)
	}
	if !primaryVersionNullable.TargetWeight.IsNull() {
		t.Errorf("primary_version.target_weight should be null, got %v", primaryVersionNullable.TargetWeight)
	}
	if !primaryVersionNullable.ConnectionIDs.IsNull() {
		t.Errorf("primary_version.connection_ids should be null, got %#v", primaryVersionNullable.ConnectionIDs)
	}
	// ray_serve_config is required upstream (AC-5) - even an empty object must round-trip as a
	// non-null string, never collapse to Terraform null the way the other Optional fields above do.
	if primaryVersionNullable.RayServeConfig.IsNull() {
		t.Error("primary_version.ray_serve_config should never be null (required upstream), got null")
	}
	if result.CanaryVersion != nil {
		t.Errorf("canary_version should be nil when the API sends null, got %+v", result.CanaryVersion)
	}
	if result.ServiceStatusChecklist != nil {
		t.Errorf("service_status_checklist should be nil when the API sends null, got %+v", result.ServiceStatusChecklist)
	}
}

// TestServiceDataSourceRead_ByName exercises the happy path of by-name resolution: exactly one
// exact match, found via the substring pre-filter.
func TestServiceDataSourceRead_ByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2":
			if got := r.URL.Query().Get("name"); got != "frontend" {
				t.Errorf("name query param = %q, want %q", got, "frontend")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [` + fullServiceJSON("svc_1", "frontend") + `], "metadata": {"total": 1}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/svc_1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": ` + fullServiceJSON("svc_1", "frontend") + `}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{Name: types.StringValue("frontend")})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.ID.ValueString() != "svc_1" {
		t.Errorf("id = %q, want svc_1", result.ID.ValueString())
	}
}

// TestServiceDataSourceRead_ByNameNotFound proves the 0-match branch of the resolver.
func TestServiceDataSourceRead_ByNameNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [], "metadata": {"total": 0}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{Name: types.StringValue("ghost")})
	if !diags.HasError() {
		t.Fatal("expected an error, got none")
	}
	if !diagsContainDetailSubstring(diags, "no service found with name 'ghost'") {
		t.Errorf("expected a not-found message, got: %v", diags)
	}
}

// TestServiceDataSourceRead_ByNameAmbiguousErrors is the architect-ruled AC: more than one exact
// name match across different projects must ERROR, not silently PickMostRecentMatch. Service
// names are unique only within a project, so two projects each holding a "frontend" service is a
// normal, expected state - silently picking the newest would let an unrelated team's later
// same-named deploy quietly re-point this data source at a different service on next refresh.
func TestServiceDataSourceRead_ByNameAmbiguousErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2" {
			if _, has := r.URL.Query()["project_id"]; has {
				t.Error("project_id should not be sent when the config does not set it")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [` +
				fullServiceJSON("svc_older", "frontend") + `,` +
				fullServiceJSON("svc_newer", "frontend") +
				`], "metadata": {"total": 2}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{Name: types.StringValue("frontend")})
	if !diags.HasError() {
		t.Fatal("expected an ambiguity error, got none - must not silently pick the most recent match")
	}
	if !diagsContainDetailSubstring(diags, "found 2 services named 'frontend'") ||
		!diagsContainDetailSubstring(diags, "unique only within a project") {
		t.Errorf("expected the ambiguity error message with count and disambiguation guidance, got: %v", diags)
	}
}

// TestServiceDataSourceRead_ByNameWithProjectIDDisambiguates proves that supplying project_id
// narrows an otherwise-ambiguous name to exactly one match, and that project_id/cloud_id are
// actually forwarded as request query parameters (not silently dropped).
func TestServiceDataSourceRead_ByNameWithProjectIDDisambiguates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2":
			if got := r.URL.Query().Get("project_id"); got != "prj_target" {
				t.Errorf("project_id query param = %q, want prj_target", got)
			}
			// Server-side filtering by project_id means only the matching project's service
			// comes back - the client-side exact filter then sees a single match.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [` + fullServiceJSON("svc_target", "frontend") + `], "metadata": {"total": 1}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2/svc_target":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": ` + fullServiceJSON("svc_target", "frontend") + `}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{
		Name:      types.StringValue("frontend"),
		ProjectID: types.StringValue("prj_target"),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.ID.ValueString() != "svc_target" {
		t.Errorf("id = %q, want svc_target", result.ID.ValueString())
	}
}

// TestServiceDataSourceRead_HitsServicesV2Endpoint pins the exact mount path getService/
// findServiceByName call, named and isolated for this one purpose rather than left as an
// incidental side effect of every other mock's strict path match. Found the hard way (see
// CONTRACT_anyscale_service.md's "Endpoint path correction"): the services_internal_router.py
// filename and its route decorators suggest /api/v2/services, but the real mounted path is
// /api/v2/services-v2 - a mock only ever fails against a URL you tell it to expect, so mocked
// unit tests are blind to a wrong-base-path bug by construction unless, like every mock in this
// file, they assert the exact path and reject anything else. Mutation-proof confirmed manually:
// reverting either literal call site back to /api/v2/services fails this test, and 7 of the
// other 8 in this file (all except LookupValidation, which short-circuits before any API call).
func TestServiceDataSourceRead_HitsServicesV2Endpoint(t *testing.T) {
	const serviceID = "service2_endpoint_pin"
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": ` + fullServiceJSON(serviceID, "endpoint-pin") + `}`))
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{ID: types.StringValue(serviceID)})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	want := "/api/v2/services-v2/" + serviceID
	if gotPath != want {
		t.Errorf("request path = %q, want %q (services-v2, not services)", gotPath, want)
	}
}

// TestServiceDataSourceRead_TransitionalNulls is the decisive, race-free proof for the DS
// null-crash (architect's ruling): a data source must not crash on a null it can receive,
// independent of whether that null is common (steady-state) or rare (a transitional read) -
// provenance only affects changelog framing, not whether this must be fixed. Confirmed via a
// real CI acceptance-test crash (TestAccServiceDataSource_ByID, "Path: service_observability_urls",
// "Received null value") that both service_observability_urls and primary_version can be wire
// null - mirrors a service still STARTING, before any version has ever gone healthy or any
// dashboard URLs have been assigned. Deliberately built from a mock, not a real-infra run: real
// infra cannot deterministically hold a service in this exact transient window long enough to
// prove a fix, which is exactly why this crash slipped through undetected since PR 116.
func TestServiceDataSourceRead_TransitionalNulls(t *testing.T) {
	const serviceID = "service2_transitional"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": {
			"id": "` + serviceID + `",
			"name": "still-starting",
			"description": null,
			"project_id": "prj_123",
			"cloud_id": "cld_456",
			"creator_id": "user_789",
			"created_at": "2024-01-01T00:00:00Z",
			"ended_at": null,
			"hostname": "still-starting.example.com",
			"base_url": "https://still-starting.example.com",
			"current_state": "STARTING",
			"goal_state": "RUNNING",
			"auto_rollout_enabled": true,
			"is_multi_version": false,
			"error_message": null,
			"service_observability_urls": null,
			"primary_version": null,
			"canary_version": null,
			"service_status_checklist": null
		}}`))
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{ID: types.StringValue(serviceID)})
	if diags.HasError() {
		t.Fatalf("BUG REPRODUCED: reading a service with service_observability_urls/primary_version null crashed: %v", diags)
	}

	if result.Name.ValueString() != "still-starting" {
		t.Errorf("name = %q, want still-starting", result.Name.ValueString())
	}
	if !result.ServiceObservabilityURLs.IsNull() {
		t.Errorf("service_observability_urls should decode as a null object, got %#v", result.ServiceObservabilityURLs)
	}
	if !result.PrimaryVersion.IsNull() {
		t.Errorf("primary_version should decode as a null object, got %#v", result.PrimaryVersion)
	}
}

// TestServiceDataSourceRead_ObservabilityURLsPresentWithNullSubfields covers the second shape
// shipwright found on the real leaked service that caused the CI crash (service2_s9uqm8dx13usk34zbqdw4pzeix):
// service_observability_urls present as an OBJECT with all 4 dashboard-URL sub-fields null, rather
// than the whole key being null (TestServiceDataSourceRead_TransitionalNulls above). Per
// architect's trace, this shape was never the crash itself - the 4 leaf fields were already
// nullable strings before this fix - so this is a no-regression confirmation, not a
// crashes-today/passes-after mutation-proof case. Included because the real service's response
// evolved from whole-null (the confirmed crash, while still unhealthy/starting) to this
// present-with-null-subfields shape moments later, so both are real states worth covering.
func TestServiceDataSourceRead_ObservabilityURLsPresentWithNullSubfields(t *testing.T) {
	const serviceID = "service2_present_null_subfields"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": {
			"id": "` + serviceID + `",
			"name": "unhealthy-no-dashboards",
			"description": null,
			"project_id": "prj_123",
			"cloud_id": "cld_456",
			"creator_id": "user_789",
			"created_at": "2024-01-01T00:00:00Z",
			"ended_at": null,
			"hostname": "unhealthy-no-dashboards.example.com",
			"base_url": "https://unhealthy-no-dashboards.example.com",
			"current_state": "UNHEALTHY",
			"goal_state": "RUNNING",
			"auto_rollout_enabled": true,
			"is_multi_version": false,
			"error_message": null,
			"service_observability_urls": {
				"service_dashboard_url": null,
				"service_dashboard_embedding_url": null,
				"serve_deployment_dashboard_url": null,
				"serve_deployment_dashboard_embedding_url": null
			},
			"primary_version": null,
			"canary_version": null,
			"service_status_checklist": null
		}}`))
	}))
	defer server.Close()

	d := &ServiceDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServiceDataSourceRead(t, d, ServiceDataSourceModel{ID: types.StringValue(serviceID)})
	if diags.HasError() {
		t.Fatalf("reading a service with an object-present-but-all-subfields-null service_observability_urls crashed: %v", diags)
	}

	if result.ServiceObservabilityURLs.IsNull() {
		t.Error("service_observability_urls should decode as a present (non-null) object, since the key itself was a present object on the wire")
	}
	var obsURLs ServiceObservabilityURLsModel
	if d := result.ServiceObservabilityURLs.As(context.Background(), &obsURLs, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode service_observability_urls: %v", d)
	}
	if !obsURLs.ServiceDashboardURL.IsNull() {
		t.Errorf("service_observability_urls.service_dashboard_url should be null, got %#v", obsURLs.ServiceDashboardURL)
	}
	if !obsURLs.ServeDeploymentDashboardEmbeddingURL.IsNull() {
		t.Errorf("service_observability_urls.serve_deployment_dashboard_embedding_url should be null, got %#v", obsURLs.ServeDeploymentDashboardEmbeddingURL)
	}
}
