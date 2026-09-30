package acctest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// projectRoleCalls records the project-collaborator writes the provider sends,
// so a test can assert what reached the wire rather than what state claims.
type projectRoleCalls struct {
	mu      sync.Mutex
	grants  []string // "<project>:<email>=<level>" per batch_create entry
	deletes []string // "<project>:<identity>" per DELETE
}

func (c *projectRoleCalls) snapshot() (grants, deletes []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.grants...), append([]string(nil), c.deletes...)
}

// recordProjectRoleCalls wraps the cloud_access mock's handler to record
// project batch_create and DELETE calls before the mock serves them.
func recordProjectRoleCalls(t *testing.T, next http.Handler) (http.Handler, *projectRoleCalls) {
	t.Helper()
	calls := &projectRoleCalls{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v2/projects/"); ok {
			projectID, tail, _ := strings.Cut(rest, "/")
			switch {
			case r.Method == http.MethodPost && tail == "collaborators/users/batch_create":
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("reading batch_create body: %v", err)
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				var entries []struct {
					Value struct {
						Email string `json:"email"`
					} `json:"value"`
					PermissionLevel string `json:"permission_level"`
				}
				_ = json.Unmarshal(body, &entries)
				calls.mu.Lock()
				for _, e := range entries {
					calls.grants = append(calls.grants, fmt.Sprintf("%s:%s=%s", projectID, strings.ToLower(e.Value.Email), e.PermissionLevel))
				}
				calls.mu.Unlock()
			case r.Method == http.MethodDelete && strings.HasPrefix(tail, "collaborators/"):
				calls.mu.Lock()
				calls.deletes = append(calls.deletes, projectID+":"+strings.TrimPrefix(tail, "collaborators/"))
				calls.mu.Unlock()
			}
		}
		next.ServeHTTP(w, r)
	}), calls
}

// TestAccCloudAccessResource_ProjectRolesGrantedAndKept_MockServer drives
// member[*].projects through Terraform Core. Declared project roles used to
// be dropped before reconcile: Create granted nothing, and any later update
// of the resource revoked a declared role the backend already held, with no
// sign in the plan.
//
// cloud-owner@example.com is declared at its real role so every plan can be
// asserted empty; otherwise the mock's unrevokable owner keeps each plan
// non-empty and would hide a project diff.
func TestAccCloudAccessResource_ProjectRolesGrantedAndKept_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const projectID = "prj_cloudaccess_roles_mock"
	const alice = "alice@example.com"

	server := newMockCloudAccessServer(t)
	handler, calls := recordProjectRoleCalls(t, server.Config.Handler)
	server.Config.Handler = handler

	config := func(aliceBaseRole string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_access" "test" {
  cloud_id = %[1]q

  member = {
    %[2]q = {
      base_role = "owner"
    }
    %[3]q = {
      base_role = %[4]q
      projects = {
        %[5]q = "readonly"
      }
    }
  }
}
`, cloudAccessMockCloudID, cloudAccessMockImplicitMember, alice, aliceBaseRole, projectID)
	}

	wantGrant := fmt.Sprintf("%s:%s=readonly", projectID, alice)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create must send the declared project grant. The step's own
				// post-apply plan must be empty: Read refreshes projects from
				// the backend, so a role that was never granted shows as a diff.
				Config: config("writer"),
				Check: func(*terraform.State) error {
					grants, deletes := calls.snapshot()
					if len(grants) != 1 || grants[0] != wantGrant {
						return fmt.Errorf("project grants sent = %v, want exactly [%s]", grants, wantGrant)
					}
					if len(deletes) != 0 {
						return fmt.Errorf("project revokes sent on create = %v, want none", deletes)
					}
					return nil
				},
			},
			{
				// An update for an unrelated reason (alice's cloud base_role)
				// must leave her declared project role alone: no DELETE to the
				// project's collaborators, and the backend still holds it.
				Config: config("collaborator"),
				Check: func(*terraform.State) error {
					_, deletes := calls.snapshot()
					if len(deletes) != 0 {
						return fmt.Errorf("an unrelated update revoked declared project roles: DELETEs %v", deletes)
					}
					held, err := mockProjectCollaboratorEmails(server.URL, projectID)
					if err != nil {
						return err
					}
					if !held[alice] {
						return fmt.Errorf("backend no longer holds %s on %s after an unrelated update (holds %v)", alice, projectID, held)
					}
					return nil
				},
			},
			{
				Config:   config("collaborator"),
				PlanOnly: true,
			},
		},
	})
}

// mockProjectCollaboratorEmails reads a project's collaborators through the
// mock's own GET route, the one the provider's Read uses.
func mockProjectCollaboratorEmails(serverURL, projectID string) (map[string]bool, error) {
	resp, err := http.Get(serverURL + "/api/v2/projects/" + projectID + "/collaborators/users")
	if err != nil {
		return nil, fmt.Errorf("reading project collaborators: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Results []struct {
			Value struct {
				Email string `json:"email"`
			} `json:"value"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decoding project collaborators: %w", err)
	}
	held := map[string]bool{}
	for _, r := range page.Results {
		held[strings.ToLower(r.Value.Email)] = true
	}
	return held, nil
}
