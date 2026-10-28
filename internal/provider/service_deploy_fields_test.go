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

// TestServiceDeployFieldsAreFrozenWhileStopped: a plan that stops the service or keeps it stopped
// deploys nothing, so every deploy field must be rejected there, ray_serve_config included.
func TestServiceDeployFieldsAreFrozenWhileStopped(t *testing.T) {
	for name, pair := range serviceDeployFieldModels(t) {
		plan, state := pair[0], pair[1]
		if len(serviceDeployFieldChanges(&plan, &state)) == 0 {
			t.Errorf("deploy field %s changes while stopped without being rejected", name)
		}
	}
}

// TestServiceStopAndRestartRequired: the stop and restart predicates key on the live goal_state
// and the planned stopped value; an unknown stopped is treated as running, so a terminated
// service plans its restart.
func TestServiceStopAndRestartRequired(t *testing.T) {
	cases := []struct {
		name          string
		goal          string
		stopped       types.Bool
		stop, restart bool
	}{
		{"running, stays running", "RUNNING", types.BoolValue(false), false, false},
		{"running, stop", "RUNNING", types.BoolValue(true), true, false},
		{"terminated, restart", "TERMINATED", types.BoolValue(false), false, true},
		{"terminated, stays stopped", "TERMINATED", types.BoolValue(true), false, false},
		{"terminated, stopped unknown", "TERMINATED", types.BoolUnknown(), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := ServiceResourceModel{Stopped: tc.stopped}
			state := ServiceResourceModel{GoalState: types.StringValue(tc.goal)}
			if got := serviceStopRequired(&plan, &state); got != tc.stop {
				t.Errorf("serviceStopRequired = %t, want %t", got, tc.stop)
			}
			if got := serviceRestartRequired(&plan, &state); got != tc.restart {
				t.Errorf("serviceRestartRequired = %t, want %t", got, tc.restart)
			}
		})
	}
}

// TestServiceAuthChangedIgnoresUnrefreshedState: state written before query_auth_token_enabled
// existed holds null until the first refresh; planning against it (terraform plan
// -refresh=false) must not roll out a new version.
func TestServiceAuthChangedIgnoresUnrefreshedState(t *testing.T) {
	plan := ServiceResourceModel{QueryAuthTokenEnabled: types.BoolValue(true)}
	if serviceAuthChanged(&plan, &ServiceResourceModel{QueryAuthTokenEnabled: types.BoolNull()}) {
		t.Error("null prior query_auth_token_enabled counted as a change")
	}
	if !serviceAuthChanged(&plan, &ServiceResourceModel{QueryAuthTokenEnabled: types.BoolValue(false)}) {
		t.Error("false -> true not counted as a change")
	}
}
