package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Formalizes forge's mount_targets state-compat spike into a permanent
// test: reclassifying file_storage.mount_targets from a
// schema.ListNestedBlock to a schema.ListNestedAttribute+Optional+Computed
// (same nested address/zone types) is a pure value-identity passthrough -
// see decodeUnderNewSchema below for the mechanism. This is a narrower,
// standalone claim than the full v0->v1 resource upgrader test
// (resource_cloud_upgrade_test.go, which also covers the k8s field-drop):
// hand-built scratch schemas isolate it from the rest of the resource
// schema, so a regression in one can't mask a regression in the other.

func mountTargetsScratchAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"address": types.StringType,
		"zone":    types.StringType,
	}
}

// mountTargetsScratchModel is deliberately the SAME Go struct shape for both
// the old and new schema - a Block vs. an Attribute is a schema-level
// classification only, the tfsdk model field underneath is types.List
// either way. Using one struct for both proves that directly rather than
// asserting it in prose.
type mountTargetsScratchModel struct {
	MountTargets types.List `tfsdk:"mount_targets"`
}

func mountTargetsOldBlockSchema() schema.Schema {
	return schema.Schema{
		Blocks: map[string]schema.Block{
			"mount_targets": schema.ListNestedBlock{
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"address": schema.StringAttribute{Optional: true},
						"zone":    schema.StringAttribute{Optional: true},
					},
				},
			},
		},
	}
}

func mountTargetsNewAttributeSchema() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"mount_targets": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"address": schema.StringAttribute{Optional: true, Computed: true},
						"zone":    schema.StringAttribute{Optional: true, Computed: true},
					},
				},
			},
		},
	}
}

// decodeUnderNewSchema builds oldModel into real state under oldSchema, then
// decodes that SAME raw value under newSchema - the crux of the passthrough
// claim: reusing state.Raw directly, not re-deriving it, is what proves no
// transformation happens in between.
func decodeUnderNewSchema(t *testing.T, oldSchema, newSchema schema.Schema, oldModel mountTargetsScratchModel) mountTargetsScratchModel {
	t.Helper()
	ctx := context.Background()

	oldState := &tfsdk.State{
		Schema: oldSchema,
		Raw:    tftypes.NewValue(oldSchema.Type().TerraformType(ctx), nil),
	}
	diags := oldState.Set(ctx, &oldModel)
	if diags.HasError() {
		t.Fatalf("failed to build old Block-shaped state fixture: %v", diags)
	}

	newState := &tfsdk.State{
		Schema: newSchema,
		Raw:    oldState.Raw,
	}

	var newModel mountTargetsScratchModel
	diags = newState.Get(ctx, &newModel)
	if diags.HasError() {
		t.Fatalf("decoding old Block-shaped raw state under the new Attribute schema failed: %v", diags)
	}
	return newModel
}

func TestMountTargetsBlockToAttribute_ValueIdentityPassthrough(t *testing.T) {
	oldSchema := mountTargetsOldBlockSchema()
	newSchema := mountTargetsNewAttributeSchema()

	t.Run("populated element decodes unchanged, no data loss", func(t *testing.T) {
		mtObj, diags := types.ObjectValue(mountTargetsScratchAttrTypes(), map[string]attr.Value{
			"address": types.StringValue("fs-abc123.efs.us-east-2.amazonaws.com"),
			"zone":    types.StringValue("us-east-2a"),
		})
		if diags.HasError() {
			t.Fatalf("failed to build mount_target fixture object: %v", diags)
		}
		mtList, diags := types.ListValue(types.ObjectType{AttrTypes: mountTargetsScratchAttrTypes()}, []attr.Value{mtObj})
		if diags.HasError() {
			t.Fatalf("failed to build mount_targets fixture list: %v", diags)
		}

		newModel := decodeUnderNewSchema(t, oldSchema, newSchema, mountTargetsScratchModel{MountTargets: mtList})

		if newModel.MountTargets.IsNull() || newModel.MountTargets.IsUnknown() {
			t.Fatalf("mount_targets must decode to a known, non-null list, got %#v", newModel.MountTargets)
		}
		elems := newModel.MountTargets.Elements()
		if len(elems) != 1 {
			t.Fatalf("expected exactly 1 recovered mount_target, got %d", len(elems))
		}
		obj, ok := elems[0].(types.Object)
		if !ok {
			t.Fatalf("recovered mount_target element is not types.Object (got %T)", elems[0])
		}
		attrs := obj.Attributes()
		if got := attrs["address"].(types.String).ValueString(); got != "fs-abc123.efs.us-east-2.amazonaws.com" {
			t.Errorf("address = %q, want unchanged fs-abc123.efs.us-east-2.amazonaws.com", got)
		}
		if got := attrs["zone"].(types.String).ValueString(); got != "us-east-2a" {
			t.Errorf("zone = %q, want unchanged us-east-2a", got)
		}
	})

	t.Run("null block decodes to a null attribute list, no crash, no spurious element", func(t *testing.T) {
		newModel := decodeUnderNewSchema(t, oldSchema, newSchema, mountTargetsScratchModel{
			MountTargets: types.ListNull(types.ObjectType{AttrTypes: mountTargetsScratchAttrTypes()}),
		})

		if !newModel.MountTargets.IsNull() {
			t.Errorf("mount_targets = %#v, want null (this is the real production shape today - every existing "+
				"cloud has mount_targets null in state since v0.16.1's Option C fix, not populated)", newModel.MountTargets)
		}
	})
}
