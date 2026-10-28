// Real-infrastructure tests for anyscale_runtime_env_package: the Gate 1 evidence in
// docs/decisions/service-working-dir-upload/README.md. Both upload to the pinned static VM
// cloud's data bucket. Nothing can delete those objects, but the fixtures are fixed, so repeated
// runs write the same keys instead of accumulating. Run one at a time:
//
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest \
//	  -run 'TestAccRuntimeEnvPackageResource_RealInfra$' -v -timeout 20m
//	TF_ACC=1 ANYSCALE_TEST_REAL_INFRA=1 go test ./internal/acctest \
//	  -run 'TestAccRuntimeEnvPackageResource_RealInfraService$' -v -timeout 240m
package acctest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// presignShape describes a presigned-URL response without its secret parts: the URL's scheme,
// host and query parameter NAMES only.
func presignShape(p *provider.CloudDataBucketPresignedURL) string {
	shape := "unparseable url"
	if u, err := url.Parse(p.URL); err == nil {
		var names []string
		for k := range u.Query() {
			names = append(names, k)
		}
		sort.Strings(names)
		shape = fmt.Sprintf("%s://%s/... query params %v", u.Scheme, u.Host, names)
	}
	return fmt.Sprintf("url_scheme=%q file_exists=%v file_size=%d bucket_path=%q file_uri=%q url=%s",
		p.URLScheme, p.FileExists, p.FileSize, p.BucketPath, p.FileURI, shape)
}

