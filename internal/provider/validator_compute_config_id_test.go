package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestComputeConfigIDValidator(t *testing.T) {
	cases := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"cpt id", types.StringValue("cpt_abc123"), false},
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
		// anyscale_compute_config.<x>.id is the config's name.
		{"compute config name", types.StringValue("my-compute-config"), true},
		{"name:version", types.StringValue("my-compute-config:3"), true},
		{"empty", types.StringValue(""), true},
		{"prefix without underscore", types.StringValue("cptabc123"), true},
		{"wrong case", types.StringValue("CPT_abc123"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validator.StringRequest{Path: path.Root("compute_config_id"), ConfigValue: tc.value}
			resp := &validator.StringResponse{}
			computeConfigIDValidator{}.ValidateString(context.Background(), req, resp)

			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Fatalf("HasError() = %v, want %v (diags: %v)", got, tc.wantErr, resp.Diagnostics)
			}
			if !tc.wantErr {
				return
			}
			detail := resp.Diagnostics.Errors()[0].Detail()
			for _, want := range []string{"config_id", "not id"} {
				if !strings.Contains(detail, want) {
					t.Errorf("error detail %q should mention %q", detail, want)
				}
			}
		})
	}
}

// TestServiceResourceSchema_ComputeConfigIDValidated pins the validator to the attribute, so
// dropping it from the schema fails a unit test, not only the acceptance test.
func TestServiceResourceSchema_ComputeConfigIDValidated(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewServiceResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	attr, ok := resp.Schema.Attributes["compute_config_id"].(schema.StringAttribute)
	if !ok {
		t.Fatal("compute_config_id is not a schema.StringAttribute")
	}
	for _, v := range attr.Validators {
		if _, ok := v.(computeConfigIDValidator); ok {
			return
		}
	}
	t.Fatal("compute_config_id has no computeConfigIDValidator")
}
