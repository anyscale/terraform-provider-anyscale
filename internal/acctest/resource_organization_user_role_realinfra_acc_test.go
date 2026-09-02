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

	// PRECONDITION 0 - SAY WHICH ORGANIZATION THIS IS ABOUT TO TOUCH.
	//
	// Not an assertion, a disclosure - and it is the check that was missing when
	// several full acceptance suites ran against an organization nobody intended,
	// creating real clouds that outlived the run. Nothing failed: the cloud
	// resolver falls back to a pinned name and then to auto-discovery, and BOTH
	// SUCCEED IN ANY ORG that happens to hold a healthy cloud. It worked, so it
	// looked correct.
	//
	// A token is scoped to exactly one organization and the provider has no
	// org-selection setting, so "which org am I in" is decided entirely by which
	// credential is in scope - ANYSCALE_CLI_TOKEN if set, otherwise
	// ~/.anyscale/credentials.json. Those are separate channels that fall through
	// to each other silently: an env var set to the WRONG token and an env var
	// that is UNSET both end up somewhere that works.
	//
	// Printing the org name costs one request and makes that invisible choice
	// visible at the top of the log, before anything is created. Set
	// ANYSCALE_TEST_ORG_NAME to turn this from a disclosure into a hard assertion.
	org := authenticatedOrgName(t)
	if org != "" {
		t.Logf("REAL-INFRA TARGET ORGANIZATION: %q", org)
	}

	// MANDATORY, not opt-in. An assertion that fires only when someone remembers
	// to set a variable is the same shape as the failure it guards against -
	// nobody set ANYSCALE_CLI_TOKEN either, and that IS what happened. So once
	// ANYSCALE_TEST_USER_EMAIL is set - i.e. once this test is actually going to
	// mutate a real person's role - naming the expected organization is required.
	want := strings.TrimSpace(os.Getenv("ANYSCALE_TEST_ORG_NAME"))
	if want == "" {
		t.Fatalf("ANYSCALE_TEST_USER_EMAIL is set but ANYSCALE_TEST_ORG_NAME is not. This test mutates a "+
			"real person's organization role, so the organization must be stated rather than inherited "+
			"from whichever credential happens to be in scope. These credentials currently authenticate "+
			"against %q - set ANYSCALE_TEST_ORG_NAME to that if it is what you intend.", org)
	}
	if org != "" && !strings.EqualFold(want, org) {
		t.Fatalf("ANYSCALE_TEST_ORG_NAME is %q but these credentials authenticate against %q. "+
			"Refusing to mutate roles in an unintended organization - fix the token in scope "+
			"(ANYSCALE_CLI_TOKEN takes precedence over ~/.anyscale/credentials.json).", want, org)
	}

	// PRECONDITION 1 - NEVER THE AUTHENTICATED IDENTITY.
	//
	// The backend refuses self-modification outright
	// (organization_collaborators_service.py:512, "You cannot modify your own
	// permission level"). Pointing this test at the token's own identity produces
	// a 403 that means "you cannot modify yourself" and looks exactly like a
	// failed gate - i.e. it would read as "the legacy write path DOES clobber deny
	// roles" when the write never happened at all. A false red on this gate is
	// worse than no result, because R5 exists to decide whether an already-merged
	// routing design is sound.
	//
	// This is a live hazard, not a theoretical one: the credentials in use during
	// development authenticate as a `+`-aliased address in the same domain as the
	// obvious fixture candidate, so the two are easy to conflate.
	if self := authenticatedEmail(t); self != "" && strings.EqualFold(self, email) {
		t.Fatalf("ANYSCALE_TEST_USER_EMAIL is the SAME identity these credentials authenticate as. " +
			"The backend refuses self-modification, so this test would 403 and the failure would look " +
			"like a real gate failure rather than a misconfiguration. Point it at a DIFFERENT member. " +
			"(Compared case-insensitively; `+`-aliases are distinct users but easy to confuse.)")
	}

	// PRECONDITION 2 - NEVER AN OWNER.
	//
	// These tests declare base_role = "collaborator". Running against an owner is a
	// real permission DOWNGRADE of a real owner account, and teardown writes back
	// whatever the config last declared rather than the role the member started
	// with - so the fixture would be left demoted with no restore.
	//
	// Checked rather than trusted: at the time of writing, every member of the
	// development test org was an owner (7 of 7), which is exactly the state where
	// an unguarded run does damage.
	member := mustReadLiveOrgRole(t, email)
	if strings.EqualFold(member.BaseRole, "owner") {
		t.Fatalf("ANYSCALE_TEST_USER_EMAIL points at an ORGANIZATION OWNER (base_role=%q). These tests "+
			"declare base_role=\"collaborator\", so running would DEMOTE a real owner, and teardown "+
			"restores the declared role rather than the original - leaving them demoted. Point this at a "+
			"collaborator-level throwaway member instead.", member.BaseRole)
	}

	return email
}

