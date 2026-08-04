package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var preIssue219CollaboratorObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"email":            types.StringType,
	"permission_level": types.StringType,
	"identity_id":      types.StringType,
	"user_id":          types.StringType,
}}

// preIssue219ProjectModel mirrors anyscale_project's schema as it existed
// BEFORE PR #219 (73d7443) removed cloud_name - the same schema version 0,
// since #219 shipped without a SchemaVersion bump.
// Identical to projectResourceModelV0 (resource_project_upgrade.go) plus the
// one extra field that PriorSchema's frozen snapshot does NOT declare.
type preIssue219ProjectModel struct {
	ID                     types.String `tfsdk:"id"`
	CloudID                types.String `tfsdk:"cloud_id"`
	CloudName              types.String `tfsdk:"cloud_name"`
	Name                   types.String `tfsdk:"name"`
	Description            types.String `tfsdk:"description"`
	InitialClusterConfigID types.String `tfsdk:"initial_cluster_config_id"`
	Collaborators          types.List   `tfsdk:"collaborator"`
	CreatorID              types.String `tfsdk:"creator_id"`
	CreatedAt              types.String `tfsdk:"created_at"`
	LastUsedCloudID        types.String `tfsdk:"last_used_cloud_id"`
	IsDefault              types.Bool   `tfsdk:"is_default"`
	DirectoryName          types.String `tfsdk:"directory_name"`
}

// preIssue219ProjectSchema is projectSchemaV0() (resource_project_upgrade.go)
// plus cloud_name - reconstructing the REAL pre-#219 shape, not a guess: #219
// (73d7443) removed cloud_name from anyscale_project, and grepping that diff
// shows no SchemaVersion change alongside it. So real state written before
// #219 and real state written after both carry schema version 0 in Terraform's
// eyes, but they are NOT the same shape - only the post-#219 one matches what
// projectSchemaV0() (and therefore 74e555e's UpgradeState) actually declares.
func preIssue219ProjectSchema() *schema.Schema {
	return &schema.Schema{
		Version:             0,
		MarkdownDescription: "Pre-#219 shape, for the PriorSchema mismatch check only.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud_id": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.NoneOfCaseInsensitive("-", "default"),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"initial_cluster_config_id": schema.StringAttribute{
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"creator_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"last_used_cloud_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"directory_name": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"collaborator": schema.ListNestedBlock{
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"email":            schema.StringAttribute{Required: true},
						"permission_level": schema.StringAttribute{Required: true},
						"identity_id":      schema.StringAttribute{Computed: true},
						"user_id":          schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

// TestProjectStateUpgradeV0toV1_PreIssue219ShapeWithCloudName is a
// precise check: real pre-#219 state has a cloud_name key that
// projectSchemaV0() (the PriorSchema 74e555e's upgrader actually declares)
// does not. Does upgradeProjectStateV0toV1 succeed against that real older
// shape, or does decoding fail the moment it hits a key PriorSchema never
// declared?
func TestProjectStateUpgradeV0toV1_PreIssue219ShapeWithCloudName(t *testing.T) {
	ctx := context.Background()

	preIssue219State := preIssue219ProjectModel{
		ID:                     types.StringValue("prj_pre219"),
		CloudID:                types.StringValue("cld_pre219"),
		CloudName:              types.StringValue("my-cloud"),
		Name:                   types.StringValue("project-pre-219"),
		Description:            types.StringValue("a pre-219 project"),
		InitialClusterConfigID: types.StringValue("ccfg_pre219"),
		Collaborators:          types.ListValueMust(preIssue219CollaboratorObjectType, []attr.Value{}),
		CreatorID:              types.StringValue("usr_creator_pre219"),
		CreatedAt:              types.StringValue("2025-01-01T00:00:00Z"),
		LastUsedCloudID:        types.StringValue("cld_pre219"),
		IsDefault:              types.BoolValue(false),
		DirectoryName:          types.StringValue("project-pre-219-dir"),
	}

	oldSchema := preIssue219ProjectSchema()
	priorState := &tfsdk.State{
		Schema: *oldSchema,
		Raw:    tftypes.NewValue(oldSchema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &preIssue219State); diags.HasError() {
		t.Fatalf("failed to build the pre-#219 state fixture (this would mean my reconstruction of the old schema is wrong, not that the real upgrader is): %v", diags)
	}

	// The response's State must be pre-populated with the REAL current (v1)
	// schema before the upgrader can encode into it - matching the pattern
	// resource_project_upgrade_test.go's own DropsCollaborator test uses. An
	// earlier draft of this test used a bare zero-value response and got a
	// framework panic instead of a diagnostic - that was this test's own
	// construction bug, not a finding about the upgrader, caught by comparing
	// against the established pattern before reporting it as real.
	r := &ProjectResource{}
	var v1SchemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &v1SchemaResp)
	if v1SchemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v1 (current) schema: %v", v1SchemaResp.Diagnostics)
	}

	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: v1SchemaResp.Schema,
			Raw:    tftypes.NewValue(v1SchemaResp.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgradeProjectStateV0toV1(ctx, resource.UpgradeStateRequest{State: priorState}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("the shipped upgrader FAILS against real pre-#219 state (has cloud_name): %v", resp.Diagnostics)
	}
	t.Logf("upgrader succeeded against pre-#219 shape; resulting state: %+v", resp.State)
}
