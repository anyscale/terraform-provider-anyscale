package acctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccOrganizationUserResource_RealAPI declares an existing member as an
// anyscale_organization_user against the real API: adopt, flip
// reinvite_if_expired in place, import by email, destroy.
//
// For a member, every step is read-only on the backend: create adopts without
// sending anything, update is a state-only write, and destroy of an adopted
// member removes nothing. The test exists to prove exactly that on the real
// API, so CheckDestroy and the cleanup both read the member back and fail if
// they were evicted or their role moved. There is nothing to restore by API if
// they were: re-adding a member needs a new invitation that a person accepts.
//
// ANYSCALE_TEST_USER_EMAIL must be a disposable, accepted, non-owner member
// that is not the token's own identity; requireRealInfraTestUser enforces all
// of that and ANYSCALE_TEST_ORG_NAME.
//
// Run: TF_ACC=1 ANYSCALE_TEST_ORG_MUTATION=1 ANYSCALE_TEST_USER_EMAIL=<disposable member> ANYSCALE_TEST_ORG_NAME=<org> go test ./internal/acctest -run '^TestAccOrganizationUserResource_RealAPI$' -v -count=1
func TestAccOrganizationUserResource_RealAPI(t *testing.T) {
	skipUnlessOrgMutation(t)
	email := requireRealInfraTestUser(t)

	before := mustReadLiveOrgRole(t, email)
	t.Logf("SNAPSHOT: %s is a member with base_role=%q additional_roles=%v",
		email, before.BaseRole, before.AdditionalRoles)

	// Read-only verification: if membership or role changed there is no API
	// call that puts it back, so say so loudly.
	t.Cleanup(func() {
		if err := checkMemberUnchanged(t, email, before); err != nil {
			t.Errorf("RESTORE IMPOSSIBLE: %s. Re-invite or re-role this member by hand.", err)
			return
		}
		t.Logf("VERIFIED: %s is still a member with the same roles", email)
	})

	const addr = "anyscale_organization_user.test"
	config := func(reinvite bool) string {
		return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_organization_user" "test" {
  email               = %q
  reinvite_if_expired = %t
}
`, email, reinvite)
	}

	var identityID string
	resource.Test(t, resource.TestCase{
		ErrorCheck:               orgModeErrorCheck(t),
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			return checkMemberUnchanged(t, email, before)
		},
		Steps: []resource.TestStep{
			{
				Config: config(false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", email),
					resource.TestCheckResourceAttr(addr, "email", email),
					resource.TestCheckResourceAttrSet(addr, "identity_id"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
					resource.TestCheckResourceAttr(addr, "user_id", before.UserID),
					CaptureResourceAttr(addr, "identity_id", &identityID),
					func(*terraform.State) error { return checkMemberUnchanged(t, email, before) },
				),
			},
			{
				Config: config(true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "reinvite_if_expired", "true"),
					VerifyResourceAttrUnchanged(addr, "identity_id", &identityID),
					func(*terraform.State) error { return checkMemberUnchanged(t, email, before) },
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     email,
				ImportStateVerify: true,
				// reinvite_if_expired is provider-side policy with no API
				// counterpart, so import sets its default (false) while state
				// holds true. created_at is write-once in state because the API
				// has returned different values across reads for one member.
				ImportStateVerifyIgnore: []string{"reinvite_if_expired", "created_at"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("import produced %d states, want 1", len(states))
					}
					a := states[0].Attributes
					if a["reinvite_if_expired"] != "false" {
						return fmt.Errorf("imported reinvite_if_expired = %q, want the default \"false\"", a["reinvite_if_expired"])
					}
					if a["created_at"] == "" {
						return fmt.Errorf("import did not recover created_at for a member")
					}
					if a["identity_id"] != identityID {
						return fmt.Errorf("imported identity_id = %q, want %q", a["identity_id"], identityID)
					}
					return nil
				},
			},
		},
	})
}

// checkMemberUnchanged reads the member from the real API and reports eviction
// or a changed role.
func checkMemberUnchanged(t *testing.T, email string, before liveOrgMember) error {
	t.Helper()
	now, found := readLiveOrgRole(t, email)
	if !found {
		return fmt.Errorf("%s is no longer a member of the organization: anyscale_organization_user must never evict", email)
	}
	if !strings.EqualFold(now.BaseRole, before.BaseRole) {
		return fmt.Errorf("%s base_role changed from %q to %q", email, before.BaseRole, now.BaseRole)
	}
	if len(now.AdditionalRoles) != len(before.AdditionalRoles) {
		return fmt.Errorf("%s additional_roles changed from %v to %v", email, before.AdditionalRoles, now.AdditionalRoles)
	}
	for _, r := range before.AdditionalRoles {
		if !containsFold(now.AdditionalRoles, r) {
			return fmt.Errorf("%s additional_roles changed from %v to %v", email, before.AdditionalRoles, now.AdditionalRoles)
		}
	}
	return nil
}
