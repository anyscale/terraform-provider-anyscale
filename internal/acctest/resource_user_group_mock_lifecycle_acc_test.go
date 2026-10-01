package acctest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Lifecycle tests for the user group resources and data sources, run through
// resource.Test against userGroupMockServer. They need no credentials and run
// in the ordinary acctest shards. Real-API coverage is in
// resource_user_group_acc_test.go and resource_user_group_members_acc_test.go.

var (
	ugUserA = userGroupMockUser{UserID: "usr_aaaaaaaaaaaaaaaaaaaaaaaaaa", IdentityID: "ide_aaaaaaaaaaaaaaaaaaaaaaaaaa", Email: "alice@example.com", Name: "Alice"}
	ugUserB = userGroupMockUser{UserID: "usr_bbbbbbbbbbbbbbbbbbbbbbbbbb", IdentityID: "ide_bbbbbbbbbbbbbbbbbbbbbbbbbb", Email: "bob@example.com", Name: "Bob"}
	ugUserC = userGroupMockUser{UserID: "usr_cccccccccccccccccccccccccc", IdentityID: "ide_cccccccccccccccccccccccccc", Email: "carol@example.com", Name: "Carol"}
)

func ugMockConfig(m *userGroupMockServer, groupName string, members ...string) string {
	cfg := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_user_group" "test" {
  name = %q
}
`, groupName)
	if members != nil {
		quoted := make([]string, len(members))
		for i, e := range members {
			quoted[i] = fmt.Sprintf("%q", e)
		}
		cfg += fmt.Sprintf(`
