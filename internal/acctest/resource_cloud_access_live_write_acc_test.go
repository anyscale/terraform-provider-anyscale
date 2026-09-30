package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Live write-path verification for anyscale_cloud_access - the five
// (live)-marked items (AC-1, AC-6, AC-15, AC-17, AC-26) that cannot be
// proven by a mock, since each asks what the real backend does with an
// authoritative write, not what the provider sends.
//
// The write path is unconditional; these run against the real resource on any
// build.
//
// RULING A1 BINDS HARDER HERE than anywhere else: every test below creates
// its OWN fresh ephemeral cloud and destroys it afterward. NEVER the static
// tfp-test-aws-useast1-STATIC fixture - an authoritative Create/Delete
// against it would revoke real people's real access.
//
// The test SUBJECT is ANYSCALE_TEST_USER_EMAIL, the designated no-cloud
// member fixture (requireRealInfraTestUser) - never a freshly-created
// service account, since a leak-on-panic there is still a leak.

// cloudAccessLiveOOBMember performs an out-of-band grant on a cloud, bypassing
// Terraform entirely - the legacy bootstrap-plus-permission-level path, which
// is what a real console/CLI administrator would use. Returns the identity_id
// the cloud-scope collaborator endpoints key on.
func cloudAccessLiveOOBMember(t *testing.T, client *provider.Client, cloudID, email, permissionLevel string) string {
	t.Helper()
	ctx := context.Background()

	body, _ := json.Marshal(map[string]string{"email": email, "permission_level": permissionLevel})
	resp, err := client.DoRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v2/clouds/%s/collaborators/users", cloudID), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("out-of-band add of %s to cloud %s failed: %v", email, cloudID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("out-of-band add of %s to cloud %s: status %d: %s", email, cloudID, resp.StatusCode, raw)
	}

	return cloudAccessLiveIdentityID(t, client, cloudID, email)
}

// cloudAccessLiveIdentityID resolves an email to its cloud-scope identity_id
// via the same search endpoint the resource reads.
func cloudAccessLiveIdentityID(t *testing.T, client *provider.Client, cloudID, email string) string {
	t.Helper()
	ctx := context.Background()

	resp, err := client.DoRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/search", cloudID), strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("member search on cloud %s failed: %v", cloudID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Results []struct {
			ID    string `json:"id"`
			Value struct {
				Email string `json:"email"`
			} `json:"value"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decoding member search response failed: %v", err)
	}
	for _, r := range page.Results {
		if strings.EqualFold(r.Value.Email, email) {
			return r.ID
		}
	}
	t.Fatalf("email %s not found in cloud %s's member search: %+v", email, cloudID, page.Results)
	return ""
}

// cloudAccessLiveUserID resolves an email to its Anyscale user_id via the
// cloud-scope search, needed for the roles PUT (which is user_id-keyed, not
// identity_id-keyed).
func cloudAccessLiveUserID(t *testing.T, client *provider.Client, cloudID, email string) string {
	t.Helper()
	ctx := context.Background()

	resp, err := client.DoRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/search", cloudID), strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("member search on cloud %s failed: %v", cloudID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Results []struct {
			Value struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"value"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decoding member search response failed: %v", err)
	}
	for _, r := range page.Results {
		if strings.EqualFold(r.Value.Email, email) {
			return r.Value.ID
		}
	}
	t.Fatalf("email %s not found in cloud %s's member search: %+v", email, cloudID, page.Results)
	return ""
}

// cloudAccessLiveSetRole grants an RBAC role out-of-band via the same roles
// PUT the reconcile itself uses - simulating a change made through a channel
// other than THIS Terraform resource (e.g. a second practitioner, or a script).
func cloudAccessLiveSetRole(t *testing.T, client *provider.Client, cloudID, userID, baseRole string, denyRoles []string) {
	t.Helper()
	ctx := context.Background()

	if denyRoles == nil {
		denyRoles = []string{}
	}
	body, _ := json.Marshal(map[string]interface{}{"base_role": baseRole, "deny_roles": denyRoles})
	resp, err := client.DoRequest(ctx, http.MethodPut,
		fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/%s/roles", cloudID, userID), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("out-of-band roles PUT for user %s on cloud %s failed: %v", userID, cloudID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("out-of-band roles PUT for user %s on cloud %s: status %d: %s", userID, cloudID, resp.StatusCode, raw)
	}
}

// cloudAccessLiveMemberEmails reads the real, current cloud-scope member list
// straight from the backend - the assertion every test below actually cares
// about, since state can lie but the backend cannot.
func cloudAccessLiveMemberEmails(t *testing.T, client *provider.Client, cloudID string) map[string]bool {
	t.Helper()
	emails, err := fetchCollaboratorEmails(context.Background(), client, http.MethodPost,
		fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/search", cloudID), strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("member search on cloud %s failed: %v", cloudID, err)
	}
	return emails
}

// cloudAccessLiveProjectMemberEmails reads a project's real, current
// collaborator list straight from the backend, for AC-15.
func cloudAccessLiveProjectMemberEmails(t *testing.T, client *provider.Client, projectID string) map[string]bool {
	t.Helper()
	emails, err := fetchCollaboratorEmails(context.Background(), client, http.MethodGet,
		fmt.Sprintf("/api/v2/projects/%s/collaborators/users", projectID), nil)
	if err != nil {
		t.Fatalf("project collaborator list for %s failed: %v", projectID, err)
	}
	return emails
}

// fetchCollaboratorEmails returns the lowercased emails in one collaborator
// list response. Any status other than 200 is an error: callers assert that
// an email is ABSENT, and an error body decoded as an empty list would make
// that assertion pass without the backend ever answering.
func fetchCollaboratorEmails(ctx context.Context, client *provider.Client, method, path string, body io.Reader) (map[string]bool, error) {
	page, err := provider.DoRequestAndParse[struct {
		Results []struct {
			Value struct {
				Email string `json:"email"`
			} `json:"value"`
		} `json:"results"`
	}](ctx, client, method, path, body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	emails := make(map[string]bool, len(page.Results))
	for _, r := range page.Results {
		emails[strings.ToLower(r.Value.Email)] = true
	}
	return emails, nil
}

// cloudAccessLiveConfig is the shared HCL shape for these tests: the real
// "anyscale" provider (no mock URL override - it authenticates exactly as
// the ambient environment does, per CLAUDE.md's resolution order), one
// anyscale_cloud_access resource, and whatever member map the caller passes
// raw. Kept as a single string builder so every test constructs its config
// the same way, rather than five slightly different HCL templates.
func cloudAccessLiveConfig(cloudID, memberHCL string, allowEmpty bool) string {
	return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_cloud_access" "live" {
  cloud_id               = %[1]q
  allow_empty_member_set = %[2]t
  member = {
%[3]s
  }
}
`, cloudID, allowEmpty, memberHCL)
}

