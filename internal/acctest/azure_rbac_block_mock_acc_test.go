package acctest

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock-server tests for the Azure RBAC block: on an Azure control plane the RBAC
// resources refuse create, update and import at plan time, still destroy, and plan
// empty on unchanged existing state; anyscale_cloud refuses a configured
// auto_add_user; the RBAC data sources refuse to read. Every other surface keeps
// working.
//
// Mock servers listen on 127.0.0.1, which never classifies as Azure, so each test
// injects the classification per provider instance. The factory builds a fresh
// provider for every Terraform command and reads the test's own atomic flag, so a
// step's PreConfig can switch the classification between steps without a package
// variable that parallel tests would race on.

var azureRBACBlocked = regexp.MustCompile(`RBAC Is Managed in Azure`)

func azureClassifiedFactories(azure *atomic.Bool) map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": func() (tfprotov6.ProviderServer, error) {
			return providerserver.NewProtocol6WithError(provider.NewFrameworkWithAzureClassification("test", azure.Load())())()
		},
	}
}

// azureBlockedSteps is the shared step sequence for a blocked resource:
//
//	(a) Azure, create          -> refused at plan
//	(b) not Azure, create      -> created
//	(c) Azure, same config     -> empty plan, no error (an existing user's first plan after upgrading)
//	(d) Azure, changed config  -> refused at plan
//	(e) Azure, import          -> refused
//	(f) Azure, resource removed -> destroyed
func azureBlockedSteps(azure *atomic.Bool, addr, providerBlock, config, changed string) []resource.TestStep {
	return []resource.TestStep{
		{
			// PlanOnly here and in (d) pins the refusal to plan time: a refusal that
			// only surfaced in the apply-time re-plan (the create half of a
			// replacement) would leave a non-empty plan and fail the step.
			PreConfig:   func() { azure.Store(true) },
			Config:      config,
			PlanOnly:    true,
			ExpectError: azureRBACBlocked,
		},
		{
			PreConfig: func() { azure.Store(false) },
			Config:    config,
		},
		{
			PreConfig: func() { azure.Store(true) },
			Config:    config,
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		},
		{
			Config:      changed,
			PlanOnly:    true,
			ExpectError: azureRBACBlocked,
		},
		{
			// The import runs in a throwaway working directory, so the managed
			// resource from step (b) does not collide with it. The ID is the one
			// in state, so without the block the import would succeed.
			Config:       config,
			ResourceName: addr,
			ImportState:  true,
			ExpectError:  azureRBACBlocked,
		},
		{
			Config: providerBlock,
		},
	}
}

// azureExpectGone fails if addr is still in state after the destroy step.
func azureExpectGone(addr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if _, ok := s.RootModule().Resources[addr]; ok {
			return fmt.Errorf("%s still in state after it was removed from config", addr)
		}
		return nil
	}
}

func withAzureDestroyCheck(steps []resource.TestStep, addr string) []resource.TestStep {
	steps[len(steps)-1].Check = azureExpectGone(addr)
	return steps
}

func TestAccRoleBindingResource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleBindingLifecycleMock(t)
	var azure atomic.Bool
	// role_id is a literal, not data.anyscale_role: that data source is blocked on
	// Azure too and would mask whether the resource itself refuses.
	cfg := func(roleID string) string {
		return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id = %q
  role_id       = %q
  cloud_id      = %q
}
`, rbMockGroup, roleID, rbMockCloud)
	}
	const addr = "anyscale_role_binding.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		CheckDestroy: func(*terraform.State) error {
			if n := m.bindingCount(); n != 0 {
				return fmt.Errorf("backend still holds %d bindings after destroy", n)
			}
			return nil
		},
		Steps: withAzureDestroyCheck(azureBlockedSteps(&azure, addr, m.providerBlock(), cfg(rbMockViewer), cfg(rbMockWriter)), addr),
	})
}

func TestAccUserGroupResource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	var azure atomic.Bool
	cfg := func(name string) string {
		return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_user_group" "test" {
  name = %q
}
`, name)
	}
	const addr = "anyscale_user_group.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		CheckDestroy: func(*terraform.State) error {
			if n := m.groupCount(); n != 0 {
				return fmt.Errorf("%d group(s) left in the backend after destroy", n)
			}
			return nil
		},
		Steps: withAzureDestroyCheck(azureBlockedSteps(&azure, addr, m.providerBlock(), cfg("tfacc-azure-ug"), cfg("tfacc-azure-ug-renamed")), addr),
	})
}