resource "anyscale_user_group_members" "test" {
  group_id = anyscale_user_group.test.id
  members  = [%s]
}
`, strings.Join(quoted, ", "))
	}
	return cfg
}

// ugExpectMembers asserts the mock's actual membership for the group in state.
// It checks the backend, not state, so an apply that only planned a change
// cannot pass it.
func ugExpectMembers(m *userGroupMockServer, want ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["anyscale_user_group.test"]
		if !ok {
			return fmt.Errorf("anyscale_user_group.test not in state")
		}
		got, exists := m.memberIDs(rs.Primary.ID)
		if !exists {
			return fmt.Errorf("group %s does not exist in the backend", rs.Primary.ID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("backend members of %s = %v, want %v", rs.Primary.ID, got, want)
		}
		return nil
	}
}

// ugExpectNoWrites asserts the step's apply sent no mutating request.
func ugExpectNoWrites(m *userGroupMockServer) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if w := m.takeWrites(); len(w) != 0 {
			return fmt.Errorf("expected no API writes, got %q", w)
		}
		return nil
	}
}

func ugClearWrites(m *userGroupMockServer) resource.TestCheckFunc {
	return func(*terraform.State) error {
		m.takeWrites()
		return nil
	}
}

// TestAccUserGroupMembersResource_MockLifecycle walks create, a case-only
// edit, out-of-band drift in both directions, emptying the set, and destroy.
func TestAccUserGroupMembersResource_MockLifecycle(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB, ugUserC)
	const name = "tfacc-ug-mock-life"
	var groupID string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if m.groupCount() != 0 {
				return fmt.Errorf("%d group(s) left in the backend after destroy", m.groupCount())
			}
			// Destroying the group must remove its members first: the backend's
			// group delete leaves the access they held through it behind.
			w := m.takeWrites()
			var memberDel, groupDel = -1, -1
			for i, e := range w {
				if strings.HasPrefix(e, "DELETE /api/v2/user_groups/"+groupID+"/members") {
					memberDel = i
				}
				if e == "DELETE /api/v2/user_groups/"+groupID {
					groupDel = i
				}
			}
			if memberDel < 0 || groupDel < 0 || memberDel > groupDel {
				return fmt.Errorf("destroy must remove members before deleting the group; writes: %q", w)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// Mixed case in config; the backend stores lowercase.
				Config: ugMockConfig(m, name, "Alice@Example.com", "bob@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					ugExpectMembers(m, ugUserA.UserID, ugUserB.UserID),
					func(s *terraform.State) error {
						groupID = s.RootModule().Resources["anyscale_user_group.test"].Primary.ID
						return nil
					},
					ugClearWrites(m),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("anyscale_user_group_members.test", tfjsonpath.New("members"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("Alice@Example.com"), knownvalue.StringExact("bob@example.com")})),
					statecheck.CompareValuePairs("anyscale_user_group_members.test", tfjsonpath.New("id"),
						"anyscale_user_group.test", tfjsonpath.New("id"), compare.ValuesSame()),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A case-only edit names the same users: an in-place update that
				// writes nothing.
				Config: ugMockConfig(m, name, "ALICE@example.com", "bob@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_user_group_members.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					ugExpectNoWrites(m),
					ugExpectMembers(m, ugUserA.UserID, ugUserB.UserID),
				),
			},
			{
				// Out-of-band add: authoritative membership removes carol, and the
				// backend must actually lose her, not just the plan.
				PreConfig: func() { m.setMember(groupID, ugUserC.UserID, true) },
				Config:    ugMockConfig(m, name, "ALICE@example.com", "bob@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_user_group_members.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					ugExpectMembers(m, ugUserA.UserID, ugUserB.UserID),
					ugClearWrites(m),
				),
			},
			{
				// Out-of-band removal is re-added.
				PreConfig: func() { m.setMember(groupID, ugUserB.UserID, false) },
				Config:    ugMockConfig(m, name, "ALICE@example.com", "bob@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_user_group_members.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					ugExpectMembers(m, ugUserA.UserID, ugUserB.UserID),
					ugClearWrites(m),
				),
			},
			{
				// An empty set is allowed and empties the group.
				Config: ugMockConfig(m, name, []string{}...),
				Check:  ugClearWrites(m),
			},
			{
				Config: ugMockConfig(m, name, "alice@example.com"),
				Check:  resource.ComposeAggregateTestCheckFunc(ugExpectMembers(m, ugUserA.UserID), ugClearWrites(m)),
			},
			{
				// Destroying only the members resource empties the group and keeps it.
				Config: ugMockConfig(m, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					ugExpectMembers(m),
					func(s *terraform.State) error {
						if got := s.RootModule().Resources["anyscale_user_group.test"].Primary.ID; got != groupID {
							return fmt.Errorf("group ID changed from %s to %s", groupID, got)
						}
						return nil
					},
				),
			},
			{
				// Re-add a member so the final destroy has members to remove.
				Config: ugMockConfig(m, name, "alice@example.com"),
				Check:  ugClearWrites(m),
			},
		},
	})
}

// TestAccUserGroupResource_MockDestroyRemovesMembersFirst: destroying a group
// that Terraform manages without a members resource still removes the members
// someone added outside Terraform before deleting it. The backend's group
// delete would otherwise leave the access they held through the group behind.
func TestAccUserGroupResource_MockDestroyRemovesMembersFirst(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB)
	const name = "tfacc-ug-mock-destroy"
	var groupID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			w := m.takeWrites()
			want := []string{
				`DELETE /api/v2/user_groups/` + groupID + `/members {"user_ids":["` + ugUserA.UserID + `","` + ugUserB.UserID + `"]}`,
				`DELETE /api/v2/user_groups/` + groupID,
			}
			if strings.Join(w, "\n") != strings.Join(want, "\n") {
				return fmt.Errorf("destroy writes:\n got  %q\n want %q", w, want)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: ugMockConfig(m, name, nil...),
				Check: func(s *terraform.State) error {
					groupID = s.RootModule().Resources["anyscale_user_group.test"].Primary.ID
					m.setMember(groupID, ugUserA.UserID, true)
					m.setMember(groupID, ugUserB.UserID, true)
					m.takeWrites()
					return nil
				},
			},
		},
	})
}

// TestAccUserGroupMembersResource_MockEmptySet: members = [] is a valid,
// distinct configuration from omitting the resource.
func TestAccUserGroupMembersResource_MockEmptySet(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: ugMockConfig(m, "tfacc-ug-mock-empty", []string{}...),
				Check:  ugExpectMembers(m),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("anyscale_user_group_members.test", tfjsonpath.New("members"), knownvalue.SetSizeExact(0)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccUserGroupMembersResource_MockImport is Test A of the import pair:
// what import actually recovers, asserted inside the import step. Import has
// no config spelling to prefer, so it recovers the backend's lowercase emails;
// that divergence from the mixed-case config is the one ignored field, and it
// is asserted explicitly rather than excluded silently.
func TestAccUserGroupMembersResource_MockImport(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ugMockConfig(m, "tfacc-ug-mock-import", "Alice@Example.com", "bob@example.com")},
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
					if a["group_id"] != a["id"] || !strings.HasPrefix(a["id"], "ug_") {
						return fmt.Errorf("imported id=%q group_id=%q, want equal ug_ IDs", a["id"], a["group_id"])
					}
					if a["members.#"] != "2" {
						return fmt.Errorf("imported members.# = %q, want 2", a["members.#"])
					}
					got := map[string]bool{}
					for k, v := range a {
						if strings.HasPrefix(k, "members.") && k != "members.#" {
							got[v] = true
						}
					}
					if !got["alice@example.com"] || !got["bob@example.com"] {
						return fmt.Errorf("imported members = %v, want the backend spelling alice@example.com, bob@example.com", got)
					}
					return nil
				},
			},
			{
				ResourceName:      "anyscale_user_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccUserGroupMembersResource_MockImportedShapePlansClean is Test B of the
// import pair: plan against the state shape import produces (lowercase
// emails) with a mixed-case config. It must be an in-place update that writes
// nothing, never a replacement.
func TestAccUserGroupMembersResource_MockImportedShapePlansClean(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ugMockConfig(m, "tfacc-ug-mock-shape", "alice@example.com"), Check: ugClearWrites(m)},
			{
				Config: ugMockConfig(m, "tfacc-ug-mock-shape", "Alice@Example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_user_group_members.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: ugExpectNoWrites(m),
			},
		},
	})
}

// TestAccUserGroupMembersResource_MockGroupDeletedOutOfBand: when the group
// disappears, both resources leave state on refresh and are planned for
// creation, rather than the members resource failing the refresh.
func TestAccUserGroupMembersResource_MockGroupDeletedOutOfBand(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	const name = "tfacc-ug-mock-oob"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ugMockConfig(m, name, "alice@example.com")},
			{
				PreConfig: func() { m.deleteGroup(m.groupIDByName(name)) },
				Config:    ugMockConfig(m, name, "alice@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_user_group.test", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction("anyscale_user_group_members.test", plancheck.ResourceActionCreate),
					},
				},
				Check: ugExpectMembers(m, ugUserA.UserID),
			},
		},
	})
}

// TestAccUserGroupMembersResource_MockUnresolvableEmails: an email that is not
// an org member fails the apply naming every such email, and nothing is
// written.
func TestAccUserGroupMembersResource_MockUnresolvableEmails(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      ugMockConfig(m, "tfacc-ug-mock-unres", "alice@example.com", "nobody@example.com", "ghost@example.com"),
				ExpectError: regexp.MustCompile(`(?s)(ghost@example\.com.*nobody@example\.com|nobody@example\.com.*ghost@example\.com)`),
				Check:       ugExpectMembers(m),
			},
		},
	})
	for _, w := range m.takeWrites() {
		if strings.Contains(w, "/members") {
			t.Fatalf("a member write was sent despite unresolvable emails: %q", w)
		}
	}
}

// TestAccUserGroupMembersResource_MockDepartedMember: a member who left the
// org drops out of the backend's listing, so the next plan proposes re-adding
// them and the apply fails clearly instead of claiming success.
func TestAccUserGroupMembersResource_MockDepartedMember(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB)
	const name = "tfacc-ug-mock-departed"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ugMockConfig(m, name, "alice@example.com", "bob@example.com")},
			{
				PreConfig:          func() { m.removeUserFromOrg(ugUserB.UserID) },
				Config:             ugMockConfig(m, name, "alice@example.com", "bob@example.com"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config:      ugMockConfig(m, name, "alice@example.com", "bob@example.com"),
				ExpectError: regexp.MustCompile(`bob@example\.com`),
			},
			{
				// Removing them from config converges.
				Config: ugMockConfig(m, name, "alice@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccUserGroupMembersResource_MockDuplicateCaseRejected: two spellings of
// one address resolve to one member and could never converge.
func TestAccUserGroupMembersResource_MockDuplicateCaseRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      ugMockConfig(m, "tfacc-ug-mock-dupcase", "alice@example.com", "Alice@example.com"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?i)alice@example\.com`),
			},
		},
	})
	if m.groupCount() != 0 {
		t.Fatalf("a plan-time rejection created %d group(s)", m.groupCount())
	}
}

