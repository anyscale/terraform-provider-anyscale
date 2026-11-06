package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real-API coverage for anyscale_role_binding_policy, on a disposable project
// and an empty group, so no grant reaches anyone. Needs a converted test
// organization with role bindings enabled; skips otherwise.
//
//	TF_ACC=1 ANYSCALE_TEST_ORG_MODE=converted go test ./internal/acctest/ -run '^TestAccRoleBindingPolicyResource_RealAPI$' -v
//
// The frozen-binding and lockout paths are mock-only: no test organization can
// produce a frozen binding, and a lockout needs the organization's owners
// stripped.

type realPolicyWire struct {
	Roles []struct {
		RoleID     string `json:"role_id"`
		Principals []struct {
			PrincipalType string `json:"principal_type"`
			PrincipalID   string `json:"principal_id"`
		} `json:"principals"`
	} `json:"roles"`
}

// realPolicy generates the scope's policy outside Terraform.
func realPolicy(t *testing.T, scopeType, scopeID string) realPolicyWire {
	t.Helper()
	status, body := userGroupAPI(t, "GET", fmt.Sprintf("/api/v2/role_bindings/%s/%s/policy/generate", scopeType, scopeID), nil)
	if status != http.StatusOK {
		t.Fatalf("generate policy on %s %s: status %d: %s", scopeType, scopeID, status, truncateBody(string(body), 256))
	}
	var resp struct {
		Result realPolicyWire `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse generated policy: %v", err)
	}
	return resp.Result
}

// realPolicyFacts is the scope's policy as sorted "role/principal_type/principal_id".
func realPolicyFacts(t *testing.T, scopeType, scopeID string) []string {
	t.Helper()
	facts := []string{}
	for _, r := range realPolicy(t, scopeType, scopeID).Roles {
		for _, p := range r.Principals {
			facts = append(facts, pmFact(r.RoleID, p.PrincipalType, p.PrincipalID))
		}
	}
	sort.Strings(facts)
	return facts
}

func realCallerUserID(t *testing.T) string {
	t.Helper()
	status, body := userGroupAPI(t, "GET", "/api/v2/userinfo", nil)
	if status != http.StatusOK {
		t.Fatalf("userinfo: status %d: %s", status, truncateBody(string(body), 256))
	}
	var resp struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Result.ID == "" {
		t.Fatalf("parse userinfo: %v", err)
	}
	return resp.Result.ID
}

// realOtherBuiltInRole returns a built-in role other than exclude, preferring
// a reader role. Role names differ per environment, so it is picked at run
// time.
func realOtherBuiltInRole(t *testing.T, exclude string) string {
	t.Helper()
	status, body := userGroupAPI(t, "GET", "/api/v2/roles/?"+url.Values{"count": {"50"}, "include_built_in": {"true"}}.Encode(), nil)
	if status != http.StatusOK {
		t.Fatalf("list roles: status %d: %s", status, truncateBody(string(body), 256))
	}
	var resp struct {
		Results []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			BuiltIn bool   `json:"built_in"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse roles: %v", err)
	}
	fallback := ""
	for _, r := range resp.Results {
		if !r.BuiltIn || r.ID == exclude {
			continue
		}
		if strings.Contains(strings.ToLower(r.Name), "project reader") {
			return r.ID
		}
		if fallback == "" {
			fallback = r.ID
		}
	}
	if fallback == "" {
		t.Fatalf("no built-in role other than %s in the first page of %d roles", exclude, len(resp.Results))
	}
	return fallback
}

// TestAccRoleBindingPolicyResource_RealAPI covers criteria 14-17: create with
// the caller's own owner binding and a group, out-of-band drift, import, and a
// destroy that leaves only the caller's own binding.
func TestAccRoleBindingPolicyResource_RealAPI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	skipUnlessTestOrgMode(t, testOrgModeConverted, "role binding policy writes answer 409 until the organization converts")
	PreCheck(t)
	skipIfRoleBindingsDisabled(t)

	projectID, _, err := CreateEphemeralTestProjectForCloud(t, GetTestCloudID(t))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	groupID := createUserGroupOutOfBand(t, UniqueName(t, "rbp"))
	callerID := realCallerUserID(t)

	// The project's creator holds a direct owner binding on it; the policy
	// must name it, so the test reads which role that is.
	ownerRole := ""
	for _, r := range realPolicy(t, "project", projectID).Roles {
		for _, p := range r.Principals {
			if p.PrincipalType == "user" && p.PrincipalID == callerID {
				ownerRole = r.RoleID
			}
		}
	}
	if ownerRole == "" {
		t.Fatalf("the caller %s holds no direct binding on the project it created, %s", callerID, projectID)
	}
	groupRole := realOtherBuiltInRole(t, ownerRole)

	cfg := fmt.Sprintf(`
resource "anyscale_role_binding_policy" "test" {
  project_id = %q
  roles = {
    %q = {
      user_ids = [%q]
    }
    %q = {
      user_group_ids = [%q]
    }
  }
}
`, projectID, ownerRole, callerID, groupRole, groupID)
	want := []string{pmFact(ownerRole, "user", callerID), pmFact(groupRole, "user_group", groupID)}
	sort.Strings(want)
	generateEquals := func(want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := realPolicyFacts(t, "project", projectID); !reflect.DeepEqual(got, want) {
				return fmt.Errorf("project %s holds %v, want %v", projectID, got, want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               orgModeErrorCheck(t),
		// Criterion 17: destroy revokes everything but the caller's own binding.
		CheckDestroy: generateEquals(pmFact(ownerRole, "user", callerID)),
		Steps: []resource.TestStep{
			{
				// Criterion 14; resource.Test also fails on a non-empty
				// re-plan after apply.
				Config: cfg,
				Check:  generateEquals(want...),
			},
			{
				// Criterion 15: a binding added outside Terraform plans its
				// removal.
				PreConfig: func() {
					extra := map[string]any{"roles": []any{
						map[string]any{"role_id": ownerRole, "principals": []any{
							map[string]any{"principal_type": "user", "principal_id": callerID},
							map[string]any{"principal_type": "user_group", "principal_id": groupID},
						}},
						map[string]any{"role_id": groupRole, "principals": []any{
							map[string]any{"principal_type": "user_group", "principal_id": groupID},
						}},
					}}
					path := fmt.Sprintf("/api/v2/role_bindings/project/%s/policy", projectID)
					if status, body := userGroupAPI(t, "PUT", path, extra); status != http.StatusOK {
						t.Fatalf("out-of-band policy apply: status %d: %s", status, truncateBody(string(body), 256))
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(pmAddr, plancheck.ResourceActionUpdate)},
				},
				Check: generateEquals(want...),
			},
			{
				// Criterion 16. ImportStateVerify compares the import with
				// the state the apply left, and resource.Test re-plans.
				Config:            cfg,
				ResourceName:      pmAddr,
				ImportState:       true,
				ImportStateId:     "project/" + projectID,
				ImportStateVerify: true,
			},
		},
	})
}
