package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Mock acceptance tests for anyscale_iam_role_binding, through resource.Test
// against policyMockServer. Warnings, which resource.Test does not surface, are
// asserted through the terraform CLI in
// resource_role_binding_policy_warnings_mock_acc_test.go.

const (
	pmAddr     = "anyscale_iam_role_binding.test"
	pmProject  = "prj_policy00000000000000000001"
	pmProject2 = "prj_policy00000000000000000002"
	pmCloud    = "cld_policy00000000000000000001"
	pmOrg      = "org_policymock000000000000000"
	pmOwner    = "rol_projectowner00000000000001"
	pmReader   = "rol_projectreader0000000000001"
	pmWriter   = "rol_projectwriter0000000000001"
	pmGroupA   = "ug_policyanalysts000000000001"
	pmGroupB   = "ug_policyadmins00000000000001"
	pmAlice    = "usr_policyalice0000000000000"
	pmBob      = "usr_policybob00000000000000"
)

// newPolicyMock returns a mock where both projects, the cloud and the
// organization exist, and the caller holds the owner role it got by creating
// pmProject.
func newPolicyMock(t *testing.T) *policyMockServer {
	m := newPolicyMockServer(t)
	for _, r := range []string{pmOwner, pmReader, pmWriter} {
		m.addRole(r)
	}
	m.addGroup(pmGroupA)
	m.addGroup(pmGroupB)
	m.addScope("project", pmProject)
	m.addScope("project", pmProject2)
	m.addScope("cloud", pmCloud)
	m.addScope("organization", pmOrg)
	m.seed("user", policyMockCaller, pmOwner, "project", pmProject)
	return m
}

