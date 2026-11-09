package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// runProjectModifyPlan drives ProjectResource.ModifyPlan directly, the same
// harness shape as runSchedulerConfigModifyPlan
// (resource_scheduler_config_modify_plan_test.go). The guard reads State and
// Plan only, so no client or server is needed.
//
// Both values must be non-nil: a null State.Raw is the create signal and a
// null Plan.Raw the destroy signal, and the guard deliberately returns early
// on both - a test that wants those paths must pass the nil raw value
// explicitly via runProjectModifyPlanRaw.
func runProjectModifyPlan(t *testing.T, prior, planned *ProjectResourceModel) resource.ModifyPlanResponse {
	t.Helper()
	ctx := context.Background()

	r := &ProjectResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	stateHolder := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := stateHolder.Set(ctx, prior); diags.HasError() {
		t.Fatalf("failed to build the state fixture: %v", diags)
	}
	planHolder := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := planHolder.Set(ctx, planned); diags.HasError() {
		t.Fatalf("failed to build the plan fixture: %v", diags)
	}

	resp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		State: stateHolder,
		Plan:  planHolder,
	}, resp)
	return *resp
}

func projectModel(id string, isDefault bool, description string) *ProjectResourceModel {
	return &ProjectResourceModel{
		ID:          types.StringValue(id),
		CloudID:     types.StringValue("cld_mock"),
		Name:        types.StringValue("project-" + id),
		Description: types.StringValue(description),
		IsDefault:   types.BoolValue(isDefault),
	}
}

// TestProjectResourceModifyPlan_RefusesDescriptionChangeOnDefaultProject is
// the plan-time half of the default-project contract: the API refuses to
// modify a default project (403, "A default project cannot be modified."),
// so a description change on one must fail the plan with an error rather
// than reach Update. This is the only place the exact refusal is asserted.
func TestProjectResourceModifyPlan_RefusesDescriptionChangeOnDefaultProject(t *testing.T) {
	resp := runProjectModifyPlan(t,
		projectModel("prj_default", true, "original"),
		projectModel("prj_default", true, "changed"))

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected a plan-time error for a description change on a default project, got none")
	}
	summary := resp.Diagnostics.Errors()[0].Summary()
	if summary != "Default Project Description Cannot Be Changed" {
		t.Fatalf("unexpected error summary %q", summary)
	}
}

// TestProjectResourceModifyPlan_AllowsSameDescriptionOnDefaultProject: the
// guard keys on a description CHANGE, not on the project being default - a
// plan that leaves the description alone (every unrelated update, or a pure
// refresh) must not error.
func TestProjectResourceModifyPlan_AllowsSameDescriptionOnDefaultProject(t *testing.T) {
	resp := runProjectModifyPlan(t,
		projectModel("prj_default", true, "same"),
		projectModel("prj_default", true, "same"))

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error when the description is unchanged, got: %v", resp.Diagnostics)
	}
}

// TestProjectResourceModifyPlan_AllowsDescriptionChangeOnNonDefaultProject is
// the mutation control for the guard itself: with the guard inverted (keying
// on any project rather than default ones) this is the test that fails.
func TestProjectResourceModifyPlan_AllowsDescriptionChangeOnNonDefaultProject(t *testing.T) {
	resp := runProjectModifyPlan(t,
		projectModel("prj_normal", false, "original"),
		projectModel("prj_normal", false, "changed"))

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error for a description change on a non-default project, got: %v", resp.Diagnostics)
	}
}

// TestProjectResourceModifyPlan_AllowsUnknownDescriptionOnDefaultProject: a
// plan whose description is still unknown (from another resource's output)
// may turn out to equal the current one, so a default project's plan must
// not fail before the value resolves. Control: with a KNOWN changed value
// the refusal still fires (the test above).
func TestProjectResourceModifyPlan_AllowsUnknownDescriptionOnDefaultProject(t *testing.T) {
	resp := runProjectModifyPlan(t,
		projectModel("prj_default", true, "original"),
		&ProjectResourceModel{
			ID:          types.StringValue("prj_default"),
			CloudID:     types.StringValue("cld_mock"),
			Name:        types.StringValue("project-prj_default"),
			Description: types.StringUnknown(),
			IsDefault:   types.BoolValue(true),
		})

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error for an unknown description on a default project, got: %v", resp.Diagnostics)
	}
}

// TestProjectResourceModifyPlan_HasNoPriorStateOnCreate: with no prior state
// there is no current project to protect, so a create-time plan (even one
// that would later be a default project) must not error. A null State.Raw is
// the early-return the production code checks first.
func TestProjectResourceModifyPlan_HasNoPriorStateOnCreate(t *testing.T) {
	ctx := context.Background()

	r := &ProjectResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	planHolder := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := planHolder.Set(ctx, projectModel("prj_new", false, "any")); diags.HasError() {
		t.Fatalf("failed to build the plan fixture: %v", diags)
	}

	resp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		// Null raw state = create.
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)},
		Plan:  planHolder,
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error on create, got: %v", resp.Diagnostics)
	}
}