// TestAccUserGroupResource_MockSCIMRejected: a directory-synced group cannot
// be imported, and its membership cannot be managed. The backend does not
// guard DELETE for scim groups, so the import refusal is what keeps a destroy
// from deleting one.
func TestAccUserGroupResource_MockSCIMRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	scim := "scim"
	scimID := m.seedGroup("okta-admins", &scim, ugUserA.UserID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: m.providerBlock() + `
resource "anyscale_user_group" "test" {
  name = "okta-admins"
}
`,
				ResourceName:  "anyscale_user_group.test",
				ImportState:   true,
				ImportStateId: scimID,
				ExpectError:   regexp.MustCompile(`(?i)identity\s+provider`),
			},
			{
				Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_user_group_members" "test" {
  group_id = %q
  members  = ["alice@example.com"]
}
`, scimID),
				ExpectError: regexp.MustCompile(`(?i)identity\s+provider`),
			},
		},
	})
	if got, ok := m.memberIDs(scimID); !ok || len(got) != 1 {
		t.Fatalf("scim group was modified: exists=%v members=%v", ok, got)
	}
	for _, w := range m.takeWrites() {
		if strings.Contains(w, scimID) {
			t.Fatalf("a write was sent to the scim group: %q", w)
		}
	}
}

// TestAccUserGroupResource_MockDuplicateName: a 409 from create surfaces the
// existing group's ID in a ready-to-run import command, and create is sent
// exactly once.
func TestAccUserGroupResource_MockDuplicateName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	user := "user"
	existing := m.seedGroup("tfacc-ug-mock-dup", &user)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      ugMockConfig(m, "tfacc-ug-mock-dup", nil...),
				ExpectError: regexp.MustCompile(`(?s)already exists.*terraform import <resource address> ` + regexp.QuoteMeta(existing)),
			},
		},
	})
	creates := 0
	for _, w := range m.takeWrites() {
		if strings.HasPrefix(w, "POST /api/v2/user_groups/ ") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("create was sent %d times, want exactly 1 (no retry on 409)", creates)
	}
}

// TestAccUserGroupResource_MockNameValidation: the server strips surrounding
// whitespace, so a padded name would never converge; it is rejected at plan
// time along with an empty or over-long name. A clean name is the positive
// control.
func TestAccUserGroupResource_MockNameValidation(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	for _, tc := range []struct {
		name    string
		wantErr bool
	}{
		{" tfacc-ug-lead", true},
		{"tfacc-ug-trail ", true},
		{"\ttfacc-ug-tab", true},
		{"", true},
		{strings.Repeat("a", 256), true},
		{strings.Repeat("a", 255), false},
	} {
		step := resource.TestStep{Config: ugMockConfig(m, tc.name, nil...), PlanOnly: true, ExpectNonEmptyPlan: true}
		if tc.wantErr {
			step.ExpectError = regexp.MustCompile(`(?i)whitespace|length`)
		}
		resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: ProtoV6ProviderFactories, Steps: []resource.TestStep{step}})
	}
	if m.groupCount() != 0 {
		t.Fatalf("plan-only steps created %d group(s)", m.groupCount())
	}
}

// TestAccUserGroupResource_MockRenameInPlace: a rename keeps the ID.
func TestAccUserGroupResource_MockRenameInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ugMockConfig(m, "tfacc-ug-mock-a", nil...)},
			{
				Config: ugMockConfig(m, "tfacc-ug-mock-b", nil...),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_user_group.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(s *terraform.State) error {
					id := s.RootModule().Resources["anyscale_user_group.test"].Primary.ID
					if m.groupIDByName("tfacc-ug-mock-b") != id || m.groupCount() != 1 {
						return fmt.Errorf("rename did not update group %s in place", id)
					}
					return nil
				},
			},
		},
	})
}

// TestAccUserGroupDataSource_Mock reads by name and by ID, including a scim
// group with an organization role and a legacy group with a null source.
func TestAccUserGroupDataSource_Mock(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB, ugUserC)
	scim := "scim"
	scimID := m.seedGroup("okta-admins", &scim, ugUserC.UserID, ugUserA.UserID, ugUserB.UserID)
	m.mu.Lock()
	m.groups[scimID].OrgPerms = map[string]any{"base_role": "collaborator", "additional_roles": []any{"image_reader"}}
	m.mu.Unlock()
	legacyID := m.seedGroup("tfacc-ug-mock-legacy", nil)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: m.providerBlock() + fmt.Sprintf(`
