package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// mockServiceCredentialsServer is a stateful httptest server serving only GET
// /api/v2/services-v2/{service_id}, the sole endpoint anyscale_service_credentials' Open calls
// (getServiceByID, service_helpers.go - shared with resource_service.go's own wait loop and
// Create/Read/Update/Delete). Any OTHER service_id path 404s with the real backend's wire shape,
// proving the unknown-service-id case surfaces a genuine error through Open rather than a
// silently-swallowed null (see ephemeral_service_credentials.go's Open doc comment on why this is
// deliberately NOT the same "expected empty state" treatment system_cluster_credentials gives a
// cloud with no System Cluster).
type mockServiceCredentialsServer struct {
	mu sync.Mutex

	authToken          *string
	secondaryAuthToken *string

	getCallCount int
}

// newMockServiceCredentialsServer starts a mock serving serviceID at baseURL with both tokens
// null (the zero value) - call setTokens before running a test against it.
func newMockServiceCredentialsServer(t *testing.T, serviceID, baseURL string) (*httptest.Server, *mockServiceCredentialsServer) {
	t.Helper()
	s := &mockServiceCredentialsServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/services-v2/"+serviceID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on services-v2/%s", r.Method, serviceID)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		s.mu.Lock()
		s.getCallCount++
		authPtr := s.authToken
		secondaryPtr := s.secondaryAuthToken
		s.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"id": %[1]q, "name": "svc-%[1]s", "project_id": "prj_sccred", "cloud_id": "cld_sccred",
			"creator_id": "usr_sccred", "created_at": "2026-01-01T00:00:00Z", "hostname": "svc.example.com",
			"base_url": %[2]q, "current_state": "RUNNING", "goal_state": "RUNNING",
			"auto_rollout_enabled": true, "is_multi_version": false, "error_message": null,
			"auth_token": %[3]s, "secondary_auth_token": %[4]s
		}}`, serviceID, baseURL, jsonStringOrNull(authPtr), jsonStringOrNull(secondaryPtr))
	})

	// Any other service_id: a plain 404 in the real backend's wire shape (api_common.py's
	// HTTPException handler - see extractAPIErrorDetail's doc comment in error_helpers.go), so
	// getServiceByID surfaces this as a real Go error rather than an empty/null result.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"detail": "not found"}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockServiceCredentialsServer) setTokens(authToken, secondaryAuthToken *string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authToken = authToken
	s.secondaryAuthToken = secondaryAuthToken
}

func (s *mockServiceCredentialsServer) snapshot() (getCallCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCallCount
}

// serviceCredentialsConfig builds an anyscale_service_credentials ephemeral resource block plus
// an echo capture resource pointed at the mock server.
func serviceCredentialsConfig(serverURL, serviceID, echoResourceName string) string {
	return fmt.Sprintf(`
%s

ephemeral "anyscale_service_credentials" "test" {
  service_id = %q
}

provider "echo" {
  data = ephemeral.anyscale_service_credentials.test
}