// authenticatedEmail returns the email of the identity the current credentials
// belong to, or "" if it cannot be determined.
//
// Returns empty rather than failing on error: an unreadable userinfo is not
// evidence that the fixture is safe, but it is also not evidence that it is
// unsafe, and turning a transient lookup failure into a hard stop would block a
// legitimate run. The owner check below is the load-bearing guard; this one
// catches the specific self-modification confusion.
func authenticatedEmail(t *testing.T) string {
	t.Helper()

	client, err := GetTestClient()
	if err != nil {
		return ""
	}
	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/userinfo", nil)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var wrapped struct {
		Result struct {
			Email string `json:"email"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		return ""
	}
	return wrapped.Result.Email
}

// authenticatedOrgName returns the name of the single organization the current
// credentials are scoped to, or "" if it cannot be determined.
//
// userinfo types organizations as a LIST, but the backend returns exactly one -
// the token-scoped org. Indexing [0] is correct here and the length guard is
// what keeps it from panicking rather than a hedge against multi-org tokens.
func authenticatedOrgName(t *testing.T) string {
	t.Helper()

	client, err := GetTestClient()
	if err != nil {
		return ""
	}
	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/userinfo", nil)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var wrapped struct {
		Result struct {
			Organizations []struct {
				Name string `json:"name"`
			} `json:"organizations"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		return ""
	}
	if len(wrapped.Result.Organizations) == 0 {
		return ""
	}
	return wrapped.Result.Organizations[0].Name
}

// liveOrgMember is the shape we care about from
// GET /api/v2/organization_collaborators. Declared locally rather than reusing
// the provider's model because the provider's lookup helper is unexported - and
// because a test that re-derives the shape from the wire is a slightly stronger
// check than one that shares the parsing code it is validating.
type liveOrgMember struct {
	Email           string   `json:"email"`
	UserID          string   `json:"user_id"`
	BaseRole        string   `json:"base_role"`
	AdditionalRoles []string `json:"additional_roles"`
}

// WHICH ENDPOINT REPORTS DENY ROLES TRUTHFULLY - this decides whether these
// tests mean anything, and the obvious choice is the wrong one.
//
// The LIST endpoint and the SINGULAR endpoint do not agree. This repo already
// documents it (see the mock in resource_organization_user_lifecycle_acc_test.go,
// whose handleList deliberately returns an EMPTY additional_roles while
// handleSingular returns the real one): the list formatter derives roles fresh
// from Postgres, the singular read derives them from SpiceDB-managed groups.
// A LIST-based check would therefore read [] for a member who genuinely holds a
// deny role - and R5 would report a CLOBBER THAT DID NOT HAPPEN, i.e. would
// declare an already-merged design broken on the strength of a false empty.
//
// So the authoritative read is the singular one, which is also what the provider
// itself uses (resource_organization_user.go:441).
//
// It is keyed on USER_ID, not identity_id - verified live: GET with an
// identity_id returns {"error":{"detail":"Not all users in {...} found. Found 0
// users; expected 1"}}, while the same member's user_id returns the record. That
// is the same dual-identifier split that disqualified user_id as this resource's
// key, showing up again on the read side.
//
// Both values are returned so a caller can surface a disagreement rather than
// silently trusting one; a divergence is itself a finding worth seeing.

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
	var fromList liveOrgMember
	for _, m := range parsed.Results {
		if strings.EqualFold(m.Email, email) {
			fromList = m
			found = true
			break
		}
	}
	if !found {
		return liveOrgMember{}, false
	}

	// Upgrade to the singular read, which is the one that reports deny roles
	// truthfully. See the comment on liveOrgMember for why the list value cannot
	// be trusted for additional_roles.
	if fromList.UserID == "" {
		t.Fatalf("%s has no user_id, so the authoritative singular read is unavailable - "+
			"some identity types genuinely have none. Point ANYSCALE_TEST_USER_EMAIL at a normal "+
			"human member rather than a service account", email)
	}

	singularResp, err := client.DoRequest(context.Background(),
		"GET", "/api/v2/organization_collaborators/"+fromList.UserID, nil)
	if err != nil {
		t.Fatalf("singular read for %s failed: %s", email, err)
	}
	defer func() { _ = singularResp.Body.Close() }()

	if singularResp.StatusCode != http.StatusOK {
		t.Fatalf("singular read for %s returned HTTP %d", email, singularResp.StatusCode)
	}

	var wrapped struct {
		Result liveOrgMember `json:"result"`
	}
	if err := json.NewDecoder(singularResp.Body).Decode(&wrapped); err != nil {
		t.Fatalf("could not parse the singular collaborator record for %s: %s", email, err)
	}

	// Surface a list-vs-singular divergence rather than hiding it. If these ever
	// agree-by-accident the tests still work; if they disagree, the log says so,
	// and the singular value is the one used.
	if len(fromList.AdditionalRoles) != len(wrapped.Result.AdditionalRoles) {
		t.Logf("NOTE: list and singular disagree on additional_roles for this member "+
			"(list=%v, singular=%v). Using the singular value, which is authoritative; "+
			"the split is the known Postgres-vs-SpiceDB read difference.",
			fromList.AdditionalRoles, wrapped.Result.AdditionalRoles)
	}

	return wrapped.Result, true
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
	// image_reader_no_base_images, not something plausible-sounding like
	// "cloud_user". The backend enforces a two-value enum and rejects anything
	// else with a 422 naming the permitted set:
	//   permitted: 'image_reader', 'image_reader_no_base_images'
	// The first draft of this test used "cloud_user" and failed on that 422 - a
	// defect in the FIXTURE, not in the write path it was meant to exercise. Worth
	// the comment because "cloud_user" reads like a real role name and there is no
	// enum constant in this repo to catch the mistake at compile time.
	const secondDeny = "image_reader_no_base_images"

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