// pmRole is one roles entry; nil omits an attribute.
func pmRole(roleID string, users, groups []string) string {
	quote := func(ids []string) string {
		q := make([]string, len(ids))
		for i, id := range ids {
			q[i] = fmt.Sprintf("%q", id)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	s := fmt.Sprintf("    %q = {\n", roleID)
	if users != nil {
		s += "      user_ids = " + quote(users) + "\n"
	}
	if groups != nil {
		s += "      user_group_ids = " + quote(groups) + "\n"
	}
	return s + "    }\n"
}

func pmConfig(m *policyMockServer, scopeAttr, scopeID string, roles ...string) string {
	return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_role_binding" "test" {
  %s = %q
  roles = {
%s  }
}
`, scopeAttr, scopeID, strings.Join(roles, ""))
}

// pmBase is the caller's own owner binding plus group A as reader.
func pmBase() []string {
	return []string{pmRole(pmOwner, []string{policyMockCaller}, nil), pmRole(pmReader, nil, []string{pmGroupA})}
}

func pmFact(role, principalType, principalID string) string {
	return role + "/" + principalType + "/" + principalID
}

func pmExpectFacts(m *policyMockServer, scopeType, scopeID string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		sort.Strings(want)
		if got := m.facts(scopeType, scopeID); !reflect.DeepEqual(got, want) {
			return fmt.Errorf("%s %s holds %v, want %v", scopeType, scopeID, got, want)
		}
		return nil
	}
}

// pmApplyBodies splits the logged policy PUTs into real applies and dry runs,
// returning each body's grants as sorted facts.
func pmApplyBodies(t *testing.T, reqs []string) (real, dry [][]string) {
	for _, r := range reqs {
		if !strings.HasPrefix(r, "PUT ") {
			continue
		}
		fields := strings.SplitN(r, " ", 3)
		var body struct {
			Roles []struct {
				RoleID     string `json:"role_id"`
				Principals []struct {
					PrincipalType string `json:"principal_type"`
					PrincipalID   string `json:"principal_id"`
				} `json:"principals"`
			} `json:"roles"`
		}
		if len(fields) < 3 || json.Unmarshal([]byte(fields[2]), &body) != nil {
			t.Fatalf("unparseable policy PUT %q", r)
		}
		facts := []string{}
		for _, role := range body.Roles {
			for _, p := range role.Principals {
				facts = append(facts, pmFact(role.RoleID, p.PrincipalType, p.PrincipalID))
			}
		}
		sort.Strings(facts)
		if strings.Contains(fields[1], "dry_run=true") {
			dry = append(dry, facts)
		} else {
			real = append(real, facts)
		}
	}
	return real, dry
}

// pmPlanCheck adapts a function to plancheck.PlanCheck.
type pmPlanCheck func() error

func (f pmPlanCheck) CheckPlan(_ context.Context, _ plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	resp.Error = f()
}

func pmRolesCheck(roles map[string][2][]string) statecheck.StateCheck {
	set := func(ids []string) knownvalue.Check {
		if ids == nil {
			return knownvalue.Null()
		}
		checks := make([]knownvalue.Check, len(ids))
		for i, id := range ids {
			checks[i] = knownvalue.StringExact(id)
		}
		return knownvalue.SetExact(checks)
	}
	entries := map[string]knownvalue.Check{}
	for role, p := range roles {
		entries[role] = knownvalue.ObjectExact(map[string]knownvalue.Check{"user_ids": set(p[0]), "user_group_ids": set(p[1])})
	}
	return statecheck.ExpectKnownValue(pmAddr, tfjsonpath.New("roles"), knownvalue.MapExact(entries))
}

// exactly one scope, and every roles entry names a principal
// with no empty set; changing the scope replaces the policy.
func TestAccIAMRoleBindingResource_PlanTimeValidation(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	twoScopes := m.providerBlock() + fmt.Sprintf(`
resource "anyscale_iam_role_binding" "test" {
  project_id = %q
  cloud_id   = %q
  roles      = {}
}
`, pmProject, pmCloud)
	noScope := m.providerBlock() + `
resource "anyscale_iam_role_binding" "test" {
  roles = {}
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: twoScopes, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid Attribute Combination`)},
			{Config: noScope, PlanOnly: true, ExpectError: regexp.MustCompile(`Missing Attribute Configuration`)},
			{
				Config:      pmConfig(m, "project_id", pmProject, pmRole(pmOwner, nil, nil)),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Role Entry Names No Principal`),
			},
			{
				Config:      pmConfig(m, "project_id", pmProject, pmRole(pmOwner, []string{}, []string{pmGroupA})),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`set\s+must\s+contain\s+at\s+least\s+1`),
			},
			{
				Config:      pmConfig(m, "project_id", pmProject, pmRole(pmOwner, []string{policyMockCaller}, []string{})),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`set\s+must\s+contain\s+at\s+least\s+1`),
			},
		},
	})
	if puts := m.requestsMatching("PUT "); len(puts) != 0 {
		t.Errorf("a config rejected at validation reached the API: %v", puts)
	}
}

func TestAccIAMRoleBindingResource_ScopeChangeReplaces(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seed("user", policyMockCaller, pmOwner, "project", pmProject2)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pmConfig(m, "project_id", pmProject, pmBase()...)},
			{
				Config: pmConfig(m, "project_id", pmProject2, pmBase()...),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(pmAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(pmAddr, "id", "project/"+pmProject2),
					pmExpectFacts(m, "project", pmProject, pmFact(pmOwner, "user", policyMockCaller)),
				),
			},
		},
	})
}

