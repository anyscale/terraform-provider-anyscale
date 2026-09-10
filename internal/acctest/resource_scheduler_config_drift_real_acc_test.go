package acctest

// Out-of-band drift against the real Anyscale API.
//
// This is the one check a mock cannot answer honestly. Every other
// scheduler test asserts how the provider behaves given a response shape; this
// one asserts what the real backend does with a document written by something
// other than Terraform, and then whether the next plan sees it. A mock that
// returns the drifted document proves only that the mock was told to.
//
// It is gated behind ANYSCALE_TEST_REAL_INFRA because it mutates a shared,
// org-level singleton irreversibly. The scheduler config API is append-only:
// applying mints a permanent version and there is no route that removes one.
// The test therefore spends exactly three real versions - a create, an
// out-of-band write, and Terraform's corrective apply - plus one on cleanup to
// put the organization's previous document back. That budget is the reason
// this is one test and not a suite; it must not run on every CI pass.
//
// One case the cleanup cannot cover: if the organization had never applied a
// config, there is no previous document to restore and the test's own declared
// flavor stays active. That is inert - nothing references it - and unavoidable
// without a delete verb. It is not a leak to be swept; there is no sweeper for
// this surface and there cannot be one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

const schedulerConfigAPIPath = "/api/v2/scheduler/config"

// schedulerRealAPI is a minimal direct client for the scheduler config
// endpoint. It deliberately does not reuse the provider's own request helpers:
// the point of an out-of-band write is that it did not come from the code
// under test, and a "drift" produced by the same marshalling path the provider
// uses is not out-of-band in any sense that matters.
type schedulerRealAPI struct {
	t     *testing.T
	url   string
	token string
}

func newSchedulerRealAPI(t *testing.T) *schedulerRealAPI {
	t.Helper()

	url := os.Getenv("ANYSCALE_API_URL")
	if url == "" {
		url = "https://console.anyscale.com"
	}
	// Match the provider's own resolution order, not just the env var. On a
	// machine authenticated with `anyscale login`, a token-only check would
	// skip this one test while every other scheduler test ran - a check that
	// reads covered and never executes in the most common local setup.
	token := os.Getenv("ANYSCALE_CLI_TOKEN")
	if token == "" {
		resolved, err := provider.GetAuthToken()
		if err != nil || resolved == "" {
			t.Skip("SKIP(no-credentials): set ANYSCALE_CLI_TOKEN or run `anyscale login` for the real-API drift test")
		}
		token = resolved
	}
	return &schedulerRealAPI{t: t, url: url, token: token}
}

func (c *schedulerRealAPI) do(method string, body []byte) (int, []byte) {
	c.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+schedulerConfigAPIPath, reader)
	if err != nil {
		c.t.Fatalf("building %s request: %v", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, schedulerConfigAPIPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read to EOF: a single Read can return short, and a truncated body would
	// satisfy assertions meant to constrain the whole document.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("reading %s response: %v", method, err)
	}
	return resp.StatusCode, raw
}

// LiveDocument returns the organization's active config document as a raw JSON
// message, or nil when no config has ever been applied.
func (c *schedulerRealAPI) LiveDocument() json.RawMessage {
	c.t.Helper()

	status, raw := c.do(http.MethodGet, nil)
	if status == http.StatusNotFound {
		return nil
	}
	if status == http.StatusForbidden {
		c.t.Skipf("SKIP(scheduler-not-enabled): GET %s answered 403; this organization does not have the scheduler enabled", schedulerConfigAPIPath)
	}
	if status != http.StatusOK {
		c.t.Fatalf("GET %s: unexpected status %d", schedulerConfigAPIPath, status)
	}

	var body struct {
		Result struct {
			Config json.RawMessage `json:"config"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		c.t.Fatalf("undecodable GET body: %v", err)
	}
	return body.Result.Config
}

// Apply writes a document out of band and returns the version the backend
// assigned.
func (c *schedulerRealAPI) Apply(document string) int64 {
	c.t.Helper()

	status, raw := c.do(http.MethodPost, []byte(fmt.Sprintf(`{"config":%s}`, document)))
	if status != http.StatusOK && status != http.StatusCreated {
		c.t.Fatalf("POST %s: unexpected status %d (body: %s)", schedulerConfigAPIPath, status, string(raw))
	}

	var body struct {
		Result struct {
			Version int64 `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		c.t.Fatalf("undecodable POST body: %v", err)
	}
	return body.Result.Version
}

// TestAccSchedulerConfigResourceOutOfBandDriftRealAPI: a config changed
// outside Terraform shows as drift on the next plan, and applying restores
// the declared document.
//
// Both halves are load bearing and they fail independently. A provider whose
// Read did not actually re-read would show an empty plan and silently accept
// whatever the out-of-band writer left behind. A provider that detected the
// drift but whose Update sent the wrong document would show the diff and then
// not fix it - which is worse than not noticing, because the practitioner is
// told it was corrected. So the plan-action check and the post-apply read-back
// against the live API are two assertions, not one restated.
func TestAccSchedulerConfigResourceOutOfBandDriftRealAPI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	api := newSchedulerRealAPI(t)

	// Put the organization's previous document back when the test finishes.
	// This cannot undo the versions the test minted - nothing can, the API is
	// append-only - but it does leave the active document as it was found,
	// which is what the next reader of this org cares about.
	previous := api.LiveDocument()
	t.Cleanup(func() {
		if previous == nil {
			return
		}
		api.Apply(string(previous))
	})

	const declaredFlavor = "tfacc-drift-declared"
	const driftedFlavor = "tfacc-drift-out-of-band"

	config := fmt.Sprintf(`
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = %q },
  ]
}
`, declaredFlavor)

	const resourceName = "anyscale_scheduler_config.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Establish Terraform as the writer of record.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "version"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", declaredFlavor),
				),
			},
			{
				// Something other than Terraform rewrites the document. The
				// same config must now plan an update: refresh has to see the
				// real change, not the value it last wrote.
				PreConfig: func() {
					api.Apply(fmt.Sprintf(`{"resource_flavors":[{"name":%q}]}`, driftedFlavor))
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", declaredFlavor),
					// State agreeing with the config proves only that the
					// provider wrote its plan into state. Read the live
					// document back through a client Terraform had no hand in
					// to prove the correction actually reached the backend.
					func(*terraform.State) error {
						live := api.LiveDocument()
						var doc struct {
							ResourceFlavors []struct {
								Name string `json:"name"`
							} `json:"resource_flavors"`
						}
						if err := json.Unmarshal(live, &doc); err != nil {
							return fmt.Errorf("undecodable live document %s: %w", string(live), err)
						}
						if len(doc.ResourceFlavors) != 1 || doc.ResourceFlavors[0].Name != declaredFlavor {
							return fmt.Errorf("apply did not restore the declared document; live config is %s", string(live))
						}
						return nil
					},
				),
			},
		},
	})
}
