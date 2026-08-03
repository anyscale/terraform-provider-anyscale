package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real-infra coverage for anyscale_organization_user_role.
//
// WHY THESE CANNOT BE MOCKS. Every other test for this resource is mock-backed,
// and that is right for CI - but a mock cannot answer either question here,
// because both are questions about WHAT THE BACKEND DOES, not about what the
// provider sends. Asserting against a mock would be asserting our own guess:
//
//	R5  does the legacy permission_level PUT silently wipe deny roles that were
//	    set through the roles path? The whole routing design in this resource -
//	    already merged to main - assumes it does not. If it does, the design is
//	    wrong, and no amount of mock coverage would reveal that.
//	V8  does a CHANGING deny_roles write actually persist, or does the second
//	    write silently no-op? A mock that echoes back whatever we sent proves
//	    only that we sent it.
//
// Both therefore READ BACK FROM THE REAL API and assert on what the backend
// actually stored, not on what we asked it to store.
//
// SAFETY. These mutate a real person's real organization role. They are gated on
// ANYSCALE_TEST_USER_EMAIL and skip cleanly when it is unset, and they must only
// ever be pointed at a disposable identity. Two reasons that is not negotiable:
// a real colleague would have their access changed underneath them, and the
// backend refuses self-modification outright (organization_collaborators_service.py:512
// "You cannot modify your own permission level"), so the authenticated identity
// cannot be used even if you were willing.
//
// Unlike the organization_user tests, these do NOT evict anyone: this resource's
// Destroy writes a role back, it does not remove a member. The final check in
// each test asserts exactly that - the member is still there afterwards.

// requireRealInfraTestUser returns the disposable test identity's email, or
// skips. Kept separate from the org_user tests' identity-ID gate because this
// resource is email-keyed.
func requireRealInfraTestUser(t *testing.T) string {
	t.Helper()

	email := strings.TrimSpace(os.Getenv("ANYSCALE_TEST_USER_EMAIL"))
	if email == "" {
		t.Skip("ANYSCALE_TEST_USER_EMAIL not set - skipping real-infra organization role test. " +
			"Point it at a DISPOSABLE org member (an accepted invitation to a throwaway address you " +
			"control), never a colleague: these tests change that person's real organization role.")
	}
	t.Logf("WARNING: this test CHANGES THE REAL ORGANIZATION ROLE of %s against the live API. "+
		"It does not evict them - this resource's destroy writes a role back rather than removing a "+
		"member - but the role changes are real and are not rolled back beyond what destroy restores.",
		email)
	return email
}

// liveOrgMember is the shape we care about from
// GET /api/v2/organization_collaborators. Declared locally rather than reusing
// the provider's model because the provider's lookup helper is unexported - and
// because a test that re-derives the shape from the wire is a slightly stronger
// check than one that shares the parsing code it is validating.
type liveOrgMember struct {
	Email           string   `json:"email"`
	BaseRole        string   `json:"base_role"`
	AdditionalRoles []string `json:"additional_roles"`
}

// readLiveOrgRole fetches the member fresh from the real API, bypassing
// Terraform state entirely. Reading through state would only tell us what the
// provider believes; the entire point of these tests is what the BACKEND holds.
//
// Returns found=false when the member is absent, so callers can distinguish
// "still there with the wrong roles" from "gone entirely" - a distinction that
// matters because eviction and a bad write are very different failures.
func readLiveOrgRole(t *testing.T, email string) (member liveOrgMember, found bool) {
	t.Helper()

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("could not build an API client: %s", err)
	}

	type listResp struct {
		Results []liveOrgMember `json:"results"`
	}

	params := url.Values{"email": []string{email}, "count": []string{"50"}}
	resp, err := client.DoRequest(context.Background(),
		"GET", "/api/v2/organization_collaborators?"+params.Encode(), nil)
	if err != nil {
		t.Fatalf("could not read %s back from the real API: %s", email, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reading %s back from the real API returned HTTP %d", email, resp.StatusCode)
	}

	var parsed listResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("could not parse the collaborator list for %s: %s", email, err)
	}

	// EqualFold, matching the provider: the server-side email filter is a
	// narrowing hint, not a guaranteed exact match, and the backend stores
	// lower-cased while the env var may be typed in any casing.
	for _, m := range parsed.Results {
		if strings.EqualFold(m.Email, email) {
			return m, true
		}
	}
	return liveOrgMember{}, false
}

// mustReadLiveOrgRole is readLiveOrgRole for the callers that treat absence as a
// fatal test error rather than a result.
func mustReadLiveOrgRole(t *testing.T, email string) liveOrgMember {
	t.Helper()
	m, found := readLiveOrgRole(t, email)
	if !found {
		t.Fatalf("%s is not a member of the organization - point ANYSCALE_TEST_USER_EMAIL at an "+
			"ACCEPTED member, not a pending invitation", email)
	}
	return m
}