// create sends one PUT with the full policy, sets state from
// generate, and plans empty afterwards.
func TestAccIAMRoleBindingResource_CreateOnePut(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "project_id", pmProject,
		pmRole(pmOwner, []string{policyMockCaller}, nil),
		pmRole(pmReader, []string{pmAlice}, []string{pmGroupA, pmGroupB}))
	want := []string{
		pmFact(pmOwner, "user", policyMockCaller),
		pmFact(pmReader, "user", pmAlice),
		pmFact(pmReader, "user_group", pmGroupA),
		pmFact(pmReader, "user_group", pmGroupB),
	}
	sort.Strings(want)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					pmRolesCheck(map[string][2][]string{
						pmOwner:  {{policyMockCaller}, nil},
						pmReader: {{pmAlice}, {pmGroupA, pmGroupB}},
					}),
					statecheck.ExpectKnownValue(pmAddr, tfjsonpath.New("id"), knownvalue.StringExact("project/"+pmProject)),
				},
				Check: func(s *terraform.State) error {
					real, _ := pmApplyBodies(t, m.takeRequests())
					if len(real) != 1 {
						return fmt.Errorf("create sent %d policy applies, want 1", len(real))
					}
					if !reflect.DeepEqual(real[0], want) {
						return fmt.Errorf("apply body %v, want %v", real[0], want)
					}
					return pmExpectFacts(m, "project", pmProject, want...)(s)
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// create on a scope that already holds grants the policy does not
// name dry-runs at plan time and revokes them on apply. The warning text is
// asserted in TestAccIAMRoleBindingResource_CreateTakeoverWarningCLI.
func TestAccIAMRoleBindingResource_CreateTakeover(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seed("user", pmBob, pmReader, "project", pmProject)
	m.seed("user_group", pmGroupB, pmWriter, "project", pmProject)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() { m.takeRequests() },
				Config:    pmConfig(m, "project_id", pmProject, pmBase()...),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(pmAddr, plancheck.ResourceActionCreate),
						pmPlanCheck(func() error {
							real, dry := pmApplyBodies(t, m.requestsMatching("PUT "))
							if len(real) != 0 || len(dry) == 0 {
								return fmt.Errorf("plan made %d applies and %d dry runs, want 0 and at least 1", len(real), len(dry))
							}
							return nil
						}),
					},
				},
				Check: pmExpectFacts(m, "project", pmProject,
					pmFact(pmOwner, "user", policyMockCaller), pmFact(pmReader, "user_group", pmGroupA)),
			},
		},
	})
}

