package acctest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real-API (Gate 1) checks for docs/decisions/service-inputs-parity. Tokens are never logged:
// only their presence and an 8-character SHA-256 prefix, to tell two tokens apart.

const parityBaseImage = "anyscale/ray:2.56.1-slim-py312-cu129"

// parityGET is the whitelisted view of a raw service GET: whether the body has a `config` key,
// token presence and fingerprint, and the primary version.
type parityGET struct {
	HasConfigKey     bool
	TokenFingerprint string // "" when auth_token is null
	PrimaryID        string
	PrimaryName      string
	CurrentState     string
	HasCanary        bool
}

func (g parityGET) String() string {
	tok := "null"
	if g.TokenFingerprint != "" {
		tok = "present(sha256:" + g.TokenFingerprint + ")"
	}
	return fmt.Sprintf("current_state=%s has_config_key=%t auth_token=%s primary=%s/%s has_canary=%t",
		g.CurrentState, g.HasConfigKey, tok, g.PrimaryID, g.PrimaryName, g.HasCanary)
}

func getParityGET(t *testing.T, serviceID string) parityGET {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	raw, err := provider.DoRequestRaw(context.Background(), client, "GET", "/api/v2/services-v2/"+serviceID, nil, 200)
	if err != nil {
		t.Fatalf("GET service: %v", err)
	}
	var body struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode service GET: %v", err)
	}
	var out parityGET
	_, out.HasConfigKey = body.Result["config"]
	var token *string
	_ = json.Unmarshal(body.Result["auth_token"], &token)
	if token != nil {
		sum := sha256.Sum256([]byte(*token))
		out.TokenFingerprint = hex.EncodeToString(sum[:])[:8]
	}
	_ = json.Unmarshal(body.Result["current_state"], &out.CurrentState)
	var primary, canary *struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	_ = json.Unmarshal(body.Result["primary_version"], &primary)
	_ = json.Unmarshal(body.Result["canary_version"], &canary)
	if primary != nil {
		out.PrimaryID, out.PrimaryName = primary.ID, primary.Version
	}
	out.HasCanary = canary != nil
	return out
}

// putServiceOutOfBand sends PUT /services-v2/apply directly and returns the status code and
// response detail (never the response body's tokens).
func putServiceOutOfBand(t *testing.T, body map[string]any) (int, string) {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	reqBody, err := provider.MarshalRequestBody(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = provider.DoRequestRaw(context.Background(), client, "PUT", "/api/v2/services-v2/apply", reqBody, 202)
	if err == nil {
		return 202, ""
	}
	var statusErr *provider.UnexpectedStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode, statusErr.Body
	}
	t.Fatalf("PUT apply: %v", err)
	return 0, ""
}

func waitParityRunning(t *testing.T, serviceID string) parityGET {
	t.Helper()
	time.Sleep(20 * time.Second)
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		g := getParityGET(t, serviceID)
		if g.CurrentState == "RUNNING" && !g.HasCanary {
			return g
		}
		time.Sleep(15 * time.Second)
	}
	t.Fatalf("service %s did not settle at RUNNING", serviceID)
	return parityGET{}
}

// TestAccServiceResource_RealInfra_InputsParity closes the service-level Gate 1 items of
// docs/decisions/service-inputs-parity on one service on the static VM cloud, with three cluster
// starts (create, one rollout, one rolled-back rollout):
//
//  1. Create with auth on, image_uri, gRPC names and env_vars: image_uri resolves to the base
//     image's build, GET has a token and no `config` key, and the next plan is empty.
//  2. Auth off, new name (one rollout): the token is cleared (G1-1).
//  3. Auth on with a broken import_path: the rollout is rolled back, and the new token survives
//     the rollback because auth is service-level (G1-1, G1-2).
//  4. The rolled-back name again: the translated 409, no rollout (G1-2); the versions list is
//     logged (G1-3).
//  5. Out-of-band PUT probes: the current primary's name with a changed config (G1-2.3), IN_PLACE
//     with a new name and auth flipped (G1-8), and the same new name and config sent twice so the
//     second lands mid-rollout (G1-2.4). Destroy then terminates that rollout without waiting.
//
// Run (one real service, about 80 min):
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 TF_LOG_PROVIDER=DEBUG TF_ACC_LOG_PATH=/tmp/svc-parity.log \
//	  go test ./internal/acctest -run 'TestAccServiceResource_RealInfra_InputsParity' -v -timeout 150m
func TestAccServiceResource_RealInfra_InputsParity(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloud := pinnedVMCloud(t)
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloud.ID)
	if err != nil {
		t.Fatalf("failed to create ephemeral test project scoped to cloud %s: %v", cloud.ID, err)
	}
	computeConfigName := UniqueName(t, "cc-for-service-parity")
	serviceName := UniqueName(t, "service-parity")

	configFor := func(auth bool, version, importPath string) string {
		return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q
  head_node = {
    instance_type = %[3]q
  }
}