// realInfraProviderBlock is the counterpart to testAccProviderBlock: an EMPTY
// provider block, so the provider resolves the real API URL and a real token
// through its normal precedence (token argument, then ANYSCALE_CLI_TOKEN, then
// ~/.anyscale/credentials.json).
//
// Deliberately empty rather than passing a token in: writing the credential into
// generated HCL would put it in Terraform's plan/state files and in any test
// output that echoes the config. Letting the provider resolve it keeps it out of
// all three, and exercises the same resolution path a real user gets.
func realInfraProviderBlock() string {
	return `
provider "anyscale" {}
`
}

// TestAccOrganizationUserRoleResourceR5LegacyPutPreservesDenyRoles is the R5
// gate: it answers whether the legacy permission_level write path clobbers deny
// roles set through the roles path.
//
// This is the check that can still invalidate the routing design in a resource
// that is ALREADY MERGED to main, which is why it blocks the tag rather than the
// merge. If the deny role does not survive, the design's central assumption -
// that the two write paths are independent - is false.
func TestAccOrganizationUserRoleResourceR5LegacyPutPreservesDenyRoles(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	email := requireRealInfraTestUser(t)

	const addr = "anyscale_organization_user_role.realinfra"
	const denyRole = "image_reader"

	// Step 1 declares deny_roles, which routes through the roles endpoint.
	withDenyRole := realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_organization_user_role" "realinfra" {
  email      = %[1]q
  base_role  = "collaborator"
  deny_roles = [%[2]q]
}
`, email, denyRole)

	// Step 2 OMITS deny_roles, which is what routes the write down the legacy
	// permission_level path. The question is whether that path leaves the
	// previously-set deny role alone.
	omittedDenyRoles := realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_organization_user_role" "realinfra" {
  email     = %[1]q
  base_role = "collaborator"
}
`, email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withDenyRole,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
					resource.TestCheckResourceAttr(addr, "deny_roles.0", denyRole),
					// Confirm against the BACKEND, not state, that the write landed.
					func(*terraform.State) error {
						live := mustReadLiveOrgRole(t, email).AdditionalRoles
						if !containsFold(live, denyRole) {
							return fmt.Errorf("the roles-path write did not persist: backend reports deny roles %v, expected to contain %q", live, denyRole)
						}
						return nil
					},
				),
			},
			{
				Config: omittedDenyRoles,
				Check: resource.ComposeAggregateTestCheckFunc(
					// THE R5 ASSERTION. Read fresh from the API after the legacy
					// permission_level PUT and require the deny role to have
					// survived it.
					func(*terraform.State) error {
						live := mustReadLiveOrgRole(t, email).AdditionalRoles
						if !containsFold(live, denyRole) {
							return fmt.Errorf("R5 FAILED - the legacy permission_level write CLOBBERED an existing deny role. "+
								"Backend now reports deny roles %v; %q was set through the roles path and is gone. "+
								"The routing design in anyscale_organization_user_role assumes these two write paths are "+
								"independent, and this proves they are not", live, denyRole)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccOrganizationUserRoleResourceChangingDenyRoleWritePersists is the V8
// residual: prove a CHANGING deny_roles value actually reaches the backend.
//
// The failure mode this exists for is a write that silently no-ops on update -
// state would look correct, the plan would be clean, and the person's real
// permissions would not have moved. Only reading the backend after the second
// apply distinguishes that from a working write.
func TestAccOrganizationUserRoleResourceChangingDenyRoleWritePersists(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	email := requireRealInfraTestUser(t)

	const addr = "anyscale_organization_user_role.realinfra"
	const firstDeny = "image_reader"
	const secondDeny = "cloud_user"

	config := func(denyRole string) string {
		return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_organization_user_role" "realinfra" {
  email      = %[1]q
  base_role  = "collaborator"
  deny_roles = [%[2]q]
}
`, email, denyRole)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// Destroy writes the role back rather than removing the member. Assert
		// the member is STILL THERE afterwards - this resource must never evict.
		CheckDestroy: func(*terraform.State) error {
			if _, found := readLiveOrgRole(t, email); !found {
				return fmt.Errorf("destroy appears to have EVICTED %s from the organization. "+
					"anyscale_organization_user_role must only write a role back, never remove a member", email)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config(firstDeny),
				Check: func(*terraform.State) error {
					live := mustReadLiveOrgRole(t, email).AdditionalRoles
					if !containsFold(live, firstDeny) {
						return fmt.Errorf("first write did not persist: backend reports %v, expected %q", live, firstDeny)
					}
					return nil
				},
			},
			{
				Config: config(secondDeny),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "deny_roles.0", secondDeny),
					// THE V8 ASSERTION. The CHANGED value must be what the backend
					// holds - not the original, and not both.
					func(*terraform.State) error {
						live := mustReadLiveOrgRole(t, email).AdditionalRoles
						if !containsFold(live, secondDeny) {
							return fmt.Errorf("V8 FAILED - the changing deny_roles write did not persist. "+
								"Backend still reports %v after applying %q; the update appears to have silently no-opped",
								live, secondDeny)
						}
						if containsFold(live, firstDeny) {
							return fmt.Errorf("V8 FAILED - the write ACCUMULATED rather than replaced. "+
								"Backend reports %v, which still contains the superseded %q alongside %q",
								live, firstDeny, secondDeny)
						}
						return nil
					},
				),
			},
		},
	})
}

func containsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
