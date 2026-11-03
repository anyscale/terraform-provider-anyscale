package acctest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccSchedulerConfigResource_RealAPI applies, updates and imports the
// organization's scheduler config against the real API.
//
// The config is an org singleton with no DELETE: every apply mints a new
// immutable version, and this resource's destroy only drops state. So the test
// snapshots the active document first and, in t.Cleanup, applies it again as a
// new version. An organization that never had a config (GET answers 404)
// cannot return to that state, so the test refuses to run there.
//
// The documents applied here declare no scheduling_rules, so while the test
// runs no workload is rejected for matching no rule. They do replace whatever
// the organization had, for the length of the run.
//
// Skips when the Anyscale Scheduler is not enabled for the organization.
//
// Run: TF_ACC=1 ANYSCALE_TEST_ORG_MUTATION=1 ANYSCALE_TEST_ORG_NAME=<org> go test ./internal/acctest -run '^TestAccSchedulerConfigResource_RealAPI$' -v -count=1
func TestAccSchedulerConfigResource_RealAPI(t *testing.T) {
	skipUnlessOrgMutation(t)

	org, err := readLiveOrganization()
	if err != nil {
		t.Fatalf("read organization: %s", err)
	}

	prior, hadConfig, err := readLiveSchedulerDocument()
	if err != nil {
		if errors.Is(err, errSchedulerNotEnabledLive) {
			t.Skipf("SKIP(org-mutation): the Anyscale Scheduler is not enabled for organization %q: %s", org.Name, err)
		}
		t.Fatalf("snapshot the scheduler config: %s", err)
	}
	if !hadConfig {
		t.Skipf("SKIP(org-mutation): organization %q has never had a scheduler config; the API has no delete, "+
			"so applying one could not be undone. Run against an organization that already has one.", org.Name)
	}
	t.Logf("SNAPSHOT: active scheduler config is version %d", prior.Version)

	t.Cleanup(func() {
		restore := prior.Config
		version, err := applyLiveSchedulerDocument(restore)
		if err != nil {
			t.Errorf("RESTORE FAILED: could not re-apply the organization's previous scheduler config: %s. "+
				"Re-apply it by hand (anyscale scheduler config apply) from version %d", err, prior.Version)
			return
		}
		after, _, err := readLiveSchedulerDocument()
		if err != nil {
			t.Errorf("RESTORE UNVERIFIED: applied version %d but could not read it back: %s", version, err)
			return
		}
		if !jsonEqual(after.Config, restore) {
			t.Errorf("RESTORE FAILED: active scheduler config after restore is %s, want %s",
				truncateBody(string(after.Config), 512), truncateBody(string(restore), 512))
			return
		}
		t.Logf("RESTORED: previous scheduler document re-applied as version %d", version)
	})

	const addr = "anyscale_scheduler_config.test"
	flavor := UniqueName(t, "sched")
	config := func(maxWorkloads int) string {
		return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    {
      name = %q
    },
  ]

  recycle_policy = {
    max_workloads = %d
  }
}
`, flavor, maxWorkloads)
	}

	var lastVersion string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// Destroy must not write: the active version is still the last one the
		// test applied.
		CheckDestroy: func(*terraform.State) error {
			live, _, err := readLiveSchedulerDocument()
			if err != nil {
				return err
			}
			if strconv.FormatInt(live.Version, 10) != lastVersion {
				return fmt.Errorf("active scheduler config is version %d after destroy, want %s: destroy wrote a new version",
					live.Version, lastVersion)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config(50),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "resource_flavors.0.name", flavor),
					resource.TestCheckResourceAttr(addr, "recycle_policy.max_workloads", "50"),
					checkLiveSchedulerDocument(addr, flavor, 50),
				),
			},
			{
				Config: config(60),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "recycle_policy.max_workloads", "60"),
					checkLiveSchedulerDocument(addr, flavor, 60),
					CaptureResourceAttr(addr, "version", &lastVersion),
				),
			},
			{
				ResourceName:  addr,
				ImportState:   true,
				ImportStateId: org.ID,
				// The resource has no id attribute; version names the document.
				ImportStateVerifyIdentifierAttribute: "version",
				ImportStateVerify:                    true,
			},
		},
	})
}

var errSchedulerNotEnabledLive = errors.New("scheduler not enabled")

type liveSchedulerDocument struct {
	Version int64           `json:"version"`
	Config  json.RawMessage `json:"config"`
}

// readLiveSchedulerDocument reads the active scheduler config outside
// Terraform, keeping the document as raw JSON so a restore re-applies exactly
// what was there, including any field this provider does not model.
// found=false means the organization never had a config (404).
func readLiveSchedulerDocument() (doc liveSchedulerDocument, found bool, err error) {
	status, body, err := orgAPI("GET", "/api/v2/scheduler/config", nil)
	if err != nil {
		return doc, false, err
	}
	switch {
	case status == http.StatusNotFound:
		return doc, false, nil
	case status == http.StatusForbidden && strings.Contains(string(body), "not enabled for this organization"):
		return doc, false, fmt.Errorf("%w: %s", errSchedulerNotEnabledLive, truncateBody(string(body), 256))
	case status != http.StatusOK:
		return doc, false, fmt.Errorf("GET /api/v2/scheduler/config: HTTP %d: %s", status, truncateBody(string(body), 256))
	}
	var parsed struct {
		Result liveSchedulerDocument `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return doc, false, fmt.Errorf("parse scheduler config: %w", err)
	}
	if len(parsed.Result.Config) == 0 || string(parsed.Result.Config) == "null" {
		parsed.Result.Config = json.RawMessage(`{}`)
	}
	return parsed.Result, true, nil
}