// dry-run refusals fail the plan with the server's text plus what
// to do about it.
func TestAccIAMRoleBindingResource_DryRunRefusals(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Omits the caller's own owner binding: the error names the
				// entry that keeps it.
				Config:   pmConfig(m, "project_id", pmProject, pmRole(pmReader, nil, []string{pmGroupA})),
				PlanOnly: true,
				ExpectError: regexp.MustCompile(`(?s)would\s+revoke\s+your\s+own\s+role\s+binding.*` +
					`"` + pmOwner + `"\s+=\s+\{\s+user_ids\s+=\s+\["` + policyMockCaller + `"\]`),
			},
			{
				Config: pmConfig(m, "project_id", pmProject,
					pmRole(pmOwner, []string{policyMockCaller}, nil),
					pmRole(pmReader, nil, []string{"ug_doesnotexist00000000000001"})),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Role or User Group Not Found.*User\s+group\s+'ug_doesnotexist00000000000001'\s+not\s+found`),
			},
			{
				Config: pmConfig(m, "project_id", pmProject,
					pmRole(pmOwner, []string{policyMockCaller}, nil),
					pmRole("rol_doesnotexist0000000000001", nil, []string{pmGroupA})),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Role or User Group Not Found.*Role\s+not\s+found.*role\s+"rol_doesnotexist0000000000001"`),
			},
			{
				// The caller cannot grant itself a role it does not hold.
				Config: pmConfig(m, "project_id", pmProject,
					pmRole(pmOwner, []string{policyMockCaller}, nil),
					pmRole(pmReader, []string{policyMockCaller}, nil)),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)cannot\s+assign\s+a\s+role\s+to\s+yourself.*role\s+"` + pmReader + `"`),
			},
		},
	})
	if real, _ := pmApplyBodies(t, m.takeRequests()); len(real) != 0 {
		t.Errorf("a refused plan applied a policy: %v", real)
	}
}

// A binding seconds old, such as the creator binding of a scope created in
// the same apply, answers GET by id with 403 until its permission propagates.
// The own-binding hint and the frozen lookup both read a binding by id, so
// both retry: the hint still names the entry, and destroy still keeps the
// frozen binding.
func TestAccIAMRoleBindingResource_ByIDLookupsRetryPropagation(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.setGetDenials(2)
	frozen := pmFrozenRoles(m, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: pmExpectFacts(m, "project", pmProject,
			pmFact(pmOwner, "user", policyMockCaller), pmFact(fmt.Sprintf("rol_frozen%020d", 1), "user", pmBob)),
		Steps: []resource.TestStep{
			{
				Config:   pmConfig(m, "project_id", pmProject, pmRole(pmReader, nil, []string{pmGroupA})),
				PlanOnly: true,
				ExpectError: regexp.MustCompile(`(?s)would\s+revoke\s+your\s+own\s+role\s+binding.*` +
					`"` + pmOwner + `"\s+=\s+\{\s+user_ids\s+=\s+\["` + policyMockCaller + `"\]`),
			},
			{Config: pmConfig(m, "project_id", pmProject, append(pmBase(), frozen...)...)},
		},
	})
}

// with the scope unknown at plan, no dry run is made. The warning
// is asserted in TestAccIAMRoleBindingResource_UnknownScopeWarningCLI.
func TestAccIAMRoleBindingResource_UnknownScopeSkipsDryRun(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := m.providerBlock() + fmt.Sprintf(`
resource "terraform_data" "scope" {
  input = %q
}

resource "anyscale_iam_role_binding" "test" {
  project_id = terraform_data.scope.output
  roles = {
%s  }
}
`, pmProject, strings.Join(pmBase(), ""))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() { m.takeRequests() },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue(pmAddr, tfjsonpath.New("project_id")),
						pmPlanCheck(func() error {
							if puts := m.requestsMatching("PUT "); len(puts) != 0 {
								return fmt.Errorf("plan with an unknown scope made policy calls: %v", puts)
							}
							return nil
						}),
					},
				},
				Check: pmExpectFacts(m, "project", pmProject,
					pmFact(pmOwner, "user", policyMockCaller), pmFact(pmReader, "user_group", pmGroupA)),
			},
		},
	})
}

// adding and removing principals updates in place, and the PUT
// carries the whole new policy.
func TestAccIAMRoleBindingResource_UpdateInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	updated := []string{
		pmFact(pmOwner, "user", policyMockCaller),
		pmFact(pmReader, "user", pmAlice),
		pmFact(pmReader, "user_group", pmGroupB),
		pmFact(pmWriter, "user_group", pmGroupA),
	}
	sort.Strings(updated)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pmConfig(m, "project_id", pmProject, pmBase()...)},
			{
				PreConfig: func() { m.takeRequests() },
				Config: pmConfig(m, "project_id", pmProject,
					pmRole(pmOwner, []string{policyMockCaller}, nil),
					pmRole(pmReader, []string{pmAlice}, []string{pmGroupB}),
					pmRole(pmWriter, nil, []string{pmGroupA})),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(pmAddr, plancheck.ResourceActionUpdate)},
				},
				Check: func(s *terraform.State) error {
					real, _ := pmApplyBodies(t, m.takeRequests())
					if len(real) != 1 || !reflect.DeepEqual(real[0], updated) {
						return fmt.Errorf("update applied %v, want exactly one apply of %v", real, updated)
					}
					return pmExpectFacts(m, "project", pmProject, updated...)(s)
				},
			},
		},
	})
}

// a binding added outside Terraform plans its removal; one revoked
// outside Terraform plans its return.
func TestAccIAMRoleBindingResource_Drift(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "project_id", pmProject, pmBase()...)
	base := []string{pmFact(pmOwner, "user", policyMockCaller), pmFact(pmReader, "user_group", pmGroupA)}
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(pmAddr, plancheck.ResourceActionUpdate)},
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig:        func() { m.seed("user", pmBob, pmReader, "project", pmProject) },
				Config:           cfg,
				ConfigPlanChecks: update,
				Check:            pmExpectFacts(m, "project", pmProject, base...),
			},
			{
				PreConfig: func() {
					for _, b := range m.bindingsOn("project", pmProject) {
						if b.PrincipalID == pmGroupA {
							m.revoke(b.ID)
						}
					}
				},
				Config:           cfg,
				ConfigPlanChecks: update,
				Check:            pmExpectFacts(m, "project", pmProject, base...),
			},
		},
	})
}

// bindingsOn copies the scope's bindings.
func (m *policyMockServer) bindingsOn(scopeType, scopeID string) []policyMockBinding {
	var out []policyMockBinding
	m.with(func() {
		for _, b := range m.bindings {
			if b.ResourceType == scopeType && b.ResourceID == scopeID {
				out = append(out, *b)
			}
		}
	})
	return out
}

// a cold import by project/<id> recovers the bindings, and a
// config naming them plans no change.
func TestAccIAMRoleBindingResource_ImportNoOp(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seed("user_group", pmGroupA, pmReader, "project", pmProject)
	m.seed("user", pmAlice, pmReader, "project", pmProject)
	cfg := pmConfig(m, "project_id", pmProject,
		pmRole(pmOwner, []string{policyMockCaller}, nil),
		pmRole(pmReader, []string{pmAlice}, []string{pmGroupA}))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg,
				ResourceName:       pmAddr,
				ImportState:        true,
				ImportStateId:      "project/" + pmProject,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d states, want 1", len(states))
					}
					a := states[0].Attributes
					want := map[string]string{
						"id":                                "project/" + pmProject,
						"project_id":                        pmProject,
						"roles.%":                           "2",
						"roles." + pmOwner + ".user_ids.#":  "1",
						"roles." + pmOwner + ".user_ids.0":  policyMockCaller,
						"roles." + pmReader + ".user_ids.0": pmAlice,
						"roles." + pmReader + ".user_group_ids.0": pmGroupA,
					}
					for k, v := range want {
						if a[k] != v {
							return fmt.Errorf("imported %s = %q, want %q (all: %v)", k, a[k], v, a)
						}
					}
					return nil
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccIAMRoleBindingResource_ImportMalformedID(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "project_id", pmProject, pmBase()...)
	format := regexp.MustCompile(`<scope_type>/<scope_id>`)
	steps := []resource.TestStep{}
	for _, id := range []string{pmProject, "team/" + pmProject, "project/", "project/" + pmProject + "/x"} {
		steps = append(steps, resource.TestStep{
			Config: cfg, ResourceName: pmAddr, ImportState: true, ImportStateId: id, ImportStatePersist: true, ExpectError: format,
		})
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: ProtoV6ProviderFactories, Steps: steps})
}

// destroy keeps the caller's own bindings and revokes the rest.
func TestAccIAMRoleBindingResource_DestroyKeepsOwnBindings(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             pmExpectFacts(m, "project", pmProject, pmFact(pmOwner, "user", policyMockCaller)),
		Steps: []resource.TestStep{
			{
				Config: pmConfig(m, "project_id", pmProject,
					pmRole(pmOwner, []string{policyMockCaller}, []string{pmGroupB}),
					pmRole(pmReader, []string{pmAlice}, []string{pmGroupA})),
			},
		},
	})
}

// pmFrozenRoles adds n roles and seeds bob frozen in each, returning the
// roles entries that name them.
func pmFrozenRoles(m *policyMockServer, n int) []string {
	var entries []string
	for i := 1; i <= n; i++ {
		role := fmt.Sprintf("rol_frozen%020d", i)
		m.addRole(role)
		m.seedFrozen("user", pmBob, role, "project", pmProject)
		entries = append(entries, pmRole(role, []string{pmBob}, nil))
	}
	return entries
}

// each frozen refusal adds the named binding and retries; ten are
// kept, and the rest of the policy is revoked.
func TestAccIAMRoleBindingResource_DestroyKeepsFrozenBindings(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	frozen := pmFrozenRoles(m, 10)
	want := []string{pmFact(pmOwner, "user", policyMockCaller)}
	for i := 1; i <= 10; i++ {
		want = append(want, pmFact(fmt.Sprintf("rol_frozen%020d", i), "user", pmBob))
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             pmExpectFacts(m, "project", pmProject, want...),
		Steps: []resource.TestStep{
			{Config: pmConfig(m, "project_id", pmProject, append(pmBase(), frozen...)...)},
		},
	})
}

// the eleventh frozen refusal is an error, and nothing is
// revoked.
func TestAccIAMRoleBindingResource_DestroyFrozenRetryBound(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	frozen := pmFrozenRoles(m, 11)
	cfg := pmConfig(m, "project_id", pmProject, append(pmBase(), frozen...)...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				Config:      cfg,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Too Many Frozen Role Bindings`),
				Check: func(*terraform.State) error {
					if n := len(m.facts("project", pmProject)); n != 13 {
						return fmt.Errorf("refused destroy left %d bindings, want all 13", n)
					}
					return nil
				},
			},
			{
				PreConfig: m.unfreezeAll,
				Config:    m.providerBlock(),
				Check:     pmExpectFacts(m, "project", pmProject, pmFact(pmOwner, "user", policyMockCaller)),
			},
		},
	})
}

