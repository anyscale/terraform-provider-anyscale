package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// unmarshalServiceResult is a small helper for these tests: fullServiceJSON/the sparse fixture
// below are both the raw `result` body shape (no {"result": ...} wrapper), matching how
// ServiceResponse.Result is typed.
func unmarshalServiceResult(t *testing.T, body string) *ServiceResult {
	t.Helper()
	var s ServiceResult
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("failed to unmarshal fixture: %v", err)
	}
	return &s
}

// asServiceVersionModel decodes one of the resource model's types.Object version fields
// (primary_version/canary_version) back into the shared ServiceVersionModel struct so tests can
// assert on its fields directly - the resource stores these as types.Object (contract §P0: a
// plain struct/pointer cannot hold Unknown, which a plain-Computed nested object legitimately is
// pre-apply), converted via the same serviceVersionAttrTypes map populateServiceResourceModelComputed uses.
func asServiceVersionModel(t *testing.T, obj types.Object) ServiceVersionModel {
	t.Helper()
	var v ServiceVersionModel
	if diags := obj.As(context.Background(), &v, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode version object: %v", diags)
	}
	return v
}

// TestPopulateServiceResourceModelComputed_EnumWireValues is the resource-side AC-R2 guard:
// current_state/goal_state and the nested version/checklist enum strings must land in the
// resource model EXACTLY as the backend sent them - this exercises populateServiceResourceModelComputed
// itself (new, resource-specific code), not just the shared sub-helpers data_source_service_test.go
// already covers (Addendum A: those are reused verbatim, so THEIR correctness isn't re-litigated
// here - only this function's own wiring of them into ServiceResourceModel, including the P0
// types.Object conversion, is what's new to prove).
func TestPopulateServiceResourceModelComputed_EnumWireValues(t *testing.T) {
	service := unmarshalServiceResult(t, fullServiceJSON("svc_enum", "enum-resource"))

	var model ServiceResourceModel
	diags := populateServiceResourceModelComputed(context.Background(), &model, service)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	primary := asServiceVersionModel(t, model.PrimaryVersion)
	canary := asServiceVersionModel(t, model.CanaryVersion)

	var checklist ServiceStatusChecklistModel
	if diags := model.ServiceStatusChecklist.As(context.Background(), &checklist, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("failed to decode service_status_checklist: %v", diags)
	}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"current_state", model.CurrentState.ValueString(), "RUNNING"},
		{"goal_state", model.GoalState.ValueString(), "RUNNING"},
		{"primary_version.current_state", primary.CurrentState.ValueString(), "RUNNING"},
		{"canary_version.current_state", canary.CurrentState.ValueString(), "STARTING"},
		{"service_status_checklist.shared[0].kind", checklist.Shared[0].Kind.ValueString(), "LOAD_BALANCER"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want exact wire value %q", c.name, c.got, c.want)
		}
	}
}

// TestPopulateServiceResourceModelComputed_NullableFields is the resource-side AC-R1 guard:
// every nullable computed field must map to Terraform null when the API sends JSON null, never
// to a zero value ("" for strings, a real-but-empty object for canary_version/
// service_status_checklist). Uses a raw JSON body (not a Go struct literal) since a non-pointer Go
// field cannot represent "absent" - this must actually exercise the null-decoding path.
func TestPopulateServiceResourceModelComputed_NullableFields(t *testing.T) {
	body := `{
		"id": "svc_nulls", "name": "sparse-resource", "project_id": "prj_1", "cloud_id": "cld_1",
		"creator_id": "user_1", "created_at": "2024-01-01T00:00:00Z",
		"ended_at": null, "hostname": "sparse.example.com", "base_url": "https://sparse.example.com",
		"current_state": "RUNNING", "goal_state": "RUNNING",
		"auto_rollout_enabled": false, "is_multi_version": false,
		"error_message": null,
		"service_observability_urls": {
			"service_dashboard_url": null, "service_dashboard_embedding_url": null,
			"serve_deployment_dashboard_url": null, "serve_deployment_dashboard_embedding_url": null
		},
		"primary_version": {
			"id": "ver_1", "created_at": "2024-01-01T00:00:00Z", "version": "v1",
			"current_state": "RUNNING", "weight": 100, "current_weight": null, "target_weight": null,
			"build_id": "bld_1", "compute_config_id": "cc_1", "production_job_ids": [],
			"connection_ids": null, "ray_serve_config": {}
		},
		"canary_version": null,
		"service_status_checklist": null
	}`
	service := unmarshalServiceResult(t, body)

	var model ServiceResourceModel
	diags := populateServiceResourceModelComputed(context.Background(), &model, service)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if !model.EndedAt.IsNull() {
		t.Errorf("EndedAt = %v, want null", model.EndedAt)
	}
	if !model.ErrorMessage.IsNull() {
		t.Errorf("ErrorMessage = %v, want null", model.ErrorMessage)
	}
	if !model.CanaryVersion.IsNull() {
		t.Errorf("CanaryVersion = %v, want a null object (a JSON null canary_version must not become a populated, zero-valued object)", model.CanaryVersion)
	}
	if !model.ServiceStatusChecklist.IsNull() {
		t.Errorf("ServiceStatusChecklist = %v, want a null object", model.ServiceStatusChecklist)
	}

	primary := asServiceVersionModel(t, model.PrimaryVersion)
	if !primary.CurrentWeight.IsNull() {
		t.Errorf("PrimaryVersion.CurrentWeight = %v, want null", primary.CurrentWeight)
	}
	if !primary.ConnectionIDs.IsNull() {
		t.Errorf("PrimaryVersion.ConnectionIDs = %v, want null", primary.ConnectionIDs)
	}
}

// TestPopulateServiceResourceModelComputed_CanaryVersionPresent is the mirror case of the
// nullable-fields test above: when the backend DOES report a canary_version (a rollout in
// progress), it must be populated as a real, non-null object with its own fields intact - not
// just "the null branch exists" but "the non-null branch actually works."
func TestPopulateServiceResourceModelComputed_CanaryVersionPresent(t *testing.T) {
	service := unmarshalServiceResult(t, fullServiceJSON("svc_canary", "canary-resource"))

	var model ServiceResourceModel
	diags := populateServiceResourceModelComputed(context.Background(), &model, service)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if model.CanaryVersion.IsNull() {
		t.Fatal("CanaryVersion is null, want a populated version (fullServiceJSON includes a real canary_version)")
	}
	canary := asServiceVersionModel(t, model.CanaryVersion)
	if got, want := canary.ID.ValueString(), "ver_canary"; got != want {
		t.Errorf("CanaryVersion.ID = %q, want %q", got, want)
	}
}