resource "anyscale_service" "test" {
  name                     = %[4]q
  project_id               = %[5]q
  image_uri                = %[6]q
  compute_config_id        = anyscale_compute_config.test.config_id
  query_auth_token_enabled = %[7]t
  version_name             = %[8]q
  grpc_service_names       = ["tfacc.ParityService"]
  env_vars = {
    TFACC_PARITY = "1"
  }

  ray_serve_config = {
    applications = [
      {
        import_path = %[9]q
        runtime_env = {
          working_dir = "https://github.com/anyscale/first-service/archive/refs/heads/main.zip"
        }
      }
    ]
  }
}
`, computeConfigName, cloud.ID, instanceTypes.Small, serviceName, projectID, parityBaseImage, auth, version, importPath)
	}

	poller := &serviceSnapshotPoller{t: t}
	t.Cleanup(poller.stop)
	var serviceID, buildID, computeConfigID, firstToken string
	logGET := func(label string, check func(parityGET) error) resource.TestCheckFunc {
		return func(*terraform.State) error {
			g := getParityGET(t, serviceID)
			t.Logf("GATE1 %s: %s", label, g)
			return check(g)
		}
	}
	broken := configFor(true, "tfacc-v3", "main:tfacc_missing_app")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			{
				Config: configFor(true, "tfacc-v1", "main:app"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					CaptureResourceAttr("anyscale_service.test", "id", &serviceID),
					CaptureResourceAttr("anyscale_service.test", "build_id", &buildID),
					CaptureResourceAttr("anyscale_service.test", "compute_config_id", &computeConfigID),
					resource.TestCheckResourceAttr("anyscale_service.test", "build_id", "anyscaleray2561-slim-py312-cu129"),
					resource.TestCheckResourceAttr("anyscale_service.test", "query_auth_token_enabled", "true"),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "tfacc-v1"),
					logGET("1 create auth=true", func(g parityGET) error {
						firstToken = g.TokenFingerprint
						if g.TokenFingerprint == "" || g.HasConfigKey || g.PrimaryName != "tfacc-v1" {
							return fmt.Errorf("unexpected GET after create: %s", g)
						}
						return nil
					}),
				),
			},
			{
				PreConfig: func() { poller.start(serviceID) },
				Config:    configFor(false, "tfacc-v2", "main:app"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "query_auth_token_enabled", "false"),
					logGET("2 auth=false", func(g parityGET) error {
						if g.TokenFingerprint != "" || g.PrimaryName != "tfacc-v2" {
							return fmt.Errorf("auth=false: %s", g)
						}
						return nil
					}),
				),
			},
			{
				Config:      broken,
				ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`),
			},
			{
				PreConfig: func() {
					g := getParityGET(t, serviceID)
					t.Logf("GATE1 3 after rollback: %s (token differs from create's: %t)", g,
						g.TokenFingerprint != "" && g.TokenFingerprint != firstToken)
					logServiceVersions(t, serviceID)
				},
				Config:      broken,
				ExpectError: regexp.MustCompile(`(?s)Version Name Already Used.*"tfacc-v3"`),
			},
			{
				PreConfig: func() {
					poller.stop()
					probeServiceVersionRulesOutOfBand(t, serviceID, serviceName, projectID, buildID, computeConfigID)
				},
				Config:             broken,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// logServiceVersions logs GET /services-v2/{id}/versions (G1-3): does it list rolled-back
// versions?
func logServiceVersions(t *testing.T, serviceID string) {
	client, err := GetTestClient()
	if err != nil {
		t.Logf("GATE1 G1-3 skipped: %v", err)
		return
	}
	raw, err := provider.DoRequestRaw(context.Background(), client, "GET",
		fmt.Sprintf("/api/v2/services-v2/%s/versions?count=50", serviceID), nil, 200)
	if err != nil {
		t.Logf("GATE1 G1-3 versions list failed: %v", err)
		return
	}
	var body struct {
		Results []struct {
			ID           string `json:"id"`
			Version      string `json:"version"`
			CurrentState string `json:"current_state"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Logf("GATE1 G1-3 decode failed: %v", err)
		return
	}
	for _, v := range body.Results {
		t.Logf("GATE1 G1-3 version %s %s %s", v.ID, v.Version, v.CurrentState)
	}
}

// probeServiceVersionRulesOutOfBand runs the out-of-band PUT probes against a RUNNING service
// with bearer auth on and no rollout in flight.
func probeServiceVersionRulesOutOfBand(t *testing.T, serviceID, name, projectID, buildID, computeConfigID string) {
	t.Helper()
	before := getParityGET(t, serviceID)
	t.Logf("GATE1 5 probes start: %s", before)
	body := func(strategy, version string, auth bool, envMarker string) map[string]any {
		app := map[string]any{
			"import_path": "main:app",
			"runtime_env": map[string]any{
				"working_dir": "https://github.com/anyscale/first-service/archive/refs/heads/main.zip",
			},
		}
		if envMarker != "" {
			app["runtime_env"].(map[string]any)["env_vars"] = map[string]any{"TFACC_PROBE": envMarker}
		}
		return map[string]any{
			"name": name, "project_id": projectID, "build_id": buildID, "compute_config_id": computeConfigID,
			"rollout_strategy": strategy, "version": version,
			"ray_serve_config": map[string]any{"applications": []any{app}},
			"config": map[string]any{
				"access":    map[string]any{"use_bearer_token": auth},
				"protocols": map[string]any{"grpc": map[string]any{"enabled": true, "service_names": []any{"tfacc.ParityService"}}},
				"env_vars":  map[string]any{"TFACC_PARITY": "1"},
			},
		}
	}

	// G1-2.3: the current primary's name with a changed config.
	status, detail := putServiceOutOfBand(t, body("ROLLOUT", before.PrimaryName, true, "a"))
	t.Logf("GATE1 G1-2.3 current primary name %q, changed config: status=%d detail=%s", before.PrimaryName, status, detail)

	// G1-8: IN_PLACE with a changed ray_serve_config, a new name and auth flipped off.
	status, detail = putServiceOutOfBand(t, body("IN_PLACE", "tfacc-inplace", false, "b"))
	t.Logf("GATE1 G1-8 IN_PLACE new name + auth=false: status=%d detail=%s", status, detail)
	if status == 202 {
		after := waitParityRunning(t, serviceID)
		t.Logf("GATE1 G1-8 after in-place: %s (primary name unchanged=%t, token unchanged=%t)",
			after, after.PrimaryName == before.PrimaryName, after.TokenFingerprint == before.TokenFingerprint)
	}

	// G1-2.4: a new rollout, then the same name and config re-sent while it is in flight.
	status, detail = putServiceOutOfBand(t, body("ROLLOUT", "tfacc-v6", true, ""))
	t.Logf("GATE1 G1-2.4 first PUT tfacc-v6: status=%d detail=%s", status, detail)
	status, detail = putServiceOutOfBand(t, body("ROLLOUT", "tfacc-v6", true, ""))
	t.Logf("GATE1 G1-2.4 re-sent mid-rollout: status=%d detail=%s; %s", status, detail, getParityGET(t, serviceID))
}

// TestAccServiceResource_RealInfra_ImageURIRegistry closes G1-4 and G1-7 without a service:
// get_or_create on a registry URI twice, then with a registry secret, a GET of the resolved build,
// and the anyscale/image alias of an anonymous and of a named environment.
//
// Nothing is archived: archiving an environment registered from a URI blocks that URI for the
// whole organization (get_or_create then 409s), and get_or_create is idempotent, so the fixed
// URI and environment name below are reused by every run and nothing accumulates. Neither name
// carries a sweepable prefix, so the sweepers never archive them either.
//
// Run: TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest \
//
//	-run 'TestAccServiceResource_RealInfra_ImageURIRegistry' -v
func TestAccServiceResource_RealInfra_ImageURIRegistry(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	const (
		uri          = "docker.io/rayproject/ray:2.10.0"
		namedEnv     = "parity-probe-named-image"
		probeSecret  = "parity-probe-registry-secret"
		anonymousEnv = "docker-io-rayproject-ray-2-10-0"
	)
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	ctx := context.Background()
	type build struct {
		ID                  string `json:"id"`
		Status              string `json:"status"`
		DockerImageName     string `json:"docker_image_name"`
		Revision            int    `json:"revision"`
		ApplicationTemplate *struct {
			Name string `json:"name"`
		} `json:"application_template"`
	}
	resolve := func(body map[string]any) (*build, error) {
		reqBody, _ := provider.MarshalRequestBody(body)
		resp, err := provider.DoRequestAndParse[struct {
			Result build `json:"result"`
		}](ctx, client, "POST", "/api/v2/builds/get_or_create_build_from_image_uri", reqBody, 200)
		if err != nil {
			return nil, err
		}
		return &resp.Result, nil
	}
	getBuild := func(id string) build {
		raw, err := provider.DoRequestRaw(ctx, client, "GET", "/api/v2/builds/"+id, nil, 200, 201)
		if err != nil {
			t.Fatalf("GET build %s: %v", id, err)
		}
		var got struct {
			Result build `json:"result"`
		}
		_ = json.Unmarshal(raw, &got)
		return got.Result
	}
	alias := func(b build) string {
		if b.ApplicationTemplate == nil {
			return ""
		}
		return fmt.Sprintf("anyscale/image/%s:%d", b.ApplicationTemplate.Name, b.Revision)
	}

	first, err := resolve(map[string]any{"image_uri": uri})
	if err != nil {
		t.Fatalf("first get_or_create: %v", err)
	}
	t.Logf("GATE1 G1-4 first: id=%s status=%s docker_image_name=%s", first.ID, first.Status, first.DockerImageName)
	second, err := resolve(map[string]any{"image_uri": uri})
	if err != nil {
		t.Fatalf("second get_or_create: %v", err)
	}
	t.Logf("GATE1 G1-4 second: same_id=%t", second.ID == first.ID)
	withSecret, err := resolve(map[string]any{"image_uri": uri, "registry_login_secret": probeSecret})
	if err != nil {
		t.Logf("GATE1 G1-4 with registry_login_secret: error %v", err)
	} else {
		t.Logf("GATE1 G1-4 with registry_login_secret: id=%s new_id=%t", withSecret.ID, withSecret.ID != first.ID)
	}

	anon := getBuild(first.ID)
	if _, err := resolve(map[string]any{"image_uri": alias(anon)}); err != nil {
		t.Logf("GATE1 G1-7 anonymous environment %s, alias %s: %v", anonymousEnv, alias(anon), err)
	}

	// A named environment, as anyscale_container_image_registry or `anyscale image register`
	// creates: its alias must resolve to its build, and the build's GET must rebuild the alias.
	named, err := resolve(map[string]any{"image_uri": uri, "cluster_env_name": namedEnv})
	if err != nil {
		t.Fatalf("get_or_create for the named environment: %v", err)
	}
	namedBuild := getBuild(named.ID)
	viaAlias, err := resolve(map[string]any{"image_uri": alias(namedBuild)})
	if err != nil {
		t.Fatalf("get_or_create on the named alias %s: %v", alias(namedBuild), err)
	}
	t.Logf("GATE1 G1-7 named build %s: GET rebuilds alias %s, which resolves to the same build: %t",
		named.ID, alias(namedBuild), viaAlias.ID == named.ID)
}
