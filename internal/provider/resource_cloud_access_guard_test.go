package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
)

// TestCloudAccessModifyPlan_AlreadyEmptyStaysEmpty covers the one real branch
// of the empty-member-set guard that resource_cloud_access_test.go's own
// suite does not exercise: a cloud that ALREADY has zero members, planned to
// stay at zero members, must not trip the guard - emptying an already-empty
// cloud revokes nobody, so erroring here would be pure noise with no safety
// benefit. This is a distinct code path (the currentCount == 0 short-circuit
// in ModifyPlan) from "creating with an empty member map" (nil prior state)
// and from "a populated cloud going empty" (the guard's main case) - all
// three look similar from the outside but exercise different branches.
//
// Kept in its own file rather than added to resource_cloud_access_test.go:
// that file is owned by separate in-flight work, and this session's own audit found that two
// branches independently touching one file is an integration hazard that
// neither branch can see from inside itself.
func TestCloudAccessModifyPlan_AlreadyEmptyStaysEmpty(t *testing.T) {
	empty := cloudAccessMemberMap(map[string]attr.Value{})

	priorEmpty := func() *CloudAccessResourceModel {
		m := cloudAccessConfigModel(empty)
		return &m
	}
	plannedEmpty := func() *CloudAccessResourceModel {
		m := cloudAccessConfigModel(empty)
		return &m
	}

	diags := runCloudAccessModifyPlan(t, priorEmpty(), plannedEmpty())
	if diags.HasError() {
		t.Fatalf("emptying an already-empty cloud revokes nobody and must not trip the guard. Got: %v", diags)
	}
}
