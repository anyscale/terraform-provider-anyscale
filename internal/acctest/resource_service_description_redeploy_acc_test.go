package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccServiceResource_DescriptionOnlyChangeRedeploys pins that a change to
// description alone goes through PUT /apply. The backend has no other write
// path for a service's description, so an Update that skipped the apply would
// record the new value in state without sending it, and the next refresh would
// revert it. TestAccServiceResource_UpdateSkipsApplyWhenOnlyTimeoutChanges is
// the opposite case on the same comparator: a change that must not re-apply.
//
// The mock stores the description each apply sends and returns it on GET, as
// the backend does, so the test also proves the change converges.
func TestAccServiceResource_DescriptionOnlyChangeRedeploys(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const serviceID = "svc_description_redeploy"
	const name = "description-redeploy"
	const projectID = "prj_description"
	var applyCallCount int32
	var terminated int32
	var mu sync.Mutex
	var storedDescription *string

	serviceBody := func() string {
		var body map[string]any
		if err := json.Unmarshal([]byte(serviceFindingsJSON(serviceID, name, projectID, serviceFindingsCurrentState(&terminated))), &body); err != nil {
			t.Fatalf("mock service body: %v", err)
		}
		mu.Lock()
		if storedDescription != nil {
			body["description"] = *storedDescription
		} else {
			body["description"] = nil
		}
		mu.Unlock()
		out, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("mock service body: %v", err)
		}
		return string(out)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/services-v2/apply", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&applyCallCount, 1)
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Description *string `json:"description"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("apply body: %v", err)
		}
		mu.Lock()
		storedDescription = req.Description
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": `+serviceBody()+`}`)
	})
	mux.HandleFunc("/api/v2/services-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		serveServiceGetOrDelete(t, w, r, serviceBody())
	})
	mux.HandleFunc("/api/v2/services-v2/"+serviceID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&terminated, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result": {}}`)
	})
	mux.HandleFunc("/api/v2/tags/resource", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, emptyTagsBody)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	config := func(description string) string {
		return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_service" "test" {
  name              = %[1]q
  project_id        = %[2]q
  build_id          = "bld_findings"
  compute_config_id = "cpt_findings"
  description       = %[3]q
%[4]s
}
`, name, projectID, description, testAccServiceRayServeConfigHCL)
	}

	applies := func(want int32) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := atomic.LoadInt32(&applyCallCount); got != want {
				return fmt.Errorf("apply was called %d time(s), want %d", got, want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "description", "first"),
					applies(1),
				),
			},
			{
				Config: config("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "description", "second"),
					applies(2),
				),
			},
		},
	})
}
