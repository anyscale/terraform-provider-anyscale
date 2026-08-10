package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// This file independently verifies the two footguns named in
// resource_cloud_iam_mapping.go's own doc comments - not by trusting the
// comments, but by driving the real Create/Update/Delete methods against a
// mock server and inspecting the actual request bodies they send. Both traps
// were mutation-tested by hand (temporarily reverting the fix each guards,
// confirming the relevant test below fails, then restoring it) before this
// file was written; that manual step is not repeated by CI, so treat these
// tests as the durable replacement for it.
//
// cloudIAMMappingTimeoutsAttrTypes/cloudIAMMappingRulesList/
// nullCloudIAMMappingTimeouts fixtures live in resource_cloud_iam_mapping_test.go
// - reused here rather than duplicated.

func nullCloudIAMMappingTimeouts() timeouts.Value {
	return timeouts.Value{Object: types.ObjectNull(cloudIAMMappingTimeoutsAttrTypes())}
}

// cloudIAMMappingRulesFixture builds a rules types.List from plain
// selector/value pairs for use in plan/config fixtures.
func cloudIAMMappingRulesFixture(t *testing.T, pairs ...[2]string) types.List {
	t.Helper()
	if len(pairs) == 0 {
		return cloudIAMMappingNullRules()
	}
	models := make([]CloudIAMMappingRuleModel, 0, len(pairs))
	for _, p := range pairs {
		models = append(models, CloudIAMMappingRuleModel{
			Selector: types.StringValue(p[0]),
			Value:    types.StringValue(p[1]),
		})
	}
	return cloudIAMMappingRulesList(models...)
}

// newCloudIAMMappingTrapMockServer serves GET/PUT
// /api/v2/clouds/{cloudID}/deployment/{cloudResourceID}/config from a fixed
// "current" spec on GET, and records every PUT body it receives for the
// caller to inspect afterward.
func newCloudIAMMappingTrapMockServer(t *testing.T, cloudID, cloudResourceID string, currentSpecJSON string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var putBodies []map[string]any

	mux := http.NewServeMux()
	path := "/api/v2/clouds/" + cloudID + "/deployment/" + cloudResourceID + "/config"
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": {"spec": ` + currentSpecJSON + `}}`))
		case http.MethodPut:
			// putCloudDeploymentConfig marshals CloudDeploymentConfigResult{Spec}
			// directly as the request body - {"spec": {...}}, with no "result"
			// wrapper. Only GET/PUT RESPONSES are wrapped in "result".
			var envelope map[string]any
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Fatalf("failed to decode PUT body: %v", err)
			}
			spec, ok := envelope["spec"].(map[string]any)
			if !ok {
				t.Fatalf("PUT body missing spec: %v", envelope)
			}
			putBodies = append(putBodies, spec)
			// Echo the spec back as the new "current" spec, wrapped in the
			// result envelope - real enough for these tests, which only
			// inspect the request bodies captured above.
			respBody, err := json.Marshal(map[string]any{"result": map[string]any{"spec": spec}})
			if err != nil {
				t.Fatalf("failed to marshal PUT response: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(respBody)
		default:
			t.Fatalf("unexpected method %s on %s", r.Method, path)
		}
	})

	return httptest.NewServer(mux), &putBodies
}

// TestCloudIAMMappingCreate_PreservesUserTagAnnotationPrefix independently
// verifies the read-modify-write footgun writeCloudIAMMapping's doc comment
// names: cloud_config_service.go assigns UserTagAnnotationPrefix
// unconditionally from every PUT's body, so a write that omits it wipes an
// unrelated cloud setting. Create must read the current value and resend it
// unchanged even though the resource's schema never exposes it.
//
// Mutation-tested: commenting out writeCloudIAMMapping's
// `UserTagAnnotationPrefix: current.UserTagAnnotationPrefix` line makes this
// test fail (the PUT body's field goes empty); restoring it passes again.
func TestCloudIAMMappingCreate_PreservesUserTagAnnotationPrefix(t *testing.T) {
	const cloudID = "cld_trap"
	const cloudResourceID = "cldrsrc_trap"
	const existingPrefix = "acme-prod-"

	server, putBodies := newCloudIAMMappingTrapMockServer(t, cloudID, cloudResourceID,
		`{"cloud_provider": "AWS", "compute_stack": "VM", "user_tag_annotation_prefix": "`+existingPrefix+`", "dataplane_iam_mapping": {}}`,
	)
	defer server.Close()

	r := &CloudIAMMappingResource{client: NewClientWithToken(server.URL, "test-token")}

	plan := CloudIAMMappingResourceModel{
		CloudID:         types.StringValue(cloudID),
		CloudResourceID: types.StringValue(cloudResourceID),
		Rules:           cloudIAMMappingRulesFixture(t, [2]string{"workload-type=job", "role-a"}),
		FallbackRule:    types.StringValue("CLOUD_DEFAULT"),
		Timeouts:        nullCloudIAMMappingTimeouts(),
	}

	tfPlan := buildCloudIAMMappingPlan(t, r, plan)
	createResp := &resource.CreateResponse{State: tfsdk.State(tfPlan)}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfPlan}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", createResp.Diagnostics)
	}

	if len(*putBodies) != 1 {
		t.Fatalf("expected exactly 1 PUT, got %d", len(*putBodies))
	}
	got, ok := (*putBodies)[0]["user_tag_annotation_prefix"].(string)
	if !ok || got != existingPrefix {
		t.Errorf("expected PUT to resend user_tag_annotation_prefix %q, got %v (the RMW read-then-resend was skipped, which would silently wipe this field)", existingPrefix, (*putBodies)[0]["user_tag_annotation_prefix"])
	}
}