// applyLiveSchedulerDocument POSTs document as a new active version.
func applyLiveSchedulerDocument(document json.RawMessage) (int64, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"config":`)
	buf.Write(document)
	buf.WriteString(`}`)
	status, body, err := orgAPI("POST", "/api/v2/scheduler/config", buf.Bytes())
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return 0, fmt.Errorf("POST /api/v2/scheduler/config: HTTP %d: %s", status, truncateBody(string(body), 512))
	}
	var parsed struct {
		Result struct {
			Version int64 `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("parse apply response: %w", err)
	}
	return parsed.Result.Version, nil
}

// checkLiveSchedulerDocument asserts the active document holds what the step
// applied and that its version matches state.
func checkLiveSchedulerDocument(addr, flavor string, maxWorkloads int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		live, found, err := readLiveSchedulerDocument()
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("API reports no active scheduler config after apply")
		}
		if got := strconv.FormatInt(live.Version, 10); got != rs.Primary.Attributes["version"] {
			return fmt.Errorf("API active version %s, state version %s", got, rs.Primary.Attributes["version"])
		}
		var doc struct {
			ResourceFlavors []struct {
				Name string `json:"name"`
			} `json:"resource_flavors"`
			RecyclePolicy *struct {
				MaxWorkloads *int64 `json:"max_workloads"`
			} `json:"recycle_policy"`
		}
		if err := json.Unmarshal(live.Config, &doc); err != nil {
			return fmt.Errorf("parse active document: %w", err)
		}
		if len(doc.ResourceFlavors) != 1 || doc.ResourceFlavors[0].Name != flavor {
			return fmt.Errorf("API resource_flavors = %+v, want one flavor %q", doc.ResourceFlavors, flavor)
		}
		if doc.RecyclePolicy == nil || doc.RecyclePolicy.MaxWorkloads == nil || *doc.RecyclePolicy.MaxWorkloads != maxWorkloads {
			return fmt.Errorf("API recycle_policy = %s, want max_workloads %d", truncateBody(string(live.Config), 512), maxWorkloads)
		}
		return nil
	}
}

// jsonEqual compares two JSON documents structurally.
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return bytes.Equal(ab, bb)
}
