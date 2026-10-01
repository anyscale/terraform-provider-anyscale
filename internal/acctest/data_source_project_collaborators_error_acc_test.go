package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// A failed collaborators fetch must fail the read. It used to be logged and
// replaced with an empty list, which reads as "this project has no
// collaborators". The 200 case is the control on the same path.
func TestAccProjectDataSource_CollaboratorsFetchErrorFailsRead(t *testing.T) {
	const projectID = "prj_collaborators_error_mock"

	cases := []struct {
		name               string
		collaboratorStatus int
		wantError          *regexp.Regexp
	}{
		{"server error", http.StatusInternalServerError, regexp.MustCompile(`(?s)Failed to read project collaborators \(HTTP 500\)`)},
		{"forbidden", http.StatusForbidden, regexp.MustCompile(`(?s)Failed to read project collaborators \(HTTP 403\)`)},
		{"ok", http.StatusOK, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v2/projects/"+projectID, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"result": projectResult(projectID, "collaborators-error", "2026-01-01T00:00:00Z", "cld_mock")})
			})
			mux.HandleFunc("/api/v2/projects/"+projectID+"/collaborators/users", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.collaboratorStatus)
				if tc.collaboratorStatus != http.StatusOK {
					_, _ = fmt.Fprint(w, `{"error": {"detail": "mock upstream failure"}}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"results": [{"id": "ident_mock", "value": {"id": "usr_mock", "name": "Mock", "email": "mock@example.com"}, "permission_level": "owner"}], "metadata": {"total": 1, "next_paging_token": null}}`)
			})
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			step := resource.TestStep{
				Config: testAccProviderBlock(server.URL) + fmt.Sprintf(`
data "anyscale_project" "test" {
  id = %q
}
`, projectID),
			}
			if tc.wantError != nil {
				step.ExpectError = tc.wantError
			} else {
				step.Check = resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "collaborators.#", "1"),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "collaborators.0.email", "mock@example.com"),
				)
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{step},
			})
		})
	}
}
