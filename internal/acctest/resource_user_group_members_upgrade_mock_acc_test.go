package acctest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Upgrade from v0.30.0, where anyscale_user_group_members took group_id, to
// this build, where it takes user_group_id. State written by v0.30.0 is
// upgraded in place, so the renamed config plans no changes and needs no
// re-import. Step 1 is the positive control that the external provider
// really is v0.30.0 (it rejects user_group_id); without it, a dev_overrides
// CLI config that substitutes the local build would make this test pass
// without testing an upgrade. Needs the public Registry to download v0.30.0.
func TestAccUserGroupMembersResource_UpgradeFromV030RenamesGroupID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB)
	const name = "tfacc-ug-mock-upgrade"
	current := ugMockConfig(m, name, ugUserA.Email, ugUserB.Email)
	legacy := strings.Replace(current, "  user_group_id = ", "  group_id = ", 1)
	if legacy == current {
		t.Fatal("test bug: the v0.30.0 config still uses user_group_id")
	}
	v030 := map[string]resource.ExternalProvider{
		"anyscale": {Source: "anyscale/anyscale", VersionConstraint: "0.30.0"},
	}
	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ExternalProviders: v030,
				Config:            current,
				ExpectError:       regexp.MustCompile(`(?s)Unsupported argument.*user_group_id`),
			},
			{
				ExternalProviders: v030,
				Config:            legacy,
				Check:             ugExpectMembers(m, ugUserA.UserID, ugUserB.UserID),
			},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   current,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"anyscale_user_group_members.test", tfjsonpath.New("user_group_id"),
						"anyscale_user_group.test", tfjsonpath.New("id"),
						compare.ValuesSame(),
					),
					statecheck.ExpectKnownValue("anyscale_user_group_members.test", tfjsonpath.New("user_group_id"),
						knownvalue.StringRegexp(regexp.MustCompile(`^ug_`))),
				},
			},
		},
	})
}
