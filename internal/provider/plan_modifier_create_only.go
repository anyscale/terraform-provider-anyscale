package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// createdByTerraformKey is the private-state key MarkCreatedByTerraform writes.
// Its absence means the state came from `terraform import`, or from a provider
// version that predates the marker.
const createdByTerraformKey = "created_by_terraform"

// MarkCreatedByTerraform records in private state that this provider created
// the resource. Call it from Create before the first State.Set, so every state
// Create persists carries the marker. RequiresReplaceUnlessUnrecoverable reads it.
func MarkCreatedByTerraform(ctx context.Context, resp *resource.CreateResponse) {
	// The framework always supplies Private on a real Create; only unit tests
	// that build a CreateResponse by hand leave it nil, and private state
	// cannot be constructed outside the framework.
	if resp.Private == nil {
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, createdByTerraformKey, []byte("true"))...)
}

// RequiresReplaceUnlessUnrecoverable is RequiresReplace for a create-only input
// that the Anyscale API never returns, so import cannot recover it and leaves it
// null.
//
// It behaves exactly like stringplanmodifier.RequiresReplace except in one case:
// the prior state value is null, the config sets a value, and the resource was
// not created by this provider (no MarkCreatedByTerraform marker). Then the
// configured value is planned as an in-place update with a warning instead of a
// replacement. The resource's Update must copy that value from plan to state
// without sending it to the API.
//
// Without this, the same configuration that created an object would destroy and
// recreate it after an import. A missing marker also covers state written by
// provider versions older than the marker: there, adding a value to a resource
// created without one is adopted rather than forcing replacement, which is the
// safe direction.
func RequiresReplaceUnlessUnrecoverable(attrName, objectName string) planmodifier.String {
	return requiresReplaceUnlessUnrecoverableModifier{attrName: attrName, objectName: objectName}
}

type requiresReplaceUnlessUnrecoverableModifier struct {
	attrName   string
	objectName string
}

func (m requiresReplaceUnlessUnrecoverableModifier) Description(_ context.Context) string {
	return fmt.Sprintf("Changing %s replaces the %s, except that setting it on an imported %s records the value without replacing it.", m.attrName, m.objectName, m.objectName)
}

func (m requiresReplaceUnlessUnrecoverableModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m requiresReplaceUnlessUnrecoverableModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Create and destroy never replace.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if req.PlanValue.Equal(req.StateValue) {
		return
	}

	if req.StateValue.IsNull() && !req.ConfigValue.IsNull() {
		marker, diags := req.Private.GetKey(ctx, createdByTerraformKey)
		resp.Diagnostics.Append(diags...)
		if diags.HasError() {
			return
		}
		if marker == nil {
			resp.Diagnostics.AddAttributeWarning(req.Path, "Value Recorded Without Replacement",
				fmt.Sprintf("%s cannot be read back from Anyscale after import; Terraform will record the configured value without sending it. "+
					"The existing %s is not changed. Use -replace to recreate it with this value.", m.attrName, m.objectName))
			return
		}
	}

	resp.RequiresReplace = true
}
