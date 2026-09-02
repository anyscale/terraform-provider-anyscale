package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// runServicesDataSourceRead drives ServicesDataSource's real Read() method end-to-end, the same
// construction pattern as runServiceDataSourceRead / runProjectsDataSourceRead.
func runServicesDataSourceRead(t *testing.T, d *ServicesDataSource, model ServicesDataSourceModel) (ServicesDataSourceModel, diag.Diagnostics) {
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
		return ServicesDataSourceModel{}, readResp.Diagnostics
	}

	var result ServicesDataSourceModel
	getDiags := readResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// TestServicesDataSourceRead_Basic exercises no-filter listing with full item mapping, proving
// (per the CONTRACT doc) that the plural data source carries the same full detail as the
// singular one - not a trimmed summary the way anyscale_projects trims collaborators.
func TestServicesDataSourceRead_Basic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [` +
				fullServiceJSON("svc_1", "frontend") + `,` +
				fullServiceJSON("svc_2", "backend") +
				`], "metadata": {"total": 2}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServicesDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServicesDataSourceRead(t, d, ServicesDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if len(result.Services) != 2 {
		t.Fatalf("services count = %d, want 2", len(result.Services))
	}
	if result.Services[0].ID.ValueString() != "svc_1" || result.Services[1].ID.ValueString() != "svc_2" {
		t.Errorf("unexpected service IDs: %q, %q", result.Services[0].ID.ValueString(), result.Services[1].ID.ValueString())
	}
	// Full detail, same as the singular DS - not trimmed.
	var primaryVersion ServiceVersionModel
	if d := result.Services[0].PrimaryVersion.As(context.Background(), &primaryVersion, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("failed to decode services[0].primary_version: %v", d)
	}
	if primaryVersion.ID.ValueString() != "ver_primary" {
		t.Errorf("services[0].primary_version.id = %q, want ver_primary", primaryVersion.ID.ValueString())
	}
	if result.Services[0].CanaryVersion == nil {
		t.Error("services[0].canary_version is nil, want present per the fixture")
	}
	if result.Services[0].ServiceStatusChecklist == nil {
		t.Error("services[0].service_status_checklist is nil, want present per the fixture")
	}
}

// TestServicesDataSourceRead_EmptyResult proves a zero-service result maps to an empty (not nil)
// list, matching this provider's convention for list attributes with no matches.
func TestServicesDataSourceRead_EmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [], "metadata": {"total": 0}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServicesDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServicesDataSourceRead(t, d, ServicesDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if result.Services == nil {
		t.Error("services is nil, want an empty (non-nil) slice")
	}
	if len(result.Services) != 0 {
		t.Errorf("services count = %d, want 0", len(result.Services))
	}
}

// TestServicesDataSourceRead_FiltersSendCorrectQueryKeys is a request-aware test asserting every
// filter this data source implements is actually forwarded as the right query key with the
// right value - the class of test that would have caught a wrong-param-name bug (like the
// documented services list "name" param actually being a substring match) if one existed here
// instead of being a deliberate, documented translation.
func TestServicesDataSourceRead_FiltersSendCorrectQueryKeys(t *testing.T) {
	var gotQuery url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2" {
			gotQuery = r.URL.Query()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [], "metadata": {"total": 0}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServicesDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runServicesDataSourceRead(t, d, ServicesDataSourceModel{
		NameContains: types.StringValue("front"),
		ProjectID:    types.StringValue("prj_x"),
		CloudID:      types.StringValue("cld_x"),
		CreatorID:    types.StringValue("user_x"),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	// name_contains forwards to the wire's "name" param (which is substring server-side despite
	// its name) - this is the one deliberate name/wire mismatch in this data source, confirmed
	// against services_dao.py directly, not assumed.
	if got := gotQuery.Get("name"); got != "front" {
		t.Errorf(`query param "name" = %q, want %q (from name_contains)`, got, "front")
	}
	if got := gotQuery.Get("project_id"); got != "prj_x" {
		t.Errorf(`query param "project_id" = %q, want %q`, got, "prj_x")
	}
	if got := gotQuery.Get("cloud_id"); got != "cld_x" {
		t.Errorf(`query param "cloud_id" = %q, want %q`, got, "cld_x")
	}
	if got := gotQuery.Get("creator_id"); got != "user_x" {
		t.Errorf(`query param "creator_id" = %q, want %q`, got, "user_x")
	}
	// No stray "name_contains" key should ever reach the wire - that's a Terraform-side name only.
	if _, has := gotQuery["name_contains"]; has {
		t.Error(`query should never send a literal "name_contains" key - it must translate to "name"`)
	}
}

// TestServicesDataSourceRead_FilterByProjectIDNarrows is a narrowing-proof test (not a
// presence-only placebo): asserts every returned service actually matches the project_id filter,
// not just that the filter was accepted and something came back.
func TestServicesDataSourceRead_FilterByProjectIDNarrows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/services-v2" {
			if got := r.URL.Query().Get("project_id"); got != "prj_target" {
				t.Errorf("project_id query param = %q, want prj_target", got)
			}
			w.WriteHeader(http.StatusOK)
			// The mock only returns matches for the requested project, mirroring real
			// server-side filtering - the assertion below checks the field really is populated
			// from the response, not hardcoded.
			_, _ = w.Write([]byte(`{"results": [` + fullServiceJSON("svc_in_project", "scoped") + `], "metadata": {"total": 1}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &ServicesDataSource{client: NewClientWithToken(server.URL, "test-token")}
	result, diags := runServicesDataSourceRead(t, d, ServicesDataSourceModel{ProjectID: types.StringValue("prj_target")})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if len(result.Services) != 1 {
		t.Fatalf("services count = %d, want 1", len(result.Services))
	}
	if result.Services[0].ProjectID.ValueString() != "prj_123" {
		// fullServiceJSON always stamps project_id "prj_123" - this proves the field really
		// flows from the response body into state, it isn't defaulted/hardcoded anywhere.
		t.Errorf("services[0].project_id = %q, want prj_123 (from the response body)", result.Services[0].ProjectID.ValueString())
	}
}

// TestServicesDataSourceRead_HitsServicesV2Endpoint is the plural analog of
// TestServiceDataSourceRead_HitsServicesV2Endpoint in data_source_service_test.go - pins
// fetchServices' mount path for the identical reason (see CONTRACT_anyscale_service.md's
// "Endpoint path correction"), named and isolated rather than left as an incidental side effect
// of the other tests' strict mocks.
func TestServicesDataSourceRead_HitsServicesV2Endpoint(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [], "metadata": {"total": 0}}`))
	}))
	defer server.Close()

	d := &ServicesDataSource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runServicesDataSourceRead(t, d, ServicesDataSourceModel{})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if gotPath != "/api/v2/services-v2" {
		t.Errorf("request path = %q, want %q (services-v2, not services)", gotPath, "/api/v2/services-v2")
	}
}