// TestCloudIAMMappingDelete_SendsNonEmptySpec independently verifies the
// destroy footgun: cloud_config_service.go short-circuits an EMPTY spec
// (JSON `{}` with no cloud_provider/compute_stack) to a no-op, so Delete
// must send a spec with cloud_provider/compute_stack (and
// user_tag_annotation_prefix) populated, with only the mapping fields
// omitted - never a wholesale empty request.
//
// Mutation-tested: replacing Delete's `desired` construction with a
// zero-valued CloudDeploymentConfigSpec{} makes this test fail (cloud_provider
// and compute_stack come back empty in the captured PUT body); restoring the
// real construction passes again.
func TestCloudIAMMappingDelete_SendsNonEmptySpec(t *testing.T) {
	const cloudID = "cld_trap_delete"
	const cloudResourceID = "cldrsrc_trap_delete"

	server, putBodies := newCloudIAMMappingTrapMockServer(t, cloudID, cloudResourceID,
		`{"cloud_provider": "AWS", "compute_stack": "VM", "user_tag_annotation_prefix": "acme-", `+
			`"dataplane_iam_mapping": {"mode": "CUSTOMER_MANAGED", "rules": [{"selector": "workload-type=job", "value": "role-a"}], "fallback_rule": "CLOUD_DEFAULT"}}`,
	)
	defer server.Close()

	r := &CloudIAMMappingResource{client: NewClientWithToken(server.URL, "test-token")}

	state := CloudIAMMappingResourceModel{
		ID:              types.StringValue(cloudID + "/" + cloudResourceID),
		CloudID:         types.StringValue(cloudID),
		CloudResourceID: types.StringValue(cloudResourceID),
		Rules:           cloudIAMMappingRulesFixture(t, [2]string{"workload-type=job", "role-a"}),
		FallbackRule:    types.StringValue("CLOUD_DEFAULT"),
		Mode:            types.StringValue("CUSTOMER_MANAGED"),
		Timeouts:        nullCloudIAMMappingTimeouts(),
	}

	tfState := buildCloudIAMMappingState(t, r, state)
	deleteResp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfState}, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", deleteResp.Diagnostics)
	}

	if len(*putBodies) != 1 {
		t.Fatalf("expected exactly 1 PUT, got %d", len(*putBodies))
	}
	body := (*putBodies)[0]

	if cp, _ := body["cloud_provider"].(string); cp != "AWS" {
		t.Errorf("expected PUT to carry cloud_provider %q (required-but-discarded transport field), got %v - an empty spec here is a documented server-side no-op, so Delete would silently do nothing", "AWS", body["cloud_provider"])
	}
	if cs, _ := body["compute_stack"].(string); cs != "VM" {
		t.Errorf("expected PUT to carry compute_stack %q, got %v", "VM", body["compute_stack"])
	}
	if prefix, _ := body["user_tag_annotation_prefix"].(string); prefix != "acme-" {
		t.Errorf("expected PUT to preserve user_tag_annotation_prefix %q on destroy too, got %v", "acme-", body["user_tag_annotation_prefix"])
	}

	mapping, _ := body["dataplane_iam_mapping"].(map[string]any)
	if rules, ok := mapping["rules"]; ok && rules != nil {
		if rs, ok := rules.([]any); !ok || len(rs) != 0 {
			t.Errorf("expected dataplane_iam_mapping.rules to be omitted/empty on destroy, got %v", rules)
		}
	}
}

// buildCloudIAMMappingPlan/buildCloudIAMMappingState build a tfsdk.Plan/State
// from the resource's own real schema, the same construction
// resource_project_test.go's runProjectResourceCreate documents: this makes
// a fixture that disagrees with the shipped schema fail here instead of
// passing against a shape the provider never actually serves.
func buildCloudIAMMappingPlan(t *testing.T, r *CloudIAMMappingResource, model CloudIAMMappingResourceModel) tfsdk.Plan {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	diags := tfPlan.Set(ctx, &model)
	if diags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", diags)
	}
	return tfPlan
}

func buildCloudIAMMappingState(t *testing.T, r *CloudIAMMappingResource, model CloudIAMMappingResourceModel) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	tfState := tfsdk.State{Schema: schemaResp.Schema}
	diags := tfState.Set(ctx, &model)
	if diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}
	return tfState
}