// a destroy the API refuses as an organization lockout fails at
// plan time, before anything is applied.
func TestAccIAMRoleBindingResource_DestroyPlanLockout(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "organization_id", pmOrg, pmRole(pmOwner, nil, []string{pmGroupB}))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() { m.setLockout(true); m.takeRequests() },
				Config:    m.providerBlock(),
				// The error must come from the plan's dry run: no real apply.
				ExpectError: regexp.MustCompile(`(?s)Change Would Leave Organization Without an Owner.*without\s+an\s+Organization\s+owner`),
			},
			{
				PreConfig: func() {
					if real, _ := pmApplyBodies(t, m.takeRequests()); len(real) != 0 {
						t.Errorf("the refused destroy applied %v", real)
					}
					m.setLockout(false)
				},
				Config: m.providerBlock(),
				Check:  pmExpectFacts(m, "organization", pmOrg),
			},
		},
	})
}

// a 403 on generate with the scope also 403 removes the policy
// from state; with the scope still readable it is an error and state stays.
func TestAccIAMRoleBindingResource_ReadForbidden(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "project_id", pmProject, pmBase()...)
	inState := func(want bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			if _, ok := s.RootModule().Resources[pmAddr]; ok != want {
				return fmt.Errorf("%s in state = %v, want %v", pmAddr, ok, want)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig:    func() { m.setNoIAM("project", pmProject, true) },
				RefreshState: true,
				ExpectError:  regexp.MustCompile(`(?s)Role Binding Not Readable.*no\s+longer\s+manages\s+IAM`),
			},
			{
				// State was kept: with access back, the plan is empty.
				PreConfig: func() { m.setNoIAM("project", pmProject, false) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				PreConfig:    func() { m.deleteScope("project", pmProject) },
				RefreshState: true,
				Check:        inState(false),
				// The follow-up plan creates the policy anew, and its dry run
				// is refused on the deleted project.
				ExpectError: regexp.MustCompile(`Cannot Manage IAM on Scope`),
			},
			{Config: m.providerBlock()},
		},
	})
}

