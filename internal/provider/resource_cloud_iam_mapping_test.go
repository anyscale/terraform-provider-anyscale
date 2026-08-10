package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// cloudIAMMappingTimeoutsAttrTypes is the attr.Type map the timeouts.Block
// schema option (Create/Update/Delete, no Read) produces.
func cloudIAMMappingTimeoutsAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"create": types.StringType,
		"update": types.StringType,
		"delete": types.StringType,
	}
}

// cloudIAMMappingConfigModel wraps rules/fallback_rule in an otherwise
// realistic configuration: cloud_id set, every Computed attribute null (as
// they are in a real config).
func cloudIAMMappingConfigModel(rules types.List, fallbackRule types.String) CloudIAMMappingResourceModel {
	return CloudIAMMappingResourceModel{
		ID:              types.StringNull(),
		CloudID:         types.StringValue("cld_iammappingtest"),
		CloudResourceID: types.StringNull(),
		Rules:           rules,
		FallbackRule:    fallbackRule,
		Mode:            types.StringNull(),
		Timeouts:        timeouts.Value{Object: types.ObjectNull(cloudIAMMappingTimeoutsAttrTypes())},
	}
}

func cloudIAMMappingRulesList(rules ...CloudIAMMappingRuleModel) types.List {
	elems := make([]attr.Value, 0, len(rules))
	for _, rule := range rules {
		obj, diags := types.ObjectValueFrom(context.Background(), cloudIAMMappingRuleAttrTypes(), rule)
		if diags.HasError() {
			panic(diags)
		}
		elems = append(elems, obj)
	}
	return types.ListValueMust(cloudIAMMappingRuleType(), elems)
}

func cloudIAMMappingNullRules() types.List {
	return types.ListNull(cloudIAMMappingRuleType())
}

func cloudIAMMappingEmptyRules() types.List {
	return types.ListValueMust(cloudIAMMappingRuleType(), []attr.Value{})
}

// runCloudIAMMappingValidateConfig drives the real ValidateConfig against a
// configuration built from the resource's own schema - see
// resource_cloud_access_test.go's runCloudAccessValidateConfig for why this
// is built from tfsdk.Plan.Set rather than synthesized by hand.
func runCloudIAMMappingValidateConfig(t *testing.T, model CloudIAMMappingResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	r := &CloudIAMMappingResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	holder := tfsdk.Plan{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	if diags := holder.Set(ctx, &model); diags.HasError() {
		t.Fatalf("failed to build config fixture: %v", diags)
	}

	resp := &resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: holder.Raw},
	}, resp)
	return resp.Diagnostics
}

func cloudIAMMappingFindError(diags diag.Diagnostics, summary string) diag.Diagnostic {
	for _, d := range diags {
		if d.Severity() == diag.SeverityError && d.Summary() == summary {
			return d
		}
	}
	return nil
}

// TestCloudIAMMappingValidateConfig_ExplicitEmptyRulesRejected is Finding 1's
// regression test (architect's integration review of 738752a): rules = []
// can never converge, because Read canonicalizes "no mapping" to null and
// rules is not Computed, so an empty-list config would diff against that
// null state on every plan forever. This test FAILS against the version of
// ValidateConfig that only checked len(Elements())==0 without also
// distinguishing IsNull() - confirmed by temporarily reverting to that
// shape and observing this test go red before restoring the fix.
func TestCloudIAMMappingValidateConfig_ExplicitEmptyRulesRejected(t *testing.T) {
	diags := runCloudIAMMappingValidateConfig(t, cloudIAMMappingConfigModel(cloudIAMMappingEmptyRules(), types.StringNull()))

	if err := cloudIAMMappingFindError(diags, "rules must not be an explicit empty list"); err == nil {
		t.Fatalf("expected an explicit-empty-list error, got diagnostics: %v", diags)
	}
}

// TestCloudIAMMappingValidateConfig_NullRulesIsValid is the explicit-empty
// rejection's necessary counterpart: omitting rules entirely (null) must
// keep working as "this cloud has no IAM mapping" - the only legitimate way
// to express that.
func TestCloudIAMMappingValidateConfig_NullRulesIsValid(t *testing.T) {
	diags := runCloudIAMMappingValidateConfig(t, cloudIAMMappingConfigModel(cloudIAMMappingNullRules(), types.StringNull()))

	if diags.HasError() {
		t.Fatalf("expected no error for omitted rules, got: %v", diags)
	}
}

func TestCloudIAMMappingValidateConfig_FallbackRuleRequiredWhenRulesNonEmpty(t *testing.T) {
	rules := cloudIAMMappingRulesList(CloudIAMMappingRuleModel{
		Selector: types.StringValue("workload-type=job"),
		Value:    types.StringValue("role-a"),
	})
	diags := runCloudIAMMappingValidateConfig(t, cloudIAMMappingConfigModel(rules, types.StringNull()))

	if err := cloudIAMMappingFindError(diags, "fallback_rule is required when rules is non-empty"); err == nil {
		t.Fatalf("expected a fallback_rule-required error, got diagnostics: %v", diags)
	}
}

func TestCloudIAMMappingValidateConfig_FallbackRuleRejectedWhenRulesEmpty(t *testing.T) {
	diags := runCloudIAMMappingValidateConfig(t, cloudIAMMappingConfigModel(cloudIAMMappingNullRules(), types.StringValue("CLOUD_DEFAULT")))

	if err := cloudIAMMappingFindError(diags, "fallback_rule requires a non-empty rules list"); err == nil {
		t.Fatalf("expected a fallback_rule-requires-non-empty-rules error, got diagnostics: %v", diags)
	}
}

func TestCloudIAMMappingValidateConfig_InvalidSelectorRejected(t *testing.T) {
	rules := cloudIAMMappingRulesList(CloudIAMMappingRuleModel{
		Selector: types.StringValue("team=platform"),
		Value:    types.StringValue("role-a"),
	})
	diags := runCloudIAMMappingValidateConfig(t, cloudIAMMappingConfigModel(rules, types.StringValue("FAIL")))

	if err := cloudIAMMappingFindError(diags, "Invalid IAM mapping rule selector"); err == nil {
		t.Fatalf("expected an invalid-selector error, got diagnostics: %v", diags)
	}
}

func TestCloudIAMMappingValidateConfig_ValidConfigPasses(t *testing.T) {
	rules := cloudIAMMappingRulesList(CloudIAMMappingRuleModel{
		Selector: types.StringValue("workload-type=job"),
		Value:    types.StringValue("role-a"),
	})
	diags := runCloudIAMMappingValidateConfig(t, cloudIAMMappingConfigModel(rules, types.StringValue("FAIL")))

	if diags.HasError() {
		t.Fatalf("expected no error for a valid config, got: %v", diags)
	}
}