// writeRealInfraPkgFixture writes a Ray Serve app that reports its marker and whether the
// .gitignored .env file reached the cluster.
func writeRealInfraPkgFixture(t *testing.T, dir, marker string) {
	t.Helper()
	files := map[string]string{
		"app.py": `import os

from ray import serve

MARKER = "` + marker + `"


@serve.deployment
class Marker:
    def __call__(self, request) -> str:
        here = os.path.dirname(os.path.abspath(__file__))
        dotenv = os.path.exists(os.path.join(here, ".env"))
        return f"marker={MARKER} dotenv={dotenv}"


app = Marker.bind()
`,
		".gitignore": ".env\n",
		".env":       "SECRET=must-not-upload\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAccRuntimeEnvPackageResource_RealInfra covers G1-a/b/c (and G1-g/h read-only probes when
// such clouds exist in the org): the presigned-URL response shape for a missing object, a real
// upload and its READ re-check, the uploaded bytes, an empty re-plan, and cloud scoping.
func TestAccRuntimeEnvPackageResource_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)
	PreCheck(t)

	cloud := pinnedVMCloud(t)
	client, err := GetTestClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// G1-a: READ for an object that does not exist.
	missing, err := provider.RequestRuntimeEnvPackageURL(ctx, client, cloud.ID, "", "tfacc-gate1-probe-missing.zip", provider.CloudDataBucketRead)
	if err != nil {
		t.Fatalf("G1-a: %v", err)
	}
	t.Logf("G1-a READ (missing): %s", presignShape(missing))
	keyPattern := regexp.MustCompile(`^[^/]+/` + regexp.QuoteMeta(cloud.ID) + `/runtime_env_packages/tfacc-gate1-probe-missing\.zip$`)
	if missing.FileExists || missing.URLScheme != "http" || !keyPattern.MatchString(missing.BucketPath) ||
		missing.FileURI != "s3://"+missing.BucketName+"/"+missing.BucketPath {
		t.Fatalf("G1-a: unexpected shape: %s", presignShape(missing))
	}

	// G1-c: a cloud resource from another cloud is refused for this cloud.
	checkForeignCloudResourceRefused(t, ctx, client, cloud.ID)
	// G1-g / G1-h: read-only probes, only if the org already has such clouds.
	probeOtherCloudKinds(t, ctx, client, cloud.ID)

	dir := t.TempDir()
	writeRealInfraPkgFixture(t, dir, "gate1")
	config := fmt.Sprintf(`
resource "anyscale_runtime_env_package" "test" {
  source_dir = %q
  cloud_id   = %q
}
`, dir, cloud.ID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					a := s.RootModule().Resources["anyscale_runtime_env_package.test"].Primary.Attributes
					t.Logf("G1-b uploaded object: %s", a["uri"])
					if !strings.HasSuffix(a["uri"], "/"+a["relative_path"]) || !strings.HasSuffix(a["relative_path"], "/pkg_"+a["content_hash"]+".zip") {
						return fmt.Errorf("uri %q / relative_path %q / content_hash %q disagree", a["uri"], a["relative_path"], a["content_hash"])
					}
					// G1-b: the object now exists, and its bytes are the package.
					got, err := provider.RequestRuntimeEnvPackageURL(ctx, client, cloud.ID, "", "pkg_"+a["content_hash"]+".zip", provider.CloudDataBucketRead)
					if err != nil {
						return err
					}
					t.Logf("G1-b READ (after upload): %s", presignShape(got))
					if !got.FileExists || got.FileSize <= 0 || got.FileURI != a["uri"] {
						return fmt.Errorf("after upload: %s", presignShape(got))
					}
					body, err := downloadPresigned(ctx, got.URL)
					if err != nil {
						return err
					}
					if int64(len(body)) != got.FileSize {
						return fmt.Errorf("downloaded %d bytes, file_size says %d", len(body), got.FileSize)
					}
					return checkRealInfraZip(body, "gate1")
				},
			},
			{
				// Re-apply: nothing to plan, so nothing is uploaded again.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// checkRealInfraZip asserts the uploaded zip holds the app under one top-level directory and
// not the .gitignored .env.
func checkRealInfraZip(body []byte, marker string) error {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("downloaded object is not a zip: %w", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == "pkg/app.py" {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			if !strings.Contains(string(b), `MARKER = "`+marker+`"`) {
				return fmt.Errorf("pkg/app.py does not carry marker %q", marker)
			}
		}
	}
	if strings.Join(names, ",") != "pkg/.gitignore,pkg/app.py" {
		return fmt.Errorf("zip entries %v, want [pkg/.gitignore pkg/app.py]", names)
	}
	return nil
}

// downloadPresigned GETs a presigned URL. Errors never include the URL.
func downloadPresigned(ctx context.Context, presigned string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, presigned, nil)
	if err != nil {
		return nil, errors.New("build download request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("download: %v", ue.Err)
		}
		return nil, errors.New("download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// checkForeignCloudResourceRefused is G1-c: a WRITE request naming another cloud's cloud
// resource must 404. Nothing is written either way: the request only mints a URL. Without
// another cloud in the org, a nonexistent resource ID checks the same 404 branch
// (_get_cloud_resource_record answers both cases identically).
func checkForeignCloudResourceRefused(t *testing.T, ctx context.Context, client *provider.Client, cloudID string) {
	t.Helper()
	foreign, label := "", ""
	list, err := provider.DoRequestAndParse[struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}](ctx, client, http.MethodGet, "/api/v2/clouds?count=50", nil, http.StatusOK)
	if err != nil {
		t.Fatalf("G1-c: list clouds: %v", err)
	}
	for _, other := range list.Results {
		if other.ID == cloudID {
			continue
		}
		if id, found, err := findDefaultCloudResourceID(ctx, client, other.ID); err == nil && found {
			foreign, label = id, "a resource of another cloud"
			break
		}
	}
	if foreign == "" {
		foreign, label = "cldrsrc_tfaccdoesnotexist", "a nonexistent resource (no other cloud in this org)"
	}
	_, err = provider.RequestRuntimeEnvPackageURL(ctx, client, cloudID, foreign, "tfacc-gate1-probe-foreign.zip", provider.CloudDataBucketWrite)
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("G1-c: %s was not refused with 404: %v", label, err)
	}
	t.Logf("G1-c: %s is refused with 404", label)
}

// probeOtherCloudKinds runs a READ-only presign against a Kubernetes cloud (G1-g) and an
// Anyscale-hosted cloud (G1-h) if the org already has one. It creates nothing.
func probeOtherCloudKinds(t *testing.T, ctx context.Context, client *provider.Client, skipCloudID string) {
	t.Helper()
	list, err := provider.DoRequestAndParse[struct {
		Results []struct {
			ID           string `json:"id"`
			Provider     string `json:"provider"`
			ComputeStack string `json:"compute_stack"`
			IsAioa       bool   `json:"is_aioa"`
		} `json:"results"`
	}](ctx, client, http.MethodGet, "/api/v2/clouds?count=50", nil, http.StatusOK)
	if err != nil {
		t.Logf("G1-g/h: could not list clouds: %v", err)
		return
	}
	var k8sDone, hostedDone bool
	for _, c := range list.Results {
		if c.ID == skipCloudID {
			continue
		}
		var label string
		switch {
		case c.IsAioa && !hostedDone:
			label, hostedDone = "G1-h (hosted)", true
		case c.ComputeStack == "K8S" && !k8sDone:
			label, k8sDone = "G1-g (K8S, READ only)", true
		default:
			continue
		}
		p, err := provider.RequestRuntimeEnvPackageURL(ctx, client, c.ID, "", "tfacc-gate1-probe-missing.zip", provider.CloudDataBucketRead)
		if err != nil {
			t.Logf("%s %s/%s: error: %v", label, c.Provider, c.ComputeStack, err)
			continue
		}
		t.Logf("%s %s/%s: %s", label, c.Provider, c.ComputeStack, presignShape(p))
	}
	if !k8sDone {
		t.Logf("G1-g: no Kubernetes cloud in this org; not probed")
	}
	if !hostedDone {
		t.Logf("G1-h: no Anyscale-hosted cloud in this org; not probed")
	}
}

// writeRealInfraServiceFixture writes a package whose only module is app_<marker>.py, so an
// import_path of app_<marker>:app can load only from this step's upload. The module refuses to
// import if the .gitignored .env reached the cluster.
func writeRealInfraServiceFixture(t *testing.T, dir, marker string) {
	t.Helper()
	old, _ := filepath.Glob(filepath.Join(dir, "app_*.py"))
	for _, p := range old {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"app_" + marker + ".py": `import os

from ray import serve

if os.path.exists(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".env")):
    raise RuntimeError(".env was uploaded: .gitignore was not honored")


@serve.deployment
class Marker:
    def __call__(self, request) -> str:
        return "marker=` + marker + `"


app = Marker.bind()
`,
		".gitignore": ".env\n",
		".env":       "SECRET=must-not-upload\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// checkDeployedVersion asserts that the service's live primary version is the one this step
// deployed: its ray_serve_config names wantImport and carries runtime_env[wantKey] == wantValue,
// its id differs from prevVersionID (when set), and no canary is in flight. RUNNING alone is not
// enough: a failed rollout rolls back and still reports RUNNING.
func checkDeployedVersion(svc *provider.ServiceResult, wantImport, wantKey, wantValue, prevVersionID string) error {
	if svc.PrimaryVersion == nil {
		return errors.New("service has no primary_version")
	}
	if svc.CanaryVersion != nil {
		return fmt.Errorf("canary_version %s is still present", svc.CanaryVersion.ID)
	}
	if prevVersionID != "" && svc.PrimaryVersion.ID == prevVersionID {
		return fmt.Errorf("primary_version is still %s, the previous step's version", prevVersionID)
	}
	var rsc struct {
		Applications []struct {
			ImportPath string         `json:"import_path"`
			RuntimeEnv map[string]any `json:"runtime_env"`
		} `json:"applications"`
	}
	if err := json.Unmarshal(svc.PrimaryVersion.RayServeConfig, &rsc); err != nil {
		return fmt.Errorf("decode primary_version.ray_serve_config: %w", err)
	}
	if len(rsc.Applications) != 1 {
		return fmt.Errorf("primary_version has %d applications, want 1", len(rsc.Applications))
	}
	app := rsc.Applications[0]
	if app.ImportPath != wantImport {
		return fmt.Errorf("primary_version import_path %q, want %q", app.ImportPath, wantImport)
	}
	if got := app.RuntimeEnv[wantKey]; got != wantValue {
		return fmt.Errorf("primary_version runtime_env.%s = %v, want %q", wantKey, got, wantValue)
	}
	return nil
}

// TestAccRuntimeEnvPackageResource_RealInfraService is G1-e and G1-f: a service deploys the
// uploaded s3:// URI, a content change rolls out the new package, and the package referenced as
// relative_working_dir also deploys on a single-resource cloud. Each step's module exists only in
// that step's upload, and the live primary version is checked to be the one the step deployed.
// The service's endpoint is not checked over HTTP: the static cloud's load balancer is not
// reachable from every network that runs this test. The service is destroyed at the end, and
// CheckDestroy confirms GET returns 404.
func TestAccRuntimeEnvPackageResource_RealInfraService(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)
	PreCheck(t)

	cloud := pinnedVMCloud(t)
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skip("No valid instance types on the resolved cloud")
	}
	projectID, _, err := CreateEphemeralTestProjectForCloud(t, cloud.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	ccName := UniqueName(t, "cc-pkg")
	svcName := UniqueName(t, "service-pkg")
	dir := t.TempDir()

	config := func(marker, workingDirKey, workingDirAttr string) string {
		return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q
  head_node = {
    instance_type = %[3]q
  }
}

resource "anyscale_runtime_env_package" "app" {
  source_dir = %[4]q
  cloud_id   = anyscale_compute_config.test.cloud_id
}

resource "anyscale_service" "test" {
  name              = %[5]q
  project_id        = %[6]q
  build_id          = "anyscaleray2561-slim-py312-cu129"
  compute_config_id = anyscale_compute_config.test.config_id
  rollout_strategy  = "ROLLOUT"
  timeouts {
    create = "60m"
    update = "60m"
  }
  ray_serve_config = {
    applications = [
      {
        import_path = "app_%[9]s:app"
        runtime_env = {
          %[7]s = anyscale_runtime_env_package.app.%[8]s
        }
      }
    ]
  }
}
`, ccName, cloud.ID, instanceTypes.Small, dir, svcName, projectID, workingDirKey, workingDirAttr, marker)
	}

	var prevVersionID string
	step := func(marker, key, attr string) resource.TestStep {
		return resource.TestStep{
			PreConfig: func() { writeRealInfraServiceFixture(t, dir, marker) },
			Config:    config(marker, key, attr),
			Check: CaptureServiceDiagnosticsOnFailure(t, "anyscale_service.test", resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("anyscale_service.test", "current_state", "RUNNING"),
				func(s *terraform.State) error {
					pkg := s.RootModule().Resources["anyscale_runtime_env_package.app"].Primary.Attributes
					t.Logf("uploaded object (%s): %s", marker, pkg["uri"])
					id := s.RootModule().Resources["anyscale_service.test"].Primary.ID
					svc, err := provider.DoRequestAndParse[provider.ServiceResponse](context.Background(), mustTestClient(t),
						http.MethodGet, "/api/v2/services-v2/"+id, nil, http.StatusOK)
					if err != nil {
						return err
					}
					if err := checkDeployedVersion(&svc.Result, "app_"+marker+":app", key, pkg[attr], prevVersionID); err != nil {
						return err
					}
					t.Logf("step %s: primary_version %s runs app_%s:app with runtime_env.%s = %s",
						marker, svc.Result.PrimaryVersion.ID, marker, key, pkg[attr])
					prevVersionID = svc.Result.PrimaryVersion.ID
					return nil
				},
			)),
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_service", "/api/v2/services-v2/%s"),
		Steps: []resource.TestStep{
			// G1-e: deploy from the uploaded s3:// URI.
			step("v1", "working_dir", "uri"),
			// G1-e: a content change uploads a new object and rolls it out.
			withPlanChecks(step("v2", "working_dir", "uri"),
				plancheck.ExpectResourceAction("anyscale_runtime_env_package.app", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
			),
			// G1-f: relative_working_dir on a single-resource cloud.
			step("v3", "relative_working_dir", "relative_path"),
		},
	})
}

// TestCheckDeployedVersionCatchesRollback runs without credentials: it proves the real-infra
// step check rejects the shapes a failed-then-rolled-back rollout leaves behind, which still
// report RUNNING.
func TestCheckDeployedVersionCatchesRollback(t *testing.T) {
	version := func(id, importPath, key, value string) *provider.ServiceVersionResult {
		rsc := fmt.Sprintf(`{"applications":[{"import_path":%q,"runtime_env":{%q:%q}}]}`, importPath, key, value)
		return &provider.ServiceVersionResult{ID: id, RayServeConfig: json.RawMessage(rsc)}
	}
	const v2URI = "s3://b/org/cld/runtime_env_packages/pkg_2.zip"
	good := &provider.ServiceResult{PrimaryVersion: version("ver2", "app_v2:app", "working_dir", v2URI)}
	if err := checkDeployedVersion(good, "app_v2:app", "working_dir", v2URI, "ver1"); err != nil {
		t.Fatalf("good deploy rejected: %v", err)
	}
	bad := map[string]*provider.ServiceResult{
		"rolled back to the previous version": {PrimaryVersion: version("ver1", "app_v1:app", "working_dir", "s3://b/pkg_1.zip")},
		"same version id":                     {PrimaryVersion: version("ver1", "app_v2:app", "working_dir", v2URI)},
		"old package":                         {PrimaryVersion: version("ver2", "app_v2:app", "working_dir", "s3://b/pkg_1.zip")},
		"canary in flight":                    {PrimaryVersion: version("ver2", "app_v2:app", "working_dir", v2URI), CanaryVersion: version("ver3", "x", "y", "z")},
		"no primary version":                  {},
	}
	for name, svc := range bad {
		if err := checkDeployedVersion(svc, "app_v2:app", "working_dir", v2URI, "ver1"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func withPlanChecks(s resource.TestStep, checks ...plancheck.PlanCheck) resource.TestStep {
	s.ConfigPlanChecks.PreApply = append(s.ConfigPlanChecks.PreApply, checks...)
	return s
}

func mustTestClient(t *testing.T) *provider.Client {
	t.Helper()
	c, err := GetTestClient()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
