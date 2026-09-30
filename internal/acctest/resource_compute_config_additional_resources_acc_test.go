package acctest

// Option C / F7 regression: the new additional_resources multi-resource
// support. splitDeploymentConfigsForRead matches N deployment_configs
// entries back to (primary, additional[]) by cloud_resource NAME against
// prior state, not position or response order - the backend's response
// order isn't guaranteed, it's merely echoed, not backend-meaningful.
//
// IMPORTANT construction note: a 2-entry response can make a completely
// broken ("first entry becomes primary" / no real name-matching)
// implementation pass BY COINCIDENCE, if the mock happens to return entries
// in an order where the naive fallback still lands on the right answer. This
// test deliberately uses a genuinely WRONG-first-entry response (a real
// additional resource placed ahead of the true primary) so a broken
// implementation would visibly assign the wrong cloud_resource as primary -
// not just reorder harmlessly.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func newOptionCMockServer(t *testing.T) (*httptest.Server, *sync.Mutex, *[]any) {
	t.Helper()
	var mu sync.Mutex
	var storedDeploymentConfigs []any
	var storedName string
	const configID = "cpt_optionc_mock"
	mux := http.NewServeMux()

	buildRecord := func() map[string]any {
		return map[string]any{
			"id": configID, "name": storedName, "version": int64(1),
			"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
			"archived_at": nil,
			"config": map[string]any{
				"cloud_id":           "cld_mock_cc",
				"deployment_configs": storedDeploymentConfigs,
			},
		}
	}

	mux.HandleFunc("/api/v2/compute_templates/search", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if storedName == "" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "metadata": map[string]any{"next_paging_token": nil}})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":  []any{map[string]any{"id": configID, "name": storedName, "version": int64(1)}},
			"metadata": map[string]any{"next_paging_token": nil},
		})
	})

	// Registered under both the subtree and bare-path forms (see
	// helpers_cloud_adoption_test.go: a subtree-only mock makes ServeMux
	// 301-redirect a bare-path request, and whether that redirect is followed
	// is not portable across Go versions/http.Client configs).
	computeTemplatesHandler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/compute_templates/":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read create body: %v", err)
				return
			}
			var req struct {
				Name   string         `json:"name"`
				Config map[string]any `json:"config"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal create body: %v", err)
				return
			}
			dcs, _ := req.Config["deployment_configs"].([]any)

			mu.Lock()
			storedDeploymentConfigs = dcs
			storedName = req.Name
			record := buildRecord()
			record["config"] = req.Config
			mu.Unlock()

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
		case r.Method == http.MethodGet:
			mu.Lock()
			record := buildRecord()
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": record})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
		}
	}
	mux.HandleFunc("/api/v2/compute_templates/", computeTemplatesHandler)
	mux.HandleFunc("/api/v2/compute_templates", computeTemplatesHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &mu, &storedDeploymentConfigs
}

func optionCDeploymentConfigEntry(cloudResource string) map[string]any {
	return map[string]any{
		"cloud_deployment": cloudResource,
		"head_node_type": map[string]any{
			"name":          "head",
			"instance_type": "m5.large",
		},
	}
}

func TestAccComputeConfigResource_AdditionalResourcesPrimaryMatchedByName_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mu, storedDCs := newOptionCMockServer(t)
	name := "cc-optionc-multi"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name          = %[1]q
  cloud_id      = "cld_mock_cc"
  cloud_resource = "resource-a"

  head_node = {
    instance_type = "m5.large"
  }

  additional_resources = [
    {
      cloud_resource = "resource-b"
      head_node = {
        instance_type = "m5.large"
      }
    },
    {
      cloud_resource = "resource-c"
      head_node = {
        instance_type = "m5.large"
      }
    }
  ]
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_resource", "resource-a"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "additional_resources.#", "2"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "additional_resources.0.cloud_resource", "resource-b"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "additional_resources.1.cloud_resource", "resource-c"),
				),
			},
			{
				PreConfig: func() {
					// The critical construction: THREE entries, with a real
					// additional resource (c) placed FIRST - not the true
					// primary (a), and not just a harmless 2-entry swap. A
					// "first entry becomes primary" implementation would
					// wrongly report cloud_resource="resource-c" here; only
					// genuine name-matching against prior state recovers
					// "resource-a" as primary regardless of response order.
					mu.Lock()
					*storedDCs = []any{
						optionCDeploymentConfigEntry("resource-c"),
						optionCDeploymentConfigEntry("resource-a"),
						optionCDeploymentConfigEntry("resource-b"),
					}
					mu.Unlock()
				},
				// No config change - a pure backend-side response-order
				// shuffle must plan clean.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// GAP A regression: additional_resources must also surface on the DATA
// SOURCE, not just the resource - a multi-resource config looked up via the
// data source previously (silently, no diagnostic) returned only the
// primary entry, the exact silent-truncation problem F7 was scoped to kill,
// just on a surface F7's original scope didn't touch. The DS has no
// prior state to match against (a fresh lookup every time), so this asserts
// the deterministic cold-lookup fallback (first entry = primary, rest =
// name-sorted additional) rather than a specific configured order.
func TestAccComputeConfigDataSource_AdditionalResourcesNotSilentlyTruncated_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server, mu, storedDCs := newOptionCMockServer(t)
	name := "cc-optionc-ds-multi"

	// Seed via the mock's create path so a real config_id/name exists to
	// look up, then overwrite with a 3-entry response before the DS ever
	// reads it - the DS has no prior create step of its own in this test.
	seedConfig := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "seed" {
  name          = %[1]q
  cloud_id      = "cld_mock_cc"
  cloud_resource = "resource-a"
  head_node = {
    instance_type = "m5.large"
  }

  # The mock's stored deployment_configs is mutated directly below to
  # simulate the DS's target looking multi-resource from a fresh lookup's
  # perspective - since the mock has no separate identity for "the record
  # this resource manages" vs "the record the DS looks up," that mutation
  # also changes what THIS resource's own next Read sees. ignore_changes
  # keeps the seed resource stable (it's a fixture, not under test here) so
  # the mutation only affects the DS's independent lookup.
  lifecycle {
    ignore_changes = [additional_resources]
  }
}

data "anyscale_compute_config" "test" {
  name = anyscale_compute_config.seed.name

  depends_on = [anyscale_compute_config.seed]
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: seedConfig,
			},
			{
				PreConfig: func() {
					// Now simulate a genuinely multi-resource config: three
					// entries, none in a privileged position, so the DS must
					// resolve all of them, not just entry zero.
					mu.Lock()
					*storedDCs = []any{
						optionCDeploymentConfigEntry("resource-a"),
						optionCDeploymentConfigEntry("resource-b"),
						optionCDeploymentConfigEntry("resource-c"),
					}
					mu.Unlock()
				},
				Config: seedConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "additional_resources.#", "2"),
				),
			},
		},
	})
}
