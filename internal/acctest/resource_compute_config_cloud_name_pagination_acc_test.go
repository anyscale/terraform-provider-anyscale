package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// newComputeConfigCloudNamePaginationMockServer is the same compute_templates
// shape as newCC3bMockComputeConfigServer (resource_compute_config_cc3b_lifecycle_acc_test.go),
// but with a genuinely two-page /api/v2/clouds handler instead of a single-page
// one. See resource_project_cloud_name_pagination_acc_test.go's header comment
// for why the two-page shape is the point: a single-page mock cannot fail
// against B1 (ResolveCloudNameToID, cloud_helpers.go:227-259, page-1-only
// before its fix).
func newComputeConfigCloudNamePaginationMockServer(t *testing.T, page1Clouds, page2Clouds []map[string]any, configID, configName, cloudID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := r.URL.Query().Get("paging_token")
		var results []map[string]any
		var nextToken any
		switch token {
		case "":
			results = page1Clouds
			nextToken = "page2"
		case "page2":
			results = page2Clouds
			nextToken = nil
		default:
			t.Errorf("unexpected paging_token %q on GET /api/v2/clouds", token)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": results,
			"metadata": map[string]any{
				"total":             len(page1Clouds) + len(page2Clouds),
				"next_paging_token": nextToken,
			},
		})
	})

	computeTemplateJSON := func() string {
		return fmt.Sprintf(`{
			"id": %[1]q, "name": %[2]q, "version": 1,
			"created_at": "2026-01-01T00:00:00Z", "last_modified_at": "2026-01-01T00:00:00Z",
			"archived_at": "",
			"config": {
				"cloud_id": %[3]q,
				"idle_termination_minutes": 120,
				"head_node_type": {"name": "head", "instance_type": "m5.2xlarge"}
			}
		}`, configID, configName, cloudID)
	}

	mux.HandleFunc("/api/v2/compute_templates/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON())
		default:
			t.Errorf("unexpected method %s on /api/v2/compute_templates/", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/compute_templates/"+configID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": %s}`, computeTemplateJSON())
	})

	mux.HandleFunc("/api/v2/compute_templates/"+configID+"/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"archived_at": "2026-01-01T00:00:00Z"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAccComputeConfigResource_CloudNameResolvesAcrossPages is the second B1
// vehicle (design doc: .crystl/quest/design/cloud-selector-design.md). Unlike
// resource_project_cloud_name_pagination_acc_test.go's project-based proof
// (which asserts last_used_cloud_id, a proxy, since project never writes
// cloud_id back to state), compute_config writes the resolved cloud_id
// straight into state for a cloud_name-only config (see
// TestAccComputeConfigResource_Lifecycle_CloudNameOnly_MockServer), so this
// asserts on cloud_id directly with no proxy. That closes a real gap the
// project-based test cannot: a naive pagination fix that reaches page 2 but
// picks the WRONG id would still make the project test's echoed-proxy
// assertion pass for the wrong reason if the wrong id happened to be a valid
// string, whereas asserting cloud_id itself directly against the known
// page-2 id has no such blind spot.
//
// Confirmed failing-first (2026-07-25) with TF_ACC=1 go test against pre-B1
// main:
//
//	Error: Cloud Name Resolution Failed
//	Failed to resolve cloud name 'cc-cloud-on-page-two' to ID: no cloud found
//	with name 'cc-cloud-on-page-two'
func TestAccComputeConfigResource_CloudNameResolvesAcrossPages(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const targetCloudID = "cld_cc_page_two_target"
	const targetCloudName = "cc-cloud-on-page-two"
	const configID = "cpt_cc_page_two_mock"
	const configName = "cc-page-two-config"

	page1 := []map[string]any{
		{"id": "cld_cc_page_one_a", "name": "cc-unrelated-cloud-a", "created_at": "2026-01-01T00:00:00Z"},
		{"id": "cld_cc_page_one_b", "name": "cc-unrelated-cloud-b", "created_at": "2026-01-01T00:00:00Z"},
	}
	page2 := []map[string]any{
		{"id": targetCloudID, "name": targetCloudName, "created_at": "2026-01-02T00:00:00Z"},
	}

	server := newComputeConfigCloudNamePaginationMockServer(t, page1, page2, configID, configName, targetCloudID)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = %[1]q
  cloud_name = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, targetCloudName)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_name", targetCloudName),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_id", targetCloudID),
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccComputeConfigResource_CloudNameDuplicateAcrossPagesPicksNewest pins
// the user-visible behavior change the B1 fix's changelog fragment promises:
// a duplicate cloud name can now resolve to a DIFFERENT cloud than before,
// because the most-recent tiebreak sees page-2 candidates that were
// previously invisible. Two clouds share the same name, one on page 1
// (older), one on page 2 (newer) - asserts the resolver picks the newer,
// page-2 one, not simply "some" cloud.
//
// Parity with data_source_cloud.go's findCloudByName (architect's core
// concern - the resource and data-source paths must agree on what an
// ambiguous name resolves to, not just each pick something deterministic):
// this is not asserted via a live side-by-side data-source lookup in this
// test - cloudSharedAttributes() backs the full anyscale_cloud schema (71
// attributes) and its Read additionally requires GET /api/v2/clouds/{id} and
// GET /api/v2/clouds/{id}/resources, which would add substantial mock
// surface unrelated to what this test is actually pinning. Instead, parity
// is verified by reading the source directly: ResolveCloudNameToID
// (cloud_helpers.go:242-247) and findCloudByName
// (data_source_cloud.go:321-325) both call PickMostRecentMatch with the
// byte-identical shape - same "cloud" resourceType string, same
// c.Name==name predicate, same c.ID/c.CreatedAt extractors - so once both
// receive the same fully-paginated candidate list, they are the same
// deterministic function over the same input and cannot diverge. What this
// test actually needs to prove, and does, is that the resource path receives
// the FULL candidate list (both pages), which is the one way the two paths
// could disagree in practice - findCloudByName already paginates correctly
// today. Flagged this scope choice to architect rather than silently
// asserting full parity; open to adding the live data-source comparison if
// that is still wanted after weighing the mock cost.
//
// Confirmed failing-first (2026-07-25) with TF_ACC=1 go test against pre-B1
// main - and notably NOT a not-found error this time, a silently WRONG
// answer, which is the more precise proof of this test's specific scenario:
//
//	Check failed: Check 2/2 error: anyscale_compute_config.test: Attribute
//	'cloud_id' expected "cld_cc_dup_page_two_newer", got
//	"cld_cc_dup_page_one_older"
//
// The unfixed page-1-only helper finds a match on page 1 (the older cloud)
// and returns immediately, never learning page 2's newer duplicate exists -
// exactly the failure mode a not-found-only test could not catch.
func TestAccComputeConfigResource_CloudNameDuplicateAcrossPagesPicksNewest(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const sharedName = "cc-dup-name-both-pages"
	const olderCloudID = "cld_cc_dup_page_one_older"
	const newerCloudID = "cld_cc_dup_page_two_newer"
	const configID = "cpt_cc_dup_mock"
	const configName = "cc-dup-name-config"

	page1 := []map[string]any{
		{"id": olderCloudID, "name": sharedName, "created_at": "2026-01-01T00:00:00Z"},
	}
	page2 := []map[string]any{
		{"id": newerCloudID, "name": sharedName, "created_at": "2026-01-02T00:00:00Z"},
	}

	server := newComputeConfigCloudNamePaginationMockServer(t, page1, page2, configID, configName, newerCloudID)
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = %[1]q
  cloud_name = %[2]q

  head_node = {
    instance_type = "m5.2xlarge"
  }
}
`, configName, sharedName)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_name", sharedName),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_id", newerCloudID),
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
