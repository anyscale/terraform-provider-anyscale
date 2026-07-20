package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestDynamicToInterface_ListOfObjects is the permanent regression guard for a real bug found by
// the anyscale_service real-infra acceptance test (contract AC-R5): a Dynamic-typed attribute
// whose value is a LIST OF OBJECTS - e.g. ray_serve_config.applications, or any future
// Dynamic-typed field shaped this way - is inferred by HCL/Terraform as a types.Tuple, not a
// types.List, because tuple element types can differ per position while list elements share one
// type. convertAttrValueToInterface had no types.Tuple case, so the switch fell through and
// silently returned nil for the whole value - meaning a real ray_serve_config.applications list
// (always object-shaped entries; that is the entire point of the field) serialized to JSON
// `null` on the wire, 422ing with "applications: none is not an allowed value". This is not
// specific to Service: DynamicToInterface/convertAttrValueToInterface is shared with
// resource_compute_config.go's advanced_instance_config/flags, so the same hazard applies there
// too if either is ever given a list-of-objects shape.
//
// This test builds the exact shape that failed against the real backend - a list containing one
// object with a nested object (mirroring ray_serve_config's
// applications = [{ import_path = ..., runtime_env = { working_dir = ... } }]) - entirely via
// framework types, no HCL/Terraform Core/mock server needed, so it is fast and isolates exactly
// the mechanism that broke.
func TestDynamicToInterface_ListOfObjects(t *testing.T) {
	runtimeEnvType := map[string]attr.Type{"working_dir": types.StringType}
	runtimeEnvObj, diags := types.ObjectValue(
		runtimeEnvType,
		map[string]attr.Value{"working_dir": types.StringValue("https://example.com/app.zip")},
	)
	if diags.HasError() {
		t.Fatalf("build runtime_env object: %v", diags)
	}

	appAttrTypes := map[string]attr.Type{
		"import_path": types.StringType,
		"runtime_env": types.ObjectType{AttrTypes: runtimeEnvType},
	}
	appObj, diags := types.ObjectValue(appAttrTypes, map[string]attr.Value{
		"import_path": types.StringValue("main:app"),
		"runtime_env": runtimeEnvObj,
	})
	if diags.HasError() {
		t.Fatalf("build application object: %v", diags)
	}

	appElemType := types.ObjectType{AttrTypes: appAttrTypes}
	applicationsTuple, diags := types.TupleValue([]attr.Type{appElemType}, []attr.Value{appObj})
	if diags.HasError() {
		t.Fatalf("build applications tuple: %v", diags)
	}

	outerAttrTypes := map[string]attr.Type{
		"applications": types.TupleType{ElemTypes: []attr.Type{appElemType}},
	}
	outerObj, diags := types.ObjectValue(outerAttrTypes, map[string]attr.Value{
		"applications": applicationsTuple,
	})
	if diags.HasError() {
		t.Fatalf("build outer object: %v", diags)
	}

	result, err := DynamicToInterface(context.Background(), types.DynamicValue(outerObj))
	if err != nil {
		t.Fatalf("DynamicToInterface returned an error: %v", err)
	}

	apps, ok := result["applications"].([]interface{})
	if !ok {
		t.Fatalf("applications = %#v (type %T), want a []interface{} with one element - "+
			"nil here means convertAttrValueToInterface silently dropped a types.Tuple value, "+
			"exactly the bug that made a real ray_serve_config.applications list serialize to "+
			"JSON null and 422 against the real backend", result["applications"], result["applications"])
	}
	if len(apps) != 1 {
		t.Fatalf("len(applications) = %d, want 1", len(apps))
	}

	app, ok := apps[0].(map[string]interface{})
	if !ok {
		t.Fatalf("applications[0] = %#v, want a map[string]interface{}", apps[0])
	}
	if got, want := app["import_path"], "main:app"; got != want {
		t.Errorf("applications[0].import_path = %v, want %v", got, want)
	}
	runtimeEnv, ok := app["runtime_env"].(map[string]interface{})
	if !ok {
		t.Fatalf("applications[0].runtime_env = %#v, want a map[string]interface{} (proves nested objects inside a tuple element also convert correctly)", app["runtime_env"])
	}
	if got, want := runtimeEnv["working_dir"], "https://example.com/app.zip"; got != want {
		t.Errorf("applications[0].runtime_env.working_dir = %v, want %v", got, want)
	}
}

// TestConvertAttrValueToInterface_EmptyTuple guards the boundary case: an empty list-of-objects
// (e.g. applications = []) must still convert to a real, empty []interface{}, not nil - the
// difference between "no applications" (a real, if useless, empty list) and "applications field
// missing/null" matters to the same 422 this bug produces.
func TestConvertAttrValueToInterface_EmptyTuple(t *testing.T) {
	emptyTuple, diags := types.TupleValue(nil, nil)
	if diags.HasError() {
		t.Fatalf("build empty tuple: %v", diags)
	}

	got := convertAttrValueToInterface(emptyTuple)
	list, ok := got.([]interface{})
	if !ok {
		t.Fatalf("convertAttrValueToInterface(empty tuple) = %#v (type %T), want []interface{}", got, got)
	}
	if len(list) != 0 {
		t.Errorf("len = %d, want 0", len(list))
	}
}