// Role bindings off is the disabled error and keeps state; an unconverted
// organization is an error naming conversion.
func TestAccIAMRoleBindingResource_FlagOffKeepsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "project_id", pmProject, pmBase()...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				// A refresh persists what Read decides, so a Read that dropped
				// the policy would leave step 3 planning a create.
				PreConfig:    func() { m.setFlagOff(true) },
				RefreshState: true,
				ExpectError:  regexp.MustCompile(`(?s)Role bindings are not enabled.*kept\s+in\s+Terraform\s+state\s+unchanged`),
			},
			{
				PreConfig: func() { m.setFlagOff(false) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccIAMRoleBindingResource_UnconvertedOrg(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.setUnconverted(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      pmConfig(m, "project_id", pmProject, pmBase()...),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Organization Not Converted to Role Bindings.*converts\s+it\s+to\s+role\s+bindings`),
			},
		},
	})
}

// the data source reads the same shape, and a 403 is an error
// whether the scope is gone or access was lost.
func TestAccIAMRoleBindingDataSource_Reads(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.seed("user_group", pmGroupA, pmReader, "project", pmProject)
	m.seed("user", pmAlice, pmReader, "project", pmProject)
	cfg := m.providerBlock() + fmt.Sprintf(`
data "anyscale_iam_role_binding" "test" {
  project_id = %q
}
`, pmProject)
	const addr = "data.anyscale_iam_role_binding.test"
	notReadable := regexp.MustCompile(`Role Binding Not Readable`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.StringExact("project/"+pmProject)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("roles"), knownvalue.MapExact(map[string]knownvalue.Check{
						pmOwner: knownvalue.ObjectExact(map[string]knownvalue.Check{
							"user_ids":       knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(policyMockCaller)}),
							"user_group_ids": knownvalue.Null(),
						}),
						pmReader: knownvalue.ObjectExact(map[string]knownvalue.Check{
							"user_ids":       knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(pmAlice)}),
							"user_group_ids": knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(pmGroupA)}),
						}),
					})),
				},
			},
			{
				PreConfig:   func() { m.setNoIAM("project", pmProject, true) },
				Config:      cfg,
				ExpectError: notReadable,
			},
			{
				PreConfig:   func() { m.setNoIAM("project", pmProject, false); m.deleteScope("project", pmProject) },
				Config:      cfg,
				ExpectError: notReadable,
			},
		},
	})
}

func TestAccIAMRoleBindingDataSource_FlagOff(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	m.setFlagOff(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: m.providerBlock() + fmt.Sprintf(`
data "anyscale_iam_role_binding" "test" {
  cloud_id = %q
}
`, pmCloud),
				ExpectError: regexp.MustCompile(`Role bindings are not enabled`),
			},
		},
	})
}

// On an Azure control plane the policy refuses create, update and import at
// plan time, still destroys, and plans empty on unchanged state; the data
// source refuses to read.
func TestAccIAMRoleBindingResource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	var azure atomic.Bool
	changed := append(pmBase(), pmRole(pmWriter, nil, []string{pmGroupB}))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps: withAzureDestroyCheck(azureBlockedSteps(&azure, pmAddr, m.providerBlock(),
			pmConfig(m, "project_id", pmProject, pmBase()...), pmConfig(m, "project_id", pmProject, changed...)), pmAddr),
	})
}

func TestAccIAMRoleBindingDataSource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	var azure atomic.Bool
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps: azureDataSourceSteps(&azure, m.providerBlock()+fmt.Sprintf(`
data "anyscale_iam_role_binding" "test" {
  project_id = %q
}
`, pmProject)),
	})
}

// A PUT that succeeded is kept in state even when the read-back fails: the
// create errors, Terraform records the resource as tainted, and the next plan
// replaces it rather than creating it as if nothing had been applied.
func TestAccIAMRoleBindingResource_ReadBackFailureKeepsTaintedState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newPolicyMock(t)
	cfg := pmConfig(m, "project_id", pmProject, pmBase()...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { m.setGenerateStatus(http.StatusBadRequest) },
				Config:      cfg,
				ExpectError: regexp.MustCompile(`read role binding policy after apply`),
			},
			{
				PreConfig: func() {
					if real, _ := pmApplyBodies(t, m.requestsMatching("PUT ")); len(real) != 1 {
						t.Errorf("the failed create made %d applies, want 1", len(real))
					}
					m.setGenerateStatus(0)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(pmAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: pmExpectFacts(m, "project", pmProject,
					pmFact(pmOwner, "user", policyMockCaller), pmFact(pmReader, "user_group", pmGroupA)),
			},
		},
	})
}