data "anyscale_user_group" "by_name" {
  name = "okta-admins"
}
data "anyscale_user_group" "by_id" {
  id = %q
}
data "anyscale_user_group" "legacy" {
  id = %q
}
`, scimID, legacyID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_name", tfjsonpath.New("id"), knownvalue.StringExact(scimID)),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_name", tfjsonpath.New("source"), knownvalue.StringExact("scim")),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_name", tfjsonpath.New("members"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{"user_id": knownvalue.StringExact(ugUserA.UserID), "email": knownvalue.StringExact("alice@example.com"), "name": knownvalue.StringExact("Alice")}),
						knownvalue.ObjectExact(map[string]knownvalue.Check{"user_id": knownvalue.StringExact(ugUserB.UserID), "email": knownvalue.StringExact("bob@example.com"), "name": knownvalue.StringExact("Bob")}),
						knownvalue.ObjectExact(map[string]knownvalue.Check{"user_id": knownvalue.StringExact(ugUserC.UserID), "email": knownvalue.StringExact("carol@example.com"), "name": knownvalue.StringExact("Carol")}),
					})),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_name", tfjsonpath.New("organization_role"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"base_role":        knownvalue.StringExact("collaborator"),
						"additional_roles": knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("image_reader")}),
					})),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_id", tfjsonpath.New("name"), knownvalue.StringExact("okta-admins")),
					statecheck.ExpectKnownValue("data.anyscale_user_group.legacy", tfjsonpath.New("source"), knownvalue.Null()),
					statecheck.ExpectKnownValue("data.anyscale_user_group.legacy", tfjsonpath.New("organization_role"), knownvalue.Null()),
					statecheck.ExpectKnownValue("data.anyscale_user_group.legacy", tfjsonpath.New("members"), knownvalue.ListSizeExact(0)),
				},
			},
		},
	})
}

// TestAccUserGroupDataSource_MockArguments: exactly one of id or name, and a
// miss is an error naming what was looked up.
func TestAccUserGroupDataSource_MockArguments(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	user := "user"
	id := m.seedGroup("tfacc-ug-mock-arg", &user)
	for _, tc := range []struct {
		body string
		want string
	}{
		{`id = "` + id + `"` + "\n  name = \"tfacc-ug-mock-arg\"", `(?i)exactly one|conflict|cannot be configured`},
		{``, `(?i)exactly one|at least one|one of`},
		{`name = "tfacc-ug-mock-missing"`, `tfacc-ug-mock-missing`},
		{`id = "ug_00000000000000000000000099"`, `ug_00000000000000000000000099`},
	} {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: ProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      m.providerBlock() + "\ndata \"anyscale_user_group\" \"test\" {\n  " + tc.body + "\n}\n",
				ExpectError: regexp.MustCompile(tc.want),
			}},
		})
	}
}

// TestAccUserGroupsDataSource_Mock: the list follows paging, drops the row
// an offset page repeats, and filters by source.
func TestAccUserGroupsDataSource_Mock(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	m.listPageSize = 2
	user, scim := "user", "scim"
	ids := []string{
		m.seedGroup("tfacc-ug-mock-l1", &user),
		m.seedGroup("tfacc-ug-mock-l2", &scim),
		m.seedGroup("tfacc-ug-mock-l3", nil),
		m.seedGroup("tfacc-ug-mock-l4", &user),
		m.seedGroup("tfacc-ug-mock-l5", &user),
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: m.providerBlock() + `
data "anyscale_user_groups" "all" {}
data "anyscale_user_groups" "scim" {
  source = "scim"
}
data "anyscale_user_groups" "user" {
  source = "user"
}
`,
				ConfigStateChecks: []statecheck.StateCheck{
					// Newest first, each exactly once.
					statecheck.ExpectKnownValue("data.anyscale_user_groups.all", tfjsonpath.New("groups"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"id": knownvalue.StringExact(ids[4])}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"id": knownvalue.StringExact(ids[3])}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"id": knownvalue.StringExact(ids[2]), "source": knownvalue.Null()}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"id": knownvalue.StringExact(ids[1])}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"id": knownvalue.StringExact(ids[0])}),
					})),
					statecheck.ExpectKnownValue("data.anyscale_user_groups.scim", tfjsonpath.New("groups"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{"id": knownvalue.StringExact(ids[1]), "name": knownvalue.StringExact("tfacc-ug-mock-l2"), "source": knownvalue.StringExact("scim")}),
					})),
					statecheck.ExpectKnownValue("data.anyscale_user_groups.user", tfjsonpath.New("groups"), knownvalue.ListSizeExact(3)),
				},
			},
		},
	})
}