resource "echo" %q {}
`, testAccProviderBlock(serverURL), serviceID, echoResourceName)
}

// TestAccServiceCredentialsEphemeralResource is the mock-tier suite for
// anyscale_service_credentials, covering every scenario in the locked test plan
// (.crystl/quest/PR1-TEST-PLAN.md): bearer auth enabled (case 1), disabled (case 2), a token
// rotation in progress (case 3), and an unknown service_id surfacing a genuine error rather than
// a silent null (case 4). Unlike anyscale_system_cluster_credentials, this resource adds no
// diagnostic of its own - bearer-enabled is not independently-tracked state here, the API's own
// null-vs-non-null is the signal - so only value assertions are needed; no direct-Open-call
// helper is required here (contrast the sibling file's openSystemClusterCredentialsDirect).
func TestAccServiceCredentialsEphemeralResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	t.Run("BearerEnabled", func(t *testing.T) {
		serviceID := "svc_sccred_enabled"
		baseURL := "https://svc-enabled.example.com"
		server, mock := newMockServiceCredentialsServer(t, serviceID, baseURL)
		token := "mock-auth-token-enabled-abc123"
		mock.setTokens(&token, nil)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: serviceCredentialsConfig(server.URL, serviceID, "test_enabled"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_enabled", tfjsonpath.New("data").AtMapKey("auth_token"), knownvalue.StringExact(token)),
						statecheck.ExpectKnownValue("echo.test_enabled", tfjsonpath.New("data").AtMapKey("secondary_auth_token"), knownvalue.Null()),
						statecheck.ExpectKnownValue("echo.test_enabled", tfjsonpath.New("data").AtMapKey("base_url"), knownvalue.StringExact(baseURL)),
					},
				},
			},
		})

		if count := mock.snapshot(); count == 0 {
			t.Fatal("GET services-v2 was never called")
		}
	})

	t.Run("BearerDisabled", func(t *testing.T) {
		serviceID := "svc_sccred_disabled"
		baseURL := "https://svc-disabled.example.com"
		server, mock := newMockServiceCredentialsServer(t, serviceID, baseURL)
		mock.setTokens(nil, nil)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: serviceCredentialsConfig(server.URL, serviceID, "test_disabled"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_disabled", tfjsonpath.New("data").AtMapKey("auth_token"), knownvalue.Null()),
						statecheck.ExpectKnownValue("echo.test_disabled", tfjsonpath.New("data").AtMapKey("secondary_auth_token"), knownvalue.Null()),
					},
				},
			},
		})
	})

	t.Run("RotationInProgress", func(t *testing.T) {
		serviceID := "svc_sccred_rotation"
		baseURL := "https://svc-rotation.example.com"
		server, mock := newMockServiceCredentialsServer(t, serviceID, baseURL)
		primary := "mock-primary-token-rotation1"
		secondary := "mock-secondary-token-rotation2"
		mock.setTokens(&primary, &secondary)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: serviceCredentialsConfig(server.URL, serviceID, "test_rotation"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_rotation", tfjsonpath.New("data").AtMapKey("auth_token"), knownvalue.StringExact(primary)),
						statecheck.ExpectKnownValue("echo.test_rotation", tfjsonpath.New("data").AtMapKey("secondary_auth_token"), knownvalue.StringExact(secondary)),
					},
				},
			},
		})
	})

	// UnknownServiceIDErrors: deliberately different from every system_cluster_credentials case -
	// an unrecognized service_id has no "expected empty state" interpretation (a service GET 404 is
	// keyed by the service's own id, a genuine not-found), so Open must let the real error through
	// rather than returning null.
	t.Run("UnknownServiceIDErrors", func(t *testing.T) {
		serviceID := "svc_sccred_registered"
		baseURL := "https://svc-registered.example.com"
		server, _ := newMockServiceCredentialsServer(t, serviceID, baseURL)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config:      serviceCredentialsConfig(server.URL, "svc_does_not_exist", "test_unknown"),
					ExpectError: regexp.MustCompile(`(?s)API Request Failed.*unexpected status 404`),
				},
			},
		})
	})
}

// TestAccServiceCredentialsEphemeralResource_RealInfra proves the enabled/non-empty auth_token
// shape against a real, already-running service. Reuses GetTestServiceID's fixture only - does
// NOT spin up a fresh service rollout (~26 minutes) just for this test.
func TestAccServiceCredentialsEphemeralResource_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	serviceID := GetTestServiceID(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
ephemeral "anyscale_service_credentials" "test" {
  service_id = %q
}

provider "echo" {
  data = ephemeral.anyscale_service_credentials.test
}

resource "echo" "test_realinfra" {}
`, serviceID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"echo.test_realinfra",
						tfjsonpath.New("data").AtMapKey("auth_token"),
						knownvalue.StringRegexp(regexp.MustCompile(`^.+$`)),
					),
				},
			},
		},
	})
}
