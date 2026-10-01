package acctest

// Create-only inputs that import does not recover (registry_login_secret,
// cloud credentials) are null after `terraform import`. A config that declares
// one must then plan an in-place update that records the value without sending
// it, not a replacement of the live object. These tests drive
// RequiresReplaceUnlessUnrecoverable through real plans against mock servers:
//
//   - cold import, then declare the value: Update, nothing written, empty plan
//     afterwards; changing it again: Replace
//   - created without the value, then add it: Replace (the Create marker in
//     private state is what tells the two cases apart)
//   - change a non-null value: Replace

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// newWriteCountingProxy fronts inner and counts every request that is not a
// GET, so a step can assert that apply sent nothing to the API.
func newWriteCountingProxy(t *testing.T, inner *httptest.Server) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatalf("parse inner URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &writes
}

func expectWrites(writes *atomic.Int32, want int32) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := writes.Load(); got != want {
			return fmt.Errorf("non-GET requests to the API = %d, want %d", got, want)
		}
		return nil
	}
}

const (
	loginSecretTemplateID = "apptemp_login_secret_mock"
	loginSecretBuildID    = "bld_login_secret_mock"
	loginSecretName       = "tfacc-login-secret-mock"
	loginSecretImageURI   = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-login-secret:v1"
	loginSecretRayVersion = "2.44.0"
	loginSecretAddr       = "anyscale_container_image_registry.test"
)

func loginSecretConfig(serverURL, secret string) string {
	secretLine := ""
	if secret != "" {
		secretLine = fmt.Sprintf("  registry_login_secret = %q\n", secret)
	}
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %[1]q
  image_uri   = %[2]q
  ray_version = %[3]q
%[4]s}
`, loginSecretName, loginSecretImageURI, loginSecretRayVersion, secretLine)
}

func newLoginSecretServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	inner := newRegistryLifecycleMockServer(t, loginSecretTemplateID, loginSecretBuildID, loginSecretName,
		loginSecretImageURI, loginSecretRayVersion, "sha256:loginsecretmock000000000000000000000000000000000000000000000000")
	return newWriteCountingProxy(t, inner)
}

func TestAccContainerImageRegistryResource_LoginSecretAdoptedAfterColdImport_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, writes := newLoginSecretServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Cold import: the image already exists in the mock; nothing was created here.
				ResourceName:       loginSecretAddr,
				ImportState:        true,
				ImportStateId:      loginSecretTemplateID,
				ImportStatePersist: true,
				Config:             loginSecretConfig(server.URL, ""),
			},
			{
				Config: loginSecretConfig(server.URL, "my-registry-secret"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(loginSecretAddr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(loginSecretAddr, "registry_login_secret", "my-registry-secret"),
					resource.TestCheckResourceAttr(loginSecretAddr, "id", loginSecretTemplateID),
					expectWrites(writes, 0),
				),
			},
			{
				// Now the value is in state, so a real change replaces as before.
				Config: loginSecretConfig(server.URL, "other-registry-secret"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(loginSecretAddr, plancheck.ResourceActionReplace)},
				},
			},
		},
	})
}

func TestAccContainerImageRegistryResource_LoginSecretAddedAfterCreateReplaces_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newLoginSecretServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: loginSecretConfig(server.URL, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(loginSecretAddr, plancheck.ResourceActionCreate)},
				},
			},
			{
				Config: loginSecretConfig(server.URL, "my-registry-secret"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(loginSecretAddr, plancheck.ResourceActionReplace)},
				},
			},
		},
	})
}

func TestAccContainerImageRegistryResource_LoginSecretChangeReplaces_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newLoginSecretServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: loginSecretConfig(server.URL, "my-registry-secret"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(loginSecretAddr, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: loginSecretConfig(server.URL, "other-registry-secret"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(loginSecretAddr, plancheck.ResourceActionReplace)},
				},
			},
		},
	})
}

const credentialsCloudAddr = "anyscale_cloud.test"

func credentialsCloudConfig(serverURL, credentials string) string {
	credentialsLine := ""
	if credentials != "" {
		credentialsLine = fmt.Sprintf("  credentials = %q\n", credentials)
	}
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
%s
  aws_config {
    vpc_id = "vpc-realct123"
    subnet_ids_to_az = {
      "subnet-realct1" = "us-east-2a"
      "subnet-realct2" = "us-east-2b"
    }
    security_group_ids        = ["sg-realct1"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id               = "real-external-id-ct"
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`, credentialsLine)
}

