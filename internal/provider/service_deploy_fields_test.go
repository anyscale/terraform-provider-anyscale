package provider

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// serviceModelFieldPair returns two different known values of the field type at index i of
// ServiceResourceModel, or ok=false for a field the deploy-field invariants do not apply to.
// It fails the test on a field type it does not know, so a new attribute cannot slip past the
// invariant tests below unexamined.
func serviceModelFieldPair(t *testing.T, f reflect.StructField) (a, b any, ok bool) {
	t.Helper()
	switch f.Type {
	case reflect.TypeOf(types.String{}):
		return types.StringValue("a"), types.StringValue("b"), true
	case reflect.TypeOf(types.Bool{}):
		return types.BoolValue(false), types.BoolValue(true), true
	case reflect.TypeOf(types.Int64{}):
		return types.Int64Value(1), types.Int64Value(2), true
	case reflect.TypeOf(types.List{}):
		return types.ListValueMust(types.StringType, []attr.Value{types.StringValue("a")}),
			types.ListValueMust(types.StringType, []attr.Value{types.StringValue("b")}), true
	case reflect.TypeOf(types.Map{}):
		return types.MapValueMust(types.StringType, map[string]attr.Value{"k": types.StringValue("a")}),
			types.MapValueMust(types.StringType, map[string]attr.Value{"k": types.StringValue("b")}), true
	case reflect.TypeOf(types.Dynamic{}):
		return types.DynamicValue(types.StringValue("a")), types.DynamicValue(types.StringValue("b")), true
	case reflect.TypeOf(types.Object{}):
		return types.ObjectNull(map[string]attr.Type{}), types.ObjectUnknown(map[string]attr.Type{}), true
	case reflect.TypeOf(timeouts.Value{}):
		return nil, nil, false // local to the provider; never deployed
	default:
		t.Fatalf("field %s has type %s, unknown to serviceModelFieldPair: add a case", f.Name, f.Type)
		return nil, nil, false
	}
}

// serviceDeployFieldModels yields, for every field that serviceDeployFieldsChanged counts, a
// (plan, state) pair differing in that field only.
func serviceDeployFieldModels(t *testing.T) map[string][2]ServiceResourceModel {
	t.Helper()
	out := map[string][2]ServiceResourceModel{}
	typ := reflect.TypeOf(ServiceResourceModel{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		_, b, ok := serviceModelFieldPair(t, f)
		if !ok {
			continue
		}
		var state, plan ServiceResourceModel
		// Every other field equal and known, so only field i can differ.
		for j := 0; j < typ.NumField(); j++ {
			if fa, _, ok := serviceModelFieldPair(t, typ.Field(j)); ok {
				reflect.ValueOf(&state).Elem().Field(j).Set(reflect.ValueOf(fa))
				reflect.ValueOf(&plan).Elem().Field(j).Set(reflect.ValueOf(fa))
			}
		}
		reflect.ValueOf(&plan).Elem().Field(i).Set(reflect.ValueOf(b))
		if serviceDeployFieldsChanged(&plan, &state) {
			out[f.Name] = [2]ServiceResourceModel{plan, state}
		}
	}
	return out
}

// TestServiceDeployFieldsAreRevertedOnRollback: every field that triggers a rollout must be one
// that a rolled-back rollout restores, or the next plan would not show the change again. Adding a
// deploy field to serviceDeployFieldsChanged without adding it to withPriorServiceDeployFields
// fails here.
func TestServiceDeployFieldsAreRevertedOnRollback(t *testing.T) {
	fields := serviceDeployFieldModels(t)
	if len(fields) < 5 {
		t.Fatalf("found only %d deploy fields; the field enumeration is broken", len(fields))
	}
	for name, pair := range fields {
		plan, state := pair[0], pair[1]
		reverted := withPriorServiceDeployFields(plan, state)
		if serviceDeployFieldsChanged(&reverted, &state) {
			t.Errorf("deploy field %s is not restored by withPriorServiceDeployFields", name)
		}
	}
}

// TestServiceDeployFieldsAreFrozenUnderInPlace: every deploy field except ray_serve_config must
// be rejected under IN_PLACE, which applies only ray_serve_config.
func TestServiceDeployFieldsAreFrozenUnderInPlace(t *testing.T) {
	for name, pair := range serviceDeployFieldModels(t) {
		plan, state := pair[0], pair[1]
		frozen := serviceInPlaceFrozenChanges(&plan, &state)
		if name == "RayServeConfig" {
			if len(frozen) != 0 {
				t.Errorf("ray_serve_config change reported as frozen: %v", frozen)
			}
			continue
		}
		if len(frozen) == 0 {
			t.Errorf("deploy field %s changes under IN_PLACE without being rejected", name)
		}
	}
}
