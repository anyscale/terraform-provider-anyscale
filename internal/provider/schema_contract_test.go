package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// These descriptions are hardcoded, stable strings on the framework's own
// modifier types (see stringplanmodifier.RequiresReplace /
// UseStateForUnknown) and are the only exported way to identify which
// modifier is present without depending on their unexported concrete types.
const (
	descRequiresReplace    = "If the value of this attribute changes, Terraform will destroy and recreate the resource."
	descUseStateForUnknown = "Once set, the value of this attribute in state will not change."
)

// schemaOf returns the resource.Schema for a resource.Resource implementation
// by calling Schema() directly, with no provider server or API client needed.
func schemaOf(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema() returned diagnostics: %s", resp.Diagnostics)
	}
	return resp.Schema
}

func hasPlanModifierDescription(mods []planmodifier.String, want string) bool {
	for _, m := range mods {
		if m.Description(context.Background()) == want {
			return true
		}
	}
	return false
}

// TestServerInferredStringAttributesAreComputedWithUseStateForUnknown pins
// the schema contract for "server-inferred creation-time" string attributes:
// ones the user may omit (the server derives/defaults a value, e.g.
// compute_stack defaulting to VM) or set explicitly, where the value is fixed
// for the life of the resource (hence RequiresReplace).
//
// Omit-or-set-explicitly requires ALL THREE of Optional, Computed, and
// UseStateForUnknown together. Optional without Computed lets Terraform plan
// an omitted config value as a hard null; when the API then returns a
// non-null value after apply, the framework has no way to reconcile the two
// and errors with "Provider produced inconsistent result after apply".
// UseStateForUnknown is what keeps that resolved value stable across later
// plans and import round-trips instead of showing a perpetual diff.
//
// anyscale_cloud.compute_stack shipped without Computed (see
// .crystl/quest/spec.json finding F1) while its siblings cloud_provider and
// region on the same resource had the full set — a schema copy/paste gap
// that only a live acceptance-test apply could catch, because nothing
// exercises plan-consistency at the schema level. This test catches the same
// class of regression in milliseconds instead of a ~30s+ acceptance test run.
func TestServerInferredStringAttributesAreComputedWithUseStateForUnknown(t *testing.T) {
	cases := []struct {
		resource  resource.Resource
		attribute string
	}{
		{&CloudResource{}, "compute_stack"},
		{&CloudResource{}, "cloud_provider"},
		{&CloudResource{}, "region"},
		{&CloudResourceResource{}, "compute_stack"},
		{&CloudResourceResource{}, "cloud_provider"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("%T/%s", tc.resource, tc.attribute), func(t *testing.T) {
			s := schemaOf(t, tc.resource)
			attr, ok := s.Attributes[tc.attribute]
			if !ok {
				t.Fatalf("attribute %q not found in schema", tc.attribute)
			}
			strAttr, ok := attr.(schema.StringAttribute)
			if !ok {
				t.Fatalf("attribute %q is not a schema.StringAttribute (got %T)", tc.attribute, attr)
			}

			if !strAttr.Optional {
				t.Errorf("%q must be Optional: true (server-inferred attributes may be omitted by the user)", tc.attribute)
			}
			if !strAttr.Computed {
				t.Errorf("%q must be Computed: true — without it, omitting the attribute plans a hard null that can never "+
					"reconcile against a non-null API response, producing 'Provider produced inconsistent result after apply'", tc.attribute)
			}
			if !hasPlanModifierDescription(strAttr.PlanModifiers, descUseStateForUnknown) {
				t.Errorf("%q must include stringplanmodifier.UseStateForUnknown() so the resolved value is stable "+
					"across subsequent plans and import round-trips", tc.attribute)
			}
			if !hasPlanModifierDescription(strAttr.PlanModifiers, descRequiresReplace) {
				t.Errorf("%q must include stringplanmodifier.RequiresReplace() — it is a creation-time property", tc.attribute)
			}
		})
	}
}