func TestAccCloudResource_CredentialsAdoptedAfterColdImport_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	inner, mock := newMockCloudCreateTimeServer(t)
	server, writes := newWriteCountingProxy(t, inner)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       credentialsCloudAddr,
				ImportState:        true,
				ImportStateId:      mock.cloudID,
				ImportStatePersist: true,
				Config:             credentialsCloudConfig(server.URL, ""),
			},
			{
				Config: credentialsCloudConfig(server.URL, "arn:aws:iam::123456789012:role/real-crossaccount"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(credentialsCloudAddr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(credentialsCloudAddr, "credentials", "arn:aws:iam::123456789012:role/real-crossaccount"),
					expectWrites(writes, 0),
				),
			},
		},
	})
}

func TestAccCloudResource_CredentialsAddedAfterCreateReplaces_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newMockCloudCreateTimeServer(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: credentialsCloudConfig(server.URL, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(credentialsCloudAddr, plancheck.ResourceActionCreate)},
				},
			},
			{
				Config: credentialsCloudConfig(server.URL, "arn:aws:iam::123456789012:role/real-crossaccount"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(credentialsCloudAddr, plancheck.ResourceActionReplace)},
				},
			},
		},
	})
}

// newBuildImportMockServer serves a pre-existing container image build for a
// cold import. projectIDJSON is the raw JSON for the template's project_id:
// a quoted ID, or null for an image created without a project (the API sends
// the key either way).
func newBuildImportMockServer(t *testing.T, templateID, buildID, projectIDJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}
	mux.HandleFunc("/api/v2/application_templates/"+templateID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"result": {
			"id": %[1]q, "name": "tfacc-build-import-mock", "creator_id": "user_mock",
			"created_at": "2024-01-01T00:00:00Z", "anonymous": false, "is_default": false,
			"project_id": %[2]s, "archived_at": null,
			"latest_build": {"id": %[3]q, "revision": 1, "status": "succeeded"}
		}}`, templateID, projectIDJSON, buildID))
	})
	// The real API answers GET builds/{id} with 201.
	mux.HandleFunc("/api/v2/builds/"+buildID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"result": {
			"id": %[1]q, "application_template_id": %[2]q,
			"docker_image_name": "anyscale/image/tfacc-build-import-mock:1",
			"revision": 1, "creator_id": "user_mock", "status": "succeeded",
			"created_at": "2024-01-01T00:00:00Z", "last_modified_at": "2024-01-01T00:00:00Z",
			"is_byod": false, "digest": "sha256:buildimportmock00000000000000000000000000000000000000000000000000"
		}}`, buildID, templateID))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testAccBuildImportProjectID(t *testing.T, projectIDJSON string, check func(attrs map[string]string) error) {
	const templateID = "apptemp_build_import_mock"
	server := newBuildImportMockServer(t, templateID, "bld_build_import_mock", projectIDJSON)
	config := testAccProviderBlock(server.URL) + `
resource "anyscale_container_image_build" "test" {
  name          = "tfacc-build-import-mock"
  containerfile = "FROM anyscale/ray:2.53.0-slim-py312"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			ResourceName:       "anyscale_container_image_build.test",
			ImportState:        true,
			ImportStateId:      templateID,
			ImportStatePersist: true,
			Config:             config,
			ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 {
					return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
				}
				return check(states[0].Attributes)
			},
		}},
	})
}

func TestAccContainerImageBuildResource_ImportRecoversProjectID_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccBuildImportProjectID(t, `"prj_build_import_mock"`, func(attrs map[string]string) error {
		if got := attrs["project_id"]; got != "prj_build_import_mock" {
			return fmt.Errorf("imported project_id = %q, want %q", got, "prj_build_import_mock")
		}
		return nil
	})
}

func TestAccContainerImageBuildResource_ImportLeavesUnsetProjectIDNull_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	testAccBuildImportProjectID(t, `null`, func(attrs map[string]string) error {
		if got, ok := attrs["project_id"]; ok && got != "" {
			return fmt.Errorf("imported project_id = %q, want null for an image created without a project", got)
		}
		return nil
	})
}