// TestAccCloudAccessResource_LiveColdImportAndDelete covers AC-1 (a cold
// import populates member with exactly the real member set minus the
// caller) and AC-17 (Delete revokes every managed member) on the SAME
// ephemeral cloud, since one lifecycle naturally produces both: seed a
// member out-of-band, import, then let the test's own automatic destroy
// (asserted via CheckDestroy) prove the revoke.
func TestAccCloudAccessResource_LiveColdImportAndDelete(t *testing.T) {
	email := requireRealInfraTestUser(t)
	PreCheck(t)

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}

	cloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Fatalf("failed to create ephemeral test cloud: %v", err)
	}

	// Seeded OUT OF BAND, before Terraform ever sees this cloud - the real
	// member set a cold import must recover exactly, per AC-1.
	cloudAccessLiveOOBMember(t, client, cloudID, email, "write")

	resourceName := "anyscale_cloud_access.live"
	config := cloudAccessLiveConfig(cloudID, fmt.Sprintf(`    %[1]q = { base_role = "writer" }`, email), false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// AC-17: the test's own automatic destroy at the end of this test IS
		// the revoke under test. Assert against the real backend, not state.
		CheckDestroy: func(*terraform.State) error {
			live := cloudAccessLiveMemberEmails(t, client, cloudID)
			if live[strings.ToLower(email)] {
				return fmt.Errorf("AC-17 FAILED: %s still has real cloud access after Delete - the backend reports %v",
					email, live)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// AC-1: import must recover EXACTLY the real member set the
				// out-of-band seed created, minus the caller (who was never
				// added here, so there is nothing to exclude, but the
				// resource must still not error resolving/excluding them).
				//
				// No ImportStateVerify: this is a COLD import (no preceding
				// Create in this test), so there is no prior state to verify
				// against - CLAUDE.md's own documented guidance on this
				// exact shape. Config is required by terraform-plugin-testing
				// even for an import-only step, to know which provider
				// configuration to import through.
				Config:        config,
				ResourceName:  resourceName,
				ImportState:   true,
				ImportStateId: cloudID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["member.%"]; got != "1" {
						return fmt.Errorf("AC-1 FAILED: imported member.%% = %q, want \"1\" (exactly the seeded member): %+v",
							got, attrs)
					}
					if _, ok := attrs["member."+email+".base_role"]; !ok {
						return fmt.Errorf("AC-1 FAILED: import did not recover the seeded member %s: %+v", email, attrs)
					}
					return nil
				},
			},
			{
				// A config declaring exactly the recovered member, so this
				// test's own end-of-run destroy has a real resource to
				// destroy rather than relying on import's throwaway state
				// (which terraform-plugin-testing discards at the end of
				// the step that verifies import).
				//
				// This step itself is a real apply (Create runs again since
				// import's state didn't persist into this step - CLAUDE.md's
				// own documented gotcha), not a no-op - it does NOT prove
				// AC-2 on its own. The no-op proof is step 3, below.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "member.%", "1"),
					resource.TestCheckResourceAttr(resourceName, "member."+email+".base_role", "writer"),
				),
			},
			{
				// AC-2: a config declaring exactly the recovered members
				// plans EMPTY. This is the "Test B" shape CLAUDE.md
				// prescribes for import-recovery coverage - two sequential
				// Config-only steps, no import involved, so state actually
				// carries forward between them (unlike the discarded
				// throwaway ImportState step above). Step 2 just applied
				// this exact config for real; re-planning the SAME config
				// here must show no changes.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCloudAccessResource_LiveCreateRevokesUndeclaredMembers is AC-6: an
// authoritative Create against a cloud already holding an undeclared member
// revokes them. The fixture is added out-of-band BEFORE Terraform ever runs,
// then Create declares an EMPTY member set - the starkest form of "the
// configuration does not declare this person."
func TestAccCloudAccessResource_LiveCreateRevokesUndeclaredMembers(t *testing.T) {
	email := requireRealInfraTestUser(t)
	PreCheck(t)

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}

	cloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Fatalf("failed to create ephemeral test cloud: %v", err)
	}

	// UNDECLARED and out of band - the fixture has real cloud access before
	// this resource is ever created, and the config below never names them.
	cloudAccessLiveOOBMember(t, client, cloudID, email, "write")
	if live := cloudAccessLiveMemberEmails(t, client, cloudID); !live[strings.ToLower(email)] {
		t.Fatalf("precondition failed: the out-of-band seed did not land: %v", live)
	}

	resourceName := "anyscale_cloud_access.live"
	// allow_empty_member_set=true: an empty member map on a cloud that
	// currently has members is otherwise a plan-time refusal (AC-11), and
	// this test's whole point is authoritative Create against exactly that
	// starting condition.
	config := cloudAccessLiveConfig(cloudID, "", true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "member.%", "0"),
					// AC-6 itself: assert against the BACKEND, not state, that
					// Create actually revoked the undeclared member.
					func(*terraform.State) error {
						live := cloudAccessLiveMemberEmails(t, client, cloudID)
						if live[strings.ToLower(email)] {
							return fmt.Errorf("AC-6 FAILED: Create did not revoke the undeclared member %s - backend still reports %v",
								email, live)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccCloudAccessResource_LiveProjectRoleDropRevokes is AC-15: dropping a
// project entry from a member's config revokes that project role on the
// real backend.
func TestAccCloudAccessResource_LiveProjectRoleDropRevokes(t *testing.T) {
	email := requireRealInfraTestUser(t)
	PreCheck(t)

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}

	cloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Fatalf("failed to create ephemeral test cloud: %v", err)
	}
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloudID)
	if err != nil {
		t.Fatalf("failed to create ephemeral test project: %v", err)
	}

	resourceName := "anyscale_cloud_access.live"
	withProjectRole := cloudAccessLiveConfig(cloudID, fmt.Sprintf(`    %[1]q = {
      base_role = "writer"
      projects  = { %[2]q = "readonly" }
    }`, email, projectID), false)
	withoutProjectRole := cloudAccessLiveConfig(cloudID, fmt.Sprintf(`    %[1]q = { base_role = "writer" }`, email), false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withProjectRole,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "member."+email+".projects."+projectID, "readonly"),
					func(*terraform.State) error {
						live := cloudAccessLiveProjectMemberEmails(t, client, projectID)
						if !live[strings.ToLower(email)] {
							return fmt.Errorf("precondition failed: the project grant did not land on the backend: %v", live)
						}
						return nil
					},
				),
			},
			{
				// The project entry is dropped, not just changed - AC-15's
				// exact shape.
				Config: withoutProjectRole,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "member."+email+".projects."+projectID),
					// AC-15 itself: assert against the BACKEND that the
					// project role was actually revoked, not merely absent
					// from config.
					func(*terraform.State) error {
						live := cloudAccessLiveProjectMemberEmails(t, client, projectID)
						if live[strings.ToLower(email)] {
							return fmt.Errorf("AC-15 FAILED: dropping the project entry did not revoke %s's real project role - backend still reports %v",
								email, live)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccCloudAccessResource_LiveOutOfBandRoleChangeDetectedAndCorrected is
// AC-26: an out-of-band change made through the RBAC roles path (the same
// path this resource itself writes through) is detected on refresh and
// corrected on the next apply.
//
// Uses a ROLE CHANGE on the already-managed fixture, rather than a brand
// new grant to a second identity, because no second safe non-admin,
// non-caller identity exists to use for this (see J.22's ruling against
// creating one). The mechanism under test - an RBAC-path write this
// resource did not make, read back on refresh, corrected on apply - is
// identical either way: AC-26 is about the ROUTE the drift arrives through
// (roles path vs legacy path, the J.19 distinction), not about grant vs.
// role-change.
func TestAccCloudAccessResource_LiveOutOfBandRoleChangeDetectedAndCorrected(t *testing.T) {
	email := requireRealInfraTestUser(t)
	PreCheck(t)

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}

	cloudID, _, err := createEphemeralTestCloud(t)
	if err != nil {
		t.Fatalf("failed to create ephemeral test cloud: %v", err)
	}

	resourceName := "anyscale_cloud_access.live"
	config := cloudAccessLiveConfig(cloudID, fmt.Sprintf(`    %[1]q = { base_role = "writer" }`, email), false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr(resourceName, "member."+email+".base_role", "writer"),
			},
			{
				// Between steps: an out-of-band RBAC write this resource did
				// not make, changing writer -> owner.
				//
				// PlanOnly + ExpectNonEmptyPlan, NOT RefreshState: RefreshState
				// asserts a subsequent plan against the SAME config is EMPTY -
				// it exists to prove a refresh causes no drift, the opposite of
				// what this step needs to prove. AC-26's first half is that the
				// drift IS visible: the plan (which refreshes first) must show a
				// change, because state now reports "owner" from the real
				// backend while the unchanged config still declares "writer".
				PreConfig: func() {
					userID := cloudAccessLiveUserID(t, client, cloudID, email)
					cloudAccessLiveSetRole(t, client, cloudID, userID, "owner", nil)
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Re-apply the ORIGINAL config (writer). AC-26's correction
				// half: the next apply must set the real role back to what
				// the configuration declares.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "member."+email+".base_role", "writer"),
					func(*terraform.State) error {
						live := cloudAccessLiveMemberEmails(t, client, cloudID) // presence check only
						if !live[strings.ToLower(email)] {
							return fmt.Errorf("precondition broke: %s is no longer a member at all: %v", email, live)
						}
						userID := cloudAccessLiveUserID(t, client, cloudID, email)
						roles := cloudAccessLiveReadRoles(t, client, cloudID)
						got, ok := roles[userID]
						if !ok || len(got) == 0 || !strings.EqualFold(got[0], "writer") {
							return fmt.Errorf("AC-26 FAILED: the out-of-band owner grant was not corrected - backend reports base_roles=%v for %s, want [writer]",
								got, email)
						}
						return nil
					},
				),
			},
		},
	})
}

// cloudAccessLiveReadRoles reads the real per-user RBAC role listing for a
// cloud - the surface AC-26 is actually about, distinct from the coarse
// permission_level the plain member search reports.
func cloudAccessLiveReadRoles(t *testing.T, client *provider.Client, cloudID string) map[string][]string {
	t.Helper()
	ctx := context.Background()

	resp, err := client.DoRequest(ctx, http.MethodGet, fmt.Sprintf("/api/v2/clouds/%s/collaborators/roles", cloudID), nil)
	if err != nil {
		t.Fatalf("roles read for cloud %s failed: %v", cloudID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Results []struct {
			UserID    string   `json:"user_id"`
			BaseRoles []string `json:"base_roles"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decoding roles response failed: %v", err)
	}
	out := make(map[string][]string, len(page.Results))
	for _, r := range page.Results {
		out[r.UserID] = r.BaseRoles
	}
	return out
}