func TestAccUserGroupMembersResource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t, ugUserA, ugUserB)
	user := "user"
	// The group is seeded out of band: anyscale_user_group is blocked on Azure
	// too, so declaring it here would mask whether the members resource refuses.
	groupID := m.seedGroup("tfacc-azure-ugm", &user)
	var azure atomic.Bool
	cfg := func(members string) string {
		return m.providerBlock() + fmt.Sprintf(`
resource "anyscale_user_group_members" "test" {
  user_group_id = %q
  members       = [%s]
}
`, groupID, members)
	}
	const addr = "anyscale_user_group_members.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		CheckDestroy: func(*terraform.State) error {
			ids, ok := m.memberIDs(groupID)
			if !ok {
				return fmt.Errorf("group %s was deleted; only its members should be removed", groupID)
			}
			if len(ids) != 0 {
				return fmt.Errorf("group %s still has members %v after destroy", groupID, ids)
			}
			return nil
		},
		Steps: withAzureDestroyCheck(azureBlockedSteps(&azure, addr, m.providerBlock(),
			cfg(`"alice@example.com"`), cfg(`"alice@example.com", "bob@example.com"`)), addr),
	})
}

func TestAccCloudAccessResource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	// The converted-org wrapper with converted=false is the ordinary legacy mock,
	// including the userinfo and collaborator routes ModifyPlan's own guards read
	// on the non-Azure create.
	m := newCloudAccessConvertedMock(t)
	var azure atomic.Bool
	providerBlock := testAccProviderBlock(m.server.URL)
	const addr = "anyscale_cloud_access.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		CheckDestroy: func(*terraform.State) error {
			if e := m.memberEmails(); len(e) != 0 {
				return fmt.Errorf("cloud still has members %v after destroy", e)
			}
			return nil
		},
		Steps: withAzureDestroyCheck(azureBlockedSteps(&azure, addr, providerBlock,
			providerBlock+cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com"),
			providerBlock+cloudAccessConvertedHCL(false, "alice@example.com")), addr),
	})
}

func TestAccOrganizationUserRoleResource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockOrgUserRoleServer(t)
	var azure atomic.Bool
	providerBlock := testAccProviderBlock(server.URL)
	cfg := func(baseRole string) string {
		return providerBlock + fmt.Sprintf(`
resource "anyscale_organization_user_role" "test" {
  email     = %q
  base_role = %q
}
`, mock.email, baseRole)
	}
	const addr = "anyscale_organization_user_role.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps:                    withAzureDestroyCheck(azureBlockedSteps(&azure, addr, providerBlock, cfg("collaborator"), cfg("owner")), addr),
	})
	// Only the non-Azure create may write; the refused update must not have.
	if base, _ := mock.snapshot(); base != "collaborator" {
		t.Errorf("backend base_role = %q, want collaborator: a refused plan must not write", base)
	}
}

// cloudConfigWithoutAutoAddUser is convOrgCloudConfig with auto_add_user omitted,
// so it plans as the schema default without being configured.
func cloudConfigWithoutAutoAddUser(t *testing.T, url string) string {
	t.Helper()
	const line = "  auto_add_user  = false\n"
	cfg := convOrgCloudConfig(url, false)
	if !strings.Contains(cfg, line) {
		t.Fatalf("convOrgCloudConfig no longer contains %q; update this helper", line)
	}
	return strings.Replace(cfg, line, "", 1)
}

func TestAccCloudResource_AzureAutoAddUserBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgCloudServer(t)
	var azure atomic.Bool
	without := cloudConfigWithoutAutoAddUser(t, server.URL)
	with := convOrgCloudConfig(server.URL, true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { azure.Store(true) },
				Config:      with,
				PlanOnly:    true,
				ExpectError: azureRBACBlocked,
			},
			{
				PreConfig: func() { azure.Store(false) },
				Config:    without,
			},
			{
				PreConfig: func() { azure.Store(true) },
				Config:    without,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config:      with,
				PlanOnly:    true,
				ExpectError: azureRBACBlocked,
			},
			{
				Config: testAccProviderBlock(server.URL),
				Check:  azureExpectGone("anyscale_cloud.test"),
			},
		},
	})
	if n := mock.puts(); n != 0 {
		t.Errorf("auto_add_user PUTs = %d, want 0: every step that would send one was refused at plan", n)
	}
}

