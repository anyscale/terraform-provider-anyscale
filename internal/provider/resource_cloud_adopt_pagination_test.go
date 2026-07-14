package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// runCloudResourceCreate drives CloudResource's real Create() method end-to-end
// against a plan model, the same pattern as runProjectResourceCreate in
// resource_project_test.go.
func runCloudResourceCreate(t *testing.T, r *CloudResource, plan CloudResourceModel) (CloudResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	planDiags := tfPlan.Set(ctx, &plan)
	if planDiags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", planDiags)
	}

	createResp := &resource.CreateResponse{
		State: tfsdk.State(tfPlan),
	}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan}, createResp)

	if createResp.Diagnostics.HasError() {
		return CloudResourceModel{}, createResp.Diagnostics
	}

	var result CloudResourceModel
	getDiags := createResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, createResp.Diagnostics
}

// TestFindCloudByName_ExactMatchOnPageTwo is the regression proof for the
// page-1-only pagination bug: the old findCloudByName made a single
// unpaginated GET /api/v2/clouds call, so a name collision that only existed
// on page 2+ was invisible - Create() would report "not found" and create a
// genuine duplicate cloud. This engineers page 1 to contain only a decoy and
// puts the real exact match on page 2.
func TestFindCloudByName_ExactMatchOnPageTwo(t *testing.T) {
	const searchName = "tfacc-adopt-cloud"
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			resp := CloudsListResponse{Results: []CloudResult{
				{ID: "cld_decoy", Name: "tfacc-adopt-cloud-old", CreatedAt: "2024-01-01T00:00:00Z"},
			}}
			next := "page-2-token"
			resp.Metadata.NextPagingToken = &next
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Page 2: the real exact match. NextPagingToken stays nil -> loop terminates.
		resp := CloudsListResponse{Results: []CloudResult{
			{ID: "cld_exact", Name: searchName, CreatedAt: "2024-01-02T00:00:00Z"},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}

	id, err := r.findCloudByName(context.Background(), searchName)
	if err != nil {
		t.Fatalf("findCloudByName returned error: %v - exact match on page 2 was not found", err)
	}
	if id != "cld_exact" {
		t.Errorf("got id %q, want %q", id, "cld_exact")
	}
	if requestCount != 2 {
		t.Fatalf("expected exactly 2 requests (one per page), got %d", requestCount)
	}
}

// TestFindCloudByName_MultipleExactMatchesReturnsError is the regression proof
// for the ambiguity bug: the old findCloudByName silently returned the first
// matching cloud with no warning. Because this is an ADOPT path (the returned
// ID gets Terraform-managed as if it were the thing just created), silently
// picking among duplicates risks managing the wrong live cloud - so this must
// hard error, not silently pick like the read-side PickMostRecentMatch would.
func TestFindCloudByName_MultipleExactMatchesReturnsError(t *testing.T) {
	const searchName = "tfacc-dup-cloud"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := CloudsListResponse{Results: []CloudResult{
			{ID: "cld_a", Name: searchName, CreatedAt: "2024-01-01T00:00:00Z"},
			{ID: "cld_b", Name: searchName, CreatedAt: "2024-06-01T00:00:00Z"},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}

	id, err := r.findCloudByName(context.Background(), searchName)
	if err == nil {
		t.Fatalf("expected an ambiguity error, got id %q with nil error", id)
	}
	if id != "" {
		t.Errorf("expected empty id alongside the error, got %q", id)
	}
	var ambigErr *multipleCloudsWithNameError
	if !errors.As(err, &ambigErr) {
		t.Fatalf("expected *multipleCloudsWithNameError, got %T: %v", err, err)
	}
	if len(ambigErr.ids) != 2 {
		t.Errorf("expected 2 ids captured, got %d: %v", len(ambigErr.ids), ambigErr.ids)
	}
}

// TestFindCloudByName_MultipleExactMatchesAcrossPagesReturnsError proves
// matches accumulate across the full paginated result set before the
// ambiguity decision is made, not just within a single page's results slice.
func TestFindCloudByName_MultipleExactMatchesAcrossPagesReturnsError(t *testing.T) {
	const searchName = "tfacc-dup-cloud-paged"
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			resp := CloudsListResponse{Results: []CloudResult{
				{ID: "cld_page1", Name: searchName, CreatedAt: "2024-01-01T00:00:00Z"},
			}}
			next := "page-2-token"
			resp.Metadata.NextPagingToken = &next
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		resp := CloudsListResponse{Results: []CloudResult{
			{ID: "cld_page2", Name: searchName, CreatedAt: "2024-02-01T00:00:00Z"},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}

	_, err := r.findCloudByName(context.Background(), searchName)
	var ambigErr *multipleCloudsWithNameError
	if !errors.As(err, &ambigErr) {
		t.Fatalf("expected ambiguity error spanning both pages, got %T: %v", err, err)
	}
	if requestCount != 2 {
		t.Fatalf("expected both pages to be walked before the ambiguity decision, got %d requests", requestCount)
	}
}

// TestFindCloudByName_NoMatchReturnsEmptyNilError confirms the 0-match
// baseline is unchanged: Create()'s adopt check must keep falling through to
// a fresh create when no cloud has this name.
func TestFindCloudByName_NoMatchReturnsEmptyNilError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	id, err := r.findCloudByName(context.Background(), "no-such-cloud")
	if err != nil {
		t.Fatalf("expected nil error on no match, got %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id on no match, got %q", id)
	}
}

// TestFindCloudByName_ExactlyOneMatchReturnsID confirms the 1-match baseline
// is unchanged: Create()'s adopt path must keep attaching to the sole match.
func TestFindCloudByName_ExactlyOneMatchReturnsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := CloudsListResponse{Results: []CloudResult{
			{ID: "cld_only", Name: "tfacc-single-cloud", CreatedAt: "2024-01-01T00:00:00Z"},
			{ID: "cld_other", Name: "some-other-cloud", CreatedAt: "2024-01-01T00:00:00Z"},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	id, err := r.findCloudByName(context.Background(), "tfacc-single-cloud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "cld_only" {
		t.Errorf("got %q, want %q", id, "cld_only")
	}
}

// TestCreate_MultipleExistingCloudsProducesResourceLevelDiagnostic proves the
// ambiguity error actually reaches Create()'s diagnostics, not just that
// findCloudByName computes it correctly in isolation. A wiring mistake (e.g.
// checking errors.Is against the wrong value, or a typo in the pointer type)
// would not be caught by the findCloudByName-only tests above - it would only
// surface here, at the call site. Also proves Create() hard-stops before
// attempting to POST a second, now-duplicate cloud.
func TestCreate_MultipleExistingCloudsProducesResourceLevelDiagnostic(t *testing.T) {
	const name = "tfacc-dup-adopt-cloud"

	postCreateCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds":
			w.Header().Set("Content-Type", "application/json")
			resp := CloudsListResponse{Results: []CloudResult{
				{ID: "cld_a", Name: name, CreatedAt: "2024-01-01T00:00:00Z"},
				{ID: "cld_b", Name: name, CreatedAt: "2024-06-01T00:00:00Z"},
			}}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/clouds":
			// Must NOT be reached: an ambiguity error must hard-stop Create
			// before it attempts to create a second, now-duplicate cloud.
			postCreateCalled = true
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": {"id": "cld_new", "name": %q}}`, name)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	plan := CloudResourceModel{
		Name:      types.StringValue(name),
		AWSConfig: types.ObjectNull(awsConfigAttrTypes()),
		GCPConfig: types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig: types.ObjectNull(map[string]attr.Type{
			"subscription_id":     types.StringType,
			"resource_group_name": types.StringType,
			"vnet_name":           types.StringType,
			"subnet_name":         types.StringType,
			"managed_identity_id": types.StringType,
		}),
		KubernetesConfig: types.ObjectNull(kubernetesConfigAttrTypes()),
		ObjectStorage:    types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:      types.ObjectNull(fileStorageAttrTypes()),
	}

	_, diags := runCloudResourceCreate(t, r, plan)

	if !diags.HasError() {
		t.Fatal("expected a resource-level diagnostic on ambiguous adopt, got none")
	}
	if !diagsContainSummary(diags, "Multiple Clouds Found") {
		t.Errorf("expected a 'Multiple Clouds Found' diagnostic, got: %v", diags)
	}
	if postCreateCalled {
		t.Error("Create must not attempt to POST a new cloud when the adopt lookup is ambiguous")
	}
}
