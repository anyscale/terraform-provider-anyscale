package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// existsCheckState is a state holding one resource with the given primary ID and attributes.
func existsCheckState(addr, id string, attrs map[string]string) *terraform.State {
	return &terraform.State{Modules: []*terraform.ModuleState{{
		Path: []string{"root"},
		Resources: map[string]*terraform.ResourceState{
			addr: {Primary: &terraform.InstanceState{ID: id, Attributes: attrs}},
		},
	}}}
}

// runExistsCheck runs check c against a server that answers every GET with status and body,
// and reports the path it was asked for.
func runExistsCheck(t *testing.T, c apiExistsCheck, state *terraform.State, status int, body string) (string, error) {
	t.Helper()
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	client := provider.NewClientWithToken(server.URL, "exists-check-fake-token")
	err := newAPIExistsCheckWithClient(func() (*provider.Client, error) { return client, nil }, "r.test", c)(state)
	return gotPath, err
}

// Each per-resource exists check, as its acceptance tests call it, against the statuses and
// bodies that decide it.
func TestAPIExistsChecks(t *testing.T) {
	cloudOK := `{"result": {"id": "cld_1"}}`
	cases := []struct {
		name     string
		check    apiExistsCheck
		id       string
		attrs    map[string]string
		status   int
		body     string
		wantPath string
		wantErr  string // "" means the check passes
	}{
		{"project found", projectExistsCheck, "prj_1", map[string]string{"id": "prj_1"}, 200, `{}`, "/api/v2/projects/prj_1", ""},
		{"project 404", projectExistsCheck, "prj_1", map[string]string{"id": "prj_1"}, 404, `{}`, "/api/v2/projects/prj_1", "status 404"},
		{"project 201 not accepted", projectExistsCheck, "prj_1", map[string]string{"id": "prj_1"}, 201, `{}`, "/api/v2/projects/prj_1", "status 201"},
		{"project empty id", projectExistsCheck, "prj_1", map[string]string{}, 200, `{}`, "", "no project ID"},

		{"cloud found", cloudExistsCheck, "cld_1", nil, 200, cloudOK, "/api/v2/clouds/cld_1", ""},
		{"cloud 404", cloudExistsCheck, "cld_1", nil, 404, `{}`, "/api/v2/clouds/cld_1", "status 404"},
		{"cloud 500", cloudExistsCheck, "cld_1", nil, 500, `{}`, "/api/v2/clouds/cld_1", "status 500"},
		{"cloud body id mismatch", cloudExistsCheck, "cld_1", nil, 200, `{"result": {"id": "cld_other"}}`, "/api/v2/clouds/cld_1", "cloud ID mismatch"},
		{"cloud body unparseable", cloudExistsCheck, "cld_1", nil, 200, `not json`, "/api/v2/clouds/cld_1", "failed to parse"},
		{"cloud empty id", cloudExistsCheck, "", nil, 200, cloudOK, "", "no cloud ID"},

		{"compute config uses config_id", computeConfigExistsCheck, "cpt_primary", map[string]string{"config_id": "cpt_ver"}, 200, `{}`, "/api/v2/compute_templates/cpt_ver", ""},
		{"compute config falls back to primary id", computeConfigExistsCheck, "cpt_primary", map[string]string{}, 200, `{}`, "/api/v2/compute_templates/cpt_primary", ""},
		{"compute config 404", computeConfigExistsCheck, "cpt_primary", map[string]string{"config_id": "cpt_ver"}, 404, `{}`, "/api/v2/compute_templates/cpt_ver", "status 404"},
		{"compute config empty id", computeConfigExistsCheck, "", map[string]string{}, 200, `{}`, "", "no compute config ID"},

		{"build found", containerImageBuildExistsCheck, "apt_1", map[string]string{"id": "apt_1"}, 200, `{}`, "/api/v2/application_templates/apt_1", ""},
		{"build 201 not accepted", containerImageBuildExistsCheck, "apt_1", map[string]string{"id": "apt_1"}, 201, `{}`, "/api/v2/application_templates/apt_1", "status 201"},
		{"build empty id", containerImageBuildExistsCheck, "apt_1", map[string]string{}, 200, `{}`, "", "no cluster environment ID"},

		{"registry 200", containerImageRegistryExistsCheck, "apt_1", map[string]string{"build_id": "bld_1"}, 200, `{}`, "/api/v2/builds/bld_1", ""},
		{"registry 201 accepted", containerImageRegistryExistsCheck, "apt_1", map[string]string{"build_id": "bld_1"}, 201, `{}`, "/api/v2/builds/bld_1", ""},
		{"registry 404", containerImageRegistryExistsCheck, "apt_1", map[string]string{"build_id": "bld_1"}, 404, `{}`, "/api/v2/builds/bld_1", "status 404"},
		{"registry empty build_id", containerImageRegistryExistsCheck, "apt_1", map[string]string{"id": "apt_1"}, 200, `{}`, "", "no build ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := runExistsCheck(t, tc.check, existsCheckState("r.test", tc.id, tc.attrs), tc.status, tc.body)
			if path != tc.wantPath {
				t.Errorf("GET %q, want %q", path, tc.wantPath)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("check failed: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("check error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// A resource missing from state fails rather than passing vacuously.
func TestAPIExistsCheck_MissingResourceFails(t *testing.T) {
	empty := &terraform.State{Modules: []*terraform.ModuleState{{Path: []string{"root"}, Resources: map[string]*terraform.ResourceState{}}}}
	_, err := runExistsCheck(t, projectExistsCheck, empty, 200, `{}`)
	if err == nil || !strings.Contains(err.Error(), "not found in state") {
		t.Errorf("err = %v, want resource not found in state", err)
	}
}