// Data sources read at plan when their arguments are known, so the refusal
// surfaces at plan. With an unknown argument Terraform Core defers the read to
// apply, and the same error surfaces there; that is how Core schedules data
// sources, not a gap in the block. These cases use known arguments.

func azureDataSourceSteps(azure *atomic.Bool, config string) []resource.TestStep {
	return []resource.TestStep{
		{
			PreConfig:   func() { azure.Store(true) },
			Config:      config,
			PlanOnly:    true,
			ExpectError: azureRBACBlocked,
		},
		{
			PreConfig: func() { azure.Store(false) },
			Config:    config,
		},
	}
}

func TestAccRoleDataSource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newRoleBindingLifecycleMock(t)
	var azure atomic.Bool
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps: azureDataSourceSteps(&azure, m.providerBlock()+`
data "anyscale_role" "test" {
  name = "project_viewer"
}
`),
	})
}

func TestAccUserGroupDataSource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	user := "user"
	m.seedGroup("tfacc-azure-ugds", &user)
	var azure atomic.Bool
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps: azureDataSourceSteps(&azure, m.providerBlock()+`
data "anyscale_user_group" "test" {
  name = "tfacc-azure-ugds"
}
`),
	})
}

func TestAccUserGroupsDataSource_AzureBlocked_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newUserGroupMockServer(t)
	user := "user"
	m.seedGroup("tfacc-azure-ugsds", &user)
	var azure atomic.Bool
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
		Steps: azureDataSourceSteps(&azure, m.providerBlock()+`
data "anyscale_user_groups" "test" {
  source = "user"
}
`),
	})
}

// TestAccAzureClassificationNegativeSetResource_MockServer: under the Azure
// classification every non-RBAC surface still creates, plans empty and destroys.
func TestAccAzureClassificationNegativeSetResource_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	for _, tc := range []struct {
		name   string
		config func(t *testing.T) string
	}{
		{"organization_user", func(t *testing.T) string {
			server, _ := newMockCollaboratorLifecycleServer(t)
			return testAccProviderBlock(server.URL) + `
resource "anyscale_organization_user" "test" {
  email = "lifecycle@example.com"
}
`
		}},
		{"organization_invitation", func(t *testing.T) string {
			server, _ := newMockInvitationServer(t, false)
			return testAccProviderBlock(server.URL) + `
resource "anyscale_organization_invitation" "test" {
  email = "azure-negative@example.com"
}
`
		}},
		{"project", func(t *testing.T) string {
			server := newMockProjectServer(t)
			return testAccProviderBlock(server.URL) + `
resource "anyscale_project" "test" {
  name        = "azure-negative-project"
  cloud_id    = "cld_mock"
  description = "azure negative set"
}
`
		}},
		{"cloud_auto_add_user_null", func(t *testing.T) string {
			server, _ := newConvOrgCloudServer(t)
			return cloudConfigWithoutAutoAddUser(t, server.URL)
		}},
		{"compute_config", func(t *testing.T) string {
			server := newMockComputeConfigServer(t)
			return testAccProviderBlock(server.URL) + `
resource "anyscale_compute_config" "test" {
  name     = "azure-negative-cc"
  cloud_id = "cld_mock_cc"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
      resources = {
        CPU = 2
      }
    }
  ]
}
`
		}},
		{"container_image_registry", func(t *testing.T) string {
			const (
				templateID = "apptemp_azure_negative_mock"
				buildID    = "bld_azure_negative_mock"
				name       = "tfacc-azure-negative"
				imageURI   = "123456789012.dkr.ecr.us-west-2.amazonaws.com/tfacc-azure-negative:v1"
				rayVersion = "2.44.0"
				digest     = "sha256:azurenegativemock000000000000000000000000000000000000000000000"
			)
			server := newRegistryLifecycleMockServer(t, templateID, buildID, name, imageURI, rayVersion, digest)
			return testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_container_image_registry" "test" {
  name        = %q
  image_uri   = %q
  ray_version = %q
}
`, name, imageURI, rayVersion)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var azure atomic.Bool
			azure.Store(true)
			config := tc.config(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: azureClassifiedFactories(&azure),
				Steps: []resource.TestStep{
					// Create; the framework's post-apply plan must be empty.
					{Config: config},
					{
						Config: config,
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
						},
					},
				},
			})
		})
	}
}
