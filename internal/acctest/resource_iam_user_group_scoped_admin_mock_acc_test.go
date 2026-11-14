package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// A token that manages IAM on some resources but not on every resource a
// group's role bindings reach sees every group, but each of its group writes
// answers 404 with the same detail as a deleted group. Each test below drives
// one write call site with the 404-then-read-back contract:
//
//   - the group still reads fine -> "Cannot Manage User Group", an error, and
//     state is untouched - never dropped, never a silent success;
//   - the group is genuinely gone after the write -> the write's outcome is
//     the ordinary one for a deleted group (destroy succeeds, apply says the
//     group is not found).
//
// Real-API coverage of this scoped-admin shape is not feasible here: it needs
// a token whose IAM scope is narrower than the group's, which this test
// organization does not provision. The mock mirrors the backend's refusal
// detail and its read-still-works behavior.

var scopedAdminRefusal = regexp.MustCompile(`Cannot Manage User Group`)

// TestAccIAMUserGroupResource_MockScopedDeleteRefusedKeepsState drives
// Delete: the removal write (and, if it ever ran, the group delete) answers
// the scoped 404 while the group reads fine. The destroy must FAIL, the
// group must still exist with its members, and the resource must stay in
// state.
func TestAccIAMUserGroupResource_MockScopedDeleteRefusedKeepsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	const name = "tfacc-ug-scoped-del"
	config := ugMockConfig(m, name)
	empty := m.providerBlock() + `
# The group stays; only the resource declarations go away.
variable "none" {
  type    = string
  default = ""
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:   func() { m.setScoped(true) },
				Config:      empty,
				ExpectError: scopedAdminRefusal,
			},
			{
				// Still in state, still whole: removing the resource from the
				// config while scoped must not have dropped anything.
				PreConfig: func() {
					m.setScoped(false)
					if got := m.groupCount(); got != 1 {
						t.Errorf("group count after refused destroy = %d, want 1 (the scoped refusal must not destroy)", got)
					}
					if id := m.groupIDByName(name); id == "" {
						t.Error("the refused destroy must not delete the group")
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_iam_user_group.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

// TestAccIAMUserGroupResource_MockScopedRenameRefused drives Update: the
// PATCH answers the scoped 404. The rename must fail with the diagnostic and
// the group keep its old name.
func TestAccIAMUserGroupResource_MockScopedRenameRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	const name = "tfacc-ug-scoped-rename"
	renamed := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_user_group" "test" {
  name = %q
}
`, name+"-renamed")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_user_group" "test" {
  name = %q
}
`, name)},
			{
				PreConfig:   func() { m.setScoped(true) },
				Config:      renamed,
				ExpectError: scopedAdminRefusal,
			},
			{
				PreConfig: func() {
					m.setScoped(false)
					if got := m.groupIDByName(name); got == "" {
						t.Error("the refused rename must leave the group under its original name")
					}
					if got := m.groupIDByName(name + "-renamed"); got != "" {
						t.Error("the refused rename must not create or rename to the new name")
					}
				},
				Config: m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_user_group" "test" {
  name = %q
}
`, name),
			},
		},
	})
}

// TestAccIAMUserGroupMembersResource_MockScopedDeleteRefused drives the
// members resource's Delete: the member-removal write answers the scoped 404.
// The destroy must fail, keep all members, and keep both resources in state.
func TestAccIAMUserGroupMembersResource_MockScopedDeleteRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	const name = "tfacc-ug-scoped-members-del"
	groupOnly := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_user_group" "test" {
  name = %q
}
`, name)
	withMembers := ugMockConfig(m, name, "alice@example.com")
	_ = withMembers
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ugMockConfig(m, name, "alice@example.com")},
			{
				PreConfig:   func() { m.setScoped(true) },
				Config:      groupOnly,
				ExpectError: scopedAdminRefusal,
			},
			{
				PreConfig: func() {
					m.setScoped(false)
					id := m.groupIDByName(name)
					if id == "" {
						t.Fatal("group vanished after a refused members destroy")
					}
					if got, exists := m.memberIDs(id); !exists || len(got) != 1 {
						t.Errorf("members after refused destroy = %v, want the original one", got)
					}
				},
				Config: ugMockConfig(m, name, "alice@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_iam_user_group_members.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

// TestAccIAMUserGroupMembersResource_MockScopedMembershipChangeRefused drives
// applyMembers' add path: the scoped 404 on the member write must read as
// "Cannot Manage User Group", not as a missing group, and nothing converges
// until the scope is restored - although the mock's behavior here IS the
// backend's upsert semantics, so the error is what matters.
func TestAccIAMUserGroupMembersResource_MockScopedMembershipChangeRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB)
	const name = "tfacc-ug-scoped-members-add"
	original := ugMockConfig(m, name, "alice@example.com")
	updated := ugMockConfig(m, name, "alice@example.com", "bob@example.com")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: original},
			{
				PreConfig:   func() { m.setScoped(true) },
				Config:      updated,
				ExpectError: scopedAdminRefusal,
			},
			{
				PreConfig: func() {
					m.setScoped(false)
					id := m.groupIDByName(name)
					got, _ := m.memberIDs(id)
					if len(got) != 1 {
						t.Errorf("members after a refused add = %v, want only the original one", got)
					}
				},
				// After the scope is restored the same config converges.
				Config: updated,
				Check:  ugExpectMembers(m, ugUserA.UserID, ugUserB.UserID),
			},
		},
	})
}

// TestAccIAMUserGroupResource_MockGoneAfterWriteDestroySucceeds: the group
// really disappears - the write's 404 is confirmed by a read-back that also
// 404s - so the destroy succeeds exactly as it does today for a group deleted
// out of band, leaving nothing stranded in state.
func TestAccIAMUserGroupResource_MockGoneAfterWriteDestroySucceeds(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA)
	const name = "tfacc-ug-gone-after-write"
	config := ugMockConfig(m, name, "alice@example.com")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// The first write deletes the group behind the 404, so the
				// read-back sees it gone: the ordinary gone-path outcome.
				PreConfig: func() { m.setVanishOnWrite(true) },
				Config:    config,
				Destroy:   true,
			},
		},
	})
	if got := m.groupCount(); got != 0 {
		t.Errorf("group count after destroy = %d, want 0", got)
	}
}
