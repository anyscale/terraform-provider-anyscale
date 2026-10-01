package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Real-API membership coverage for anyscale_user_group_members. It adds and
// removes ANYSCALE_TEST_USER_EMAIL from a group the test creates. The group
// carries no role, so membership grants nothing, but it is still a write about
// a real person, so the test is opt-in and checks the target org.
//
// Run: TF_ACC=1 ANYSCALE_TEST_USER_EMAIL=<disposable member> ANYSCALE_TEST_ORG_NAME=<org> go test ./internal/acctest -run '^TestAccUserGroupMembersResource_RealAPI$' -v -count=1

// liveOrgUserID resolves an email to the member's usr_ ID through the same
// listing the provider uses.
func liveOrgUserID(t *testing.T, email string) string {
	t.Helper()
	status, body := userGroupAPI(t, "GET", "/api/v2/organization_collaborators/?count=50&email="+url.QueryEscape(email), nil)
	if status != http.StatusOK {
		t.Fatalf("list organization collaborators: status %d: %s", status, truncateBody(string(body), 256))
	}
	var resp struct {
		Results []struct {
			Email  string  `json:"email"`
			UserID *string `json:"user_id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse collaborators: %v", err)
	}
	for _, c := range resp.Results {
		if strings.EqualFold(c.Email, email) && c.UserID != nil {
			return *c.UserID
		}
	}
	t.Fatalf("%s is not an organization member", email)
	return ""
}

// liveGroupMemberIDs returns a group's member usr_ IDs from the API, and
// whether the group appears in the listing at all.
func liveGroupMemberIDs(t *testing.T, groupID string) ([]string, bool) {
	t.Helper()
	status, body := userGroupAPI(t, "GET", "/api/v2/user_groups/memberships/list", nil)
	if status != http.StatusOK {
		t.Fatalf("list memberships: status %d: %s", status, truncateBody(string(body), 256))
	}
	var resp struct {
		Result struct {
			Groups []struct {
				GroupID string `json:"group_id"`
				Members []struct {
					UserID string `json:"user_id"`
				} `json:"members"`
			} `json:"groups"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse memberships: %v", err)
	}
	for _, g := range resp.Result.Groups {
		if g.GroupID == groupID {
			ids := make([]string, 0, len(g.Members))
			for _, m := range g.Members {
				ids = append(ids, m.UserID)
			}
			return ids, true
		}
	}
	return nil, false
}

func testAccCheckLiveGroupMembers(t *testing.T, want ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		groupID := s.RootModule().Resources["anyscale_user_group.test"].Primary.ID
		got, found := liveGroupMemberIDs(t, groupID)
		if !found {
			return fmt.Errorf("group %s is missing from the memberships listing", groupID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("live members of %s = %v, want %v", groupID, got, want)
		}
		return nil
	}
}

func testAccUserGroupMembersConfig(name string, members ...string) string {
	quoted := make([]string, len(members))
	for i, e := range members {
		quoted[i] = fmt.Sprintf("%q", e)
	}
	return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_user_group" "test" {
  name = %q
}

resource "anyscale_user_group_members" "test" {
  group_id = anyscale_user_group.test.id
  members  = [%s]
}
`, name, strings.Join(quoted, ", "))
}

// TestAccUserGroupMembersResource_RealAPI covers add with a differently-cased
// email, import, an out-of-band removal re-added by apply, emptying the set,
// and destroy. Every membership claim is checked against the API, not state.
func TestAccUserGroupMembersResource_RealAPI(t *testing.T) {
	email := requireRealInfraTestUser(t)
	userID := liveOrgUserID(t, email)
	// The backend stores emails lowercased; an uppercased config exercises the
	// case-insensitive match and the kept spelling.
	configEmail := strings.ToUpper(email)
	name := UniqueName(t, "ug-members")
	var groupID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_user_group", "/api/v2/user_groups/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupMembersConfig(name, configEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckLiveGroupMembers(t, userID),
					func(s *terraform.State) error {
						groupID = s.RootModule().Resources["anyscale_user_group.test"].Primary.ID
						return nil
					},
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("anyscale_user_group_members.test", tfjsonpath.New("members"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(configEmail)})),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:            "anyscale_user_group_members.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"members"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance, got %d", len(states))
					}
					a := states[0].Attributes
					if a["members.#"] != "1" || a["members.0"] != strings.ToLower(email) {
						return fmt.Errorf("import recovered members %v, want the backend's lowercase %q", a, strings.ToLower(email))
					}
					return nil
				},
			},
			{
				PreConfig: func() {
					if status, body := userGroupAPI(t, "DELETE", "/api/v2/user_groups/"+groupID+"/members", map[string][]string{"user_ids": {userID}}); status != http.StatusNoContent {
						t.Fatalf("out-of-band member removal: status %d: %s", status, truncateBody(string(body), 256))
					}
				},
				Config: testAccUserGroupMembersConfig(name, configEmail),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_user_group_members.test", plancheck.ResourceActionUpdate)},
				},
				Check: testAccCheckLiveGroupMembers(t, userID),
			},
			{
				Config: testAccUserGroupMembersConfig(name),
				Check:  testAccCheckLiveGroupMembers(t),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Destroy runs with the member present, so it must remove them before
				// deleting the group.
				Config: testAccUserGroupMembersConfig(name, email),
				Check:  testAccCheckLiveGroupMembers(t, userID),
			},
		},
	})
}
