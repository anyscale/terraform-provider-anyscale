package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Gate 2 proof for anyscale_scheduler_config's advanced_instance_config.
//
// The attribute is a jsontypes.Normalized nested inside a ListNestedAttribute
// element. Framework source describes the semantic-equality mechanism without
// revealing what Core enforces for a value nested inside a list, so a unit test
// built on that source would share its blind spot. This settles it with a real
// plan. Result: semantic equality IS consulted nested in a list.
//
// Two findings from building this, both of which changed the code under test:
//
//  1. The provider re-marshals the API's parsed object through Go, which sorts
//     keys and renders float64(3) as "3". So the API's own key reordering and
//     integer widening are canonicalized away before they ever reach state, and
//     a config written with jsonencode() (also sorted, also compact) can never
//     differ from state textually. The load-bearing case is therefore a
//     hand-written or file()-loaded blob, which is what this test uses.
//
//  2. jsontypes' jsonEqual decodes with dec.UseNumber(), so numbers are compared
//     as their literal text: "3.0" does NOT equal "3". Key order and whitespace
//     are absorbed; numeric form is not. The schema description says so.
//
// Mutation check (run, not assumed): dropping CustomType
// jsontypes.NormalizedType{} makes step 2 fail with a whitespace/key-order diff
// on both flavors.
func TestAccSchedulerConfigResourceAdvancedInstanceConfigSemanticEquality(t *testing.T) {
	server := newMockSchedulerConfigServer(t)
	defer server.Close()

	// What the practitioner writes: a literal JSON string, keys in the order
	// they chose, with the whitespace they chose. Not jsonencode() - that
	// emits sorted keys and no whitespace, i.e. the same canonical form the
	// provider produces when it re-marshals the API's parsed object, so a
	// jsonencode config diffs against nothing and tests nothing. A hand-written
	// or file()-loaded blob is where the text genuinely differs from state.
	const config = `
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    {
      name                     = "canonicalized"
      advanced_instance_config = "{\"c\": 3,   \"b\": \"two\", \"a\": 1}"
    },
    {
      name                     = "reordered"
      advanced_instance_config = "{ \"zebra\":\"z\",  \"middle\": 5,\n \"alpha\":\"a\" }"
    },
  ]
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1 applies, then refreshes the mock's reordered,
				// float-widened read shape into state.
				Config: testAccProviderBlock(server.URL) + config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_scheduler_config.test", "resource_flavors.#", "2"),
					resource.TestCheckResourceAttr("anyscale_scheduler_config.test", "version", "1"),
				),
			},
			{
				// Step 2 re-plans the identical config against that state. If
				// semantic equality is NOT consulted for a value nested in a
				// list, the widened 1 -> 1.0 and the reordered keys show as a
				// diff here and this step fails.
				Config: testAccProviderBlock(server.URL) + config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// mockSchedulerConfigServer serves the scheduler endpoints the resource
// touches, returning advanced_instance_config in the reordered, float-widened
// shape the real API returns.
type mockSchedulerConfigServer struct {
	mu      sync.Mutex
	version int64
	config  map[string]any
	reads   int
}

func newMockSchedulerConfigServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := &mockSchedulerConfigServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/scheduler/config/validate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v2/scheduler/config", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch r.Method {
		case http.MethodPost:
			var body struct {
				Config map[string]any `json:"config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("undecodable apply body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.version++
			s.config = body.Config
			fmt.Fprintf(w, `{"result":{"version":%d}}`, s.version)

		case http.MethodGet:
			if s.version == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"detail":"No active scheduler config found."}}`))
				return
			}
			s.reads++
			// Serve the two flavor blobs mangled the way the real API mangles
			// them: keys in a different order than they were written, and
			// integers widened to floats.
			//
			// This is hand-written rather than round-tripped through
			// encoding/json on purpose. Go's marshaller sorts map keys and
			// renders float64(1) as "1", which reproduces Terraform's own
			// jsonencode output byte for byte - a mock built that way passes
			// against a plain string attribute and proves nothing. Verified:
			// the first draft of this test did exactly that and the mutation
			// check came back green.
			fmt.Fprintf(w, `{"result":{"version":%d,"is_active":true,"created_at":"2026-09-09T00:00:00Z","creator_id":"usr_mock","config":{"resource_flavors":[{"name":"canonicalized","advanced_instance_config":{"c":3.0,"b":"two","a":1.0}},{"name":"reordered","advanced_instance_config":{"middle":5.0,"alpha":"a","zebra":"z"}}]}}}`, s.version)

		default:
			t.Errorf("unexpected method %s on /api/v2/scheduler/config", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux)
}
