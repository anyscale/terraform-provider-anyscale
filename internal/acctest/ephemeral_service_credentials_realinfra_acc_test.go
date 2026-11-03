package acctest

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// TestAccServiceCredentialsEphemeralResource_RealInfra proves anyscale_service_credentials
// against a real running service on the pinned fixture cloud (any platform): the auth_token and
// base_url it returns answer an authenticated request to the service, and the same request
// without the token is refused. That is the only use of these values, so a token that reads back
// non-empty but does not authenticate would pass every mock test and still be useless.
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest/ \
//	  -run '^TestAccServiceCredentialsEphemeralResource_RealInfra$' -v -timeout 90m
//
// The last step reads the same service back through the anyscale_service and anyscale_services
// data sources. It creates one compute config, one ephemeral project and one service, and
// destroys them.
func TestAccServiceCredentialsEphemeralResource_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	f := newCanaryRealInfraFixture(t, "svc-creds")
	service := f.config("main:app", "v1", "")
	withCredentials := service + `
ephemeral "anyscale_service_credentials" "test" {
  service_id = anyscale_service.test.id
}

provider "echo" {
  data = ephemeral.anyscale_service_credentials.test
}

resource "echo" "creds" {}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{Config: service},
			{
				// The credentials and the service and services data sources read back the same running
				// service. This step needs no network path to the service itself.
				Config: withCredentials + `
data "anyscale_service" "by_id" {
  id = anyscale_service.test.id
}

data "anyscale_service" "by_name" {
  name       = anyscale_service.test.name
  project_id = anyscale_service.test.project_id
}

data "anyscale_services" "filtered" {
  name_contains = anyscale_service.test.name
  project_id    = anyscale_service.test.project_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.anyscale_service.by_id", "id", canaryServiceAddr, "id"),
					resource.TestCheckResourceAttrPair("data.anyscale_service.by_id", "base_url", canaryServiceAddr, "base_url"),
					resource.TestCheckResourceAttrPair("data.anyscale_service.by_id", "hostname", canaryServiceAddr, "hostname"),
					resource.TestCheckResourceAttr("data.anyscale_service.by_id", "current_state", "RUNNING"),
					resource.TestCheckResourceAttrPair("data.anyscale_service.by_name", "id", canaryServiceAddr, "id"),
					resource.TestCheckResourceAttr("data.anyscale_services.filtered", "services.#", "1"),
					resource.TestCheckResourceAttrPair("data.anyscale_services.filtered", "services.0.id", canaryServiceAddr, "id"),
					resource.TestCheckResourceAttr("data.anyscale_services.filtered", "services.0.cloud_id", f.cloudID),
					resource.TestCheckResourceAttrPair("echo.creds", "data.base_url", canaryServiceAddr, "base_url"),
					resource.TestCheckResourceAttrSet("echo.creds", "data.auth_token"),
				),
			},
			{
				// Last, because it needs a network path from the test runner to the service.
				Config: withCredentials + `
data "anyscale_service" "by_id" {
  id = anyscale_service.test.id
}

data "anyscale_service" "by_name" {
  name       = anyscale_service.test.name
  project_id = anyscale_service.test.project_id
}

data "anyscale_services" "filtered" {
  name_contains = anyscale_service.test.name
  project_id    = anyscale_service.test.project_id
}
`,
				Check: checkServiceCredentialsAuthenticate("echo.creds"),
			},
		},
	})
}

// checkServiceCredentialsAuthenticate sends GET base_url with and without the captured token. A
// freshly deployed service can take a little while to start answering, so it retries until the
// authenticated request succeeds, then requires the unauthenticated one to be refused.
func checkServiceCredentialsAuthenticate(echoAddr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[echoAddr]
		if !ok {
			return fmt.Errorf("not found in state: %s", echoAddr)
		}
		baseURL := rs.Primary.Attributes["data.base_url"]
		token := rs.Primary.Attributes["data.auth_token"]
		if baseURL == "" || token == "" {
			return fmt.Errorf("credentials incomplete: base_url set = %t, auth_token set = %t", baseURL != "", token != "")
		}
		get := func(withToken bool) (int, string, error) {
			req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/", nil)
			if err != nil {
				return 0, "", err
			}
			if withToken {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
			if err != nil {
				return 0, "", err
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return resp.StatusCode, string(body), nil
		}

		// Reaching the service is a network precondition, not provider behavior: report a host
		// that cannot be dialed separately from a request the service refused or mishandled.
		host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(baseURL, "/"), "https://"), "http://")
		deadline := time.Now().Add(5 * time.Minute)
		for {
			status, body, err := get(true)
			if err == nil && status == http.StatusOK {
				break
			}
			if time.Now().After(deadline) {
				conn, dialErr := net.DialTimeout("tcp", host+":443", 10*time.Second)
				if conn != nil {
					_ = conn.Close()
				}
				return fmt.Errorf("authenticated GET %s: status %d, err %v, body %q (TCP dial %s:443: %v)",
					baseURL, status, err, body, host, dialErr)
			}
			time.Sleep(10 * time.Second)
		}
		status, body, err := get(false)
		if err != nil {
			return fmt.Errorf("unauthenticated GET %s: %v", baseURL, err)
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return fmt.Errorf("unauthenticated GET %s: status %d, want 401 or 403 (body %q)", baseURL, status, body)
		}
		return nil
	}
}
