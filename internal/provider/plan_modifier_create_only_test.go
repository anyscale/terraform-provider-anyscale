package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The plan-action behavior is covered end to end by the *_MockServer tests in
// internal/acctest/resource_create_only_input_import_acc_test.go. Those cannot
// observe diagnostics, so this checks the warning a practitioner sees when an
// imported resource adopts a configured value. A nil Private is what an
// imported resource has: no Create ever wrote the marker.
func TestRequiresReplaceUnlessUnrecoverable_ImportedAdoptWarns(t *testing.T) {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"secret": tftypes.String}}
	existing := tftypes.NewValue(objType, map[string]tftypes.Value{"secret": tftypes.NewValue(tftypes.String, nil)})

	tests := []struct {
		name            string
		state, config   types.String
		wantReplace     bool
		wantWarningText string
	}{
		{name: "imported null, config sets value: adopt with warning", state: types.StringNull(), config: types.StringValue("s1"), wantWarningText: "cannot be read back from Anyscale after import"},
		{name: "non-null change still replaces", state: types.StringValue("s1"), config: types.StringValue("s2"), wantReplace: true},
		{name: "removing a value still replaces", state: types.StringValue("s1"), config: types.StringNull(), wantReplace: true},
		{name: "unchanged is a no-op", state: types.StringValue("s1"), config: types.StringValue("s1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := planmodifier.StringRequest{
				Path:        path.Root("secret"),
				State:       tfsdk.State{Raw: existing},
				Plan:        tfsdk.Plan{Raw: existing},
				StateValue:  tt.state,
				ConfigValue: tt.config,
				PlanValue:   tt.config,
			}
			resp := &planmodifier.StringResponse{PlanValue: tt.config}

			RequiresReplaceUnlessUnrecoverable("secret", "thing").PlanModifyString(context.Background(), req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error: %v", resp.Diagnostics)
			}
			if resp.RequiresReplace != tt.wantReplace {
				t.Errorf("RequiresReplace = %v, want %v", resp.RequiresReplace, tt.wantReplace)
			}
			warnings := resp.Diagnostics.Warnings()
			if tt.wantWarningText == "" {
				if len(warnings) != 0 {
					t.Errorf("unexpected warnings: %v", warnings)
				}
				return
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0].Detail(), tt.wantWarningText) || !strings.Contains(warnings[0].Detail(), "-replace") {
				t.Errorf("warnings = %v, want one naming %q and -replace", warnings, tt.wantWarningText)
			}
		})
	}
}
