package acctest

// TestAccIAMOrganizationUserResource_MovedFromOldType_MockServer proves a
// moved{} block upgrades state written under the pre-rename type name
// anyscale_organization_user to anyscale_iam_organization_user: the moved
// state lands on the new type, plans no-op against the new provider, and
// settles to an empty plan.
//
// Step 1 creates under the OLD type name through a wrapper that registers
// ONLY the old name: that is exactly the position an existing user is in
// after upgrading the provider. The wrapper overrides Metadata alone - schema,
// CRUD, private-state and self-heal behavior are the production resource's,
// so the state carried into step 2 is production-shaped, private membership
// record included. Step 2 runs the real provider, whose schema does not
// register the old name, so the no-op plan here means Core accepted the
// cross-type move via MoveResourceState rather than anything served by the
// old registration. Step 3 re-plans the same config and must see an empty
// plan.
//
// The pre-rename provider address Core reports inside resource.Test is
// registry.terraform.io/hashicorp/anyscale (harness keying), so exercising
// the production address gate requires it to compare only the provider TYPE
// segment - last '/'-segment == "anyscale" - which is how the mover gates.
// Per-step ProtoV6ProviderFactories are required here because plugin-testing
// makes the whole TestCase use them when any step does.

import (
	"context"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// preRenameOrganizationUserResource re-registers the real membership resource
// under its pre-rename type name. Only Metadata is overridden; every other
// method - Schema, Create/Read/Update/Delete, Configure, ImportState - is the
// promoted production implementation.
type preRenameOrganizationUserResource struct {
	*provider.OrganizationUserResource
}

func (r *preRenameOrganizationUserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	// Deliberately the OLD, removed type name: the source of the moved block.
	resp.TypeName = req.ProviderTypeName + "_organization_user"
}

// preRenameProvider embeds the real provider and registers ONLY the
// pre-rename organization_user resource. Metadata/Schema/Configure promote,
// so the config's provider block (api_url, token) configures the client
// exactly as in production tests.
type preRenameProvider struct {
	fwprovider.Provider
}

func (p *preRenameProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource {
			r := provider.NewOrganizationUserResource()
			inner, ok := r.(*provider.OrganizationUserResource)
			if !ok {
				// Fail construction loudly rather than nil-deref at test time.
				panic("provider.NewOrganizationUserResource no longer returns *provider.OrganizationUserResource; update the moved-state test wrapper")
			}
			return &preRenameOrganizationUserResource{OrganizationUserResource: inner}
		},
	}
}

// newPreRenameProviderFactories returns per-step factories whose provider
// registers only the old type name.
func newPreRenameProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	preRename := &preRenameProvider{Provider: realTestProvider()}
	return map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": providerserver.NewProtocol6WithError(preRename),
	}
}

// realTestProviderFactories matches the suite-wide ProtoV6ProviderFactories
// helper, declared per step: plugin-testing requires ALL steps to declare
// factories when any step does, and none on TestCase.
func realTestProvider() fwprovider.Provider {
	return provider.NewFramework("test")()
}

func TestAccIAMOrganizationUserResource_MovedFromOldType_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mockServer := newMockCollaboratorLifecycleServer(t)
	const newAddr = "anyscale_iam_organization_user.test"
	const email = "lifecycle@example.com"

	movedConfig := testAccProviderBlock(httpServer.URL) + `
resource "anyscale_iam_organization_user" "test" {
  email = "lifecycle@example.com"
}

moved {
  from = anyscale_organization_user.test
  to   = anyscale_iam_organization_user.test
}
`

	newOnlyFactories := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": providerserver.NewProtocol6WithError(realTestProvider()),
	}

	tfresource.Test(t, tfresource.TestCase{
		// No TestCase-level factories: every step declares its own
		// (plugin-testing v1.16.0 requirement).
		Steps: []tfresource.TestStep{
			{
				// Create under the OLD type, via a provider that registers
				// only the old name. This is the state a pre-rename user
				// holds: production resource, production schema, private
				// membership record included.
				ProtoV6ProviderFactories: newPreRenameProviderFactories(),
				Config: testAccProviderBlock(httpServer.URL) + `
resource "anyscale_organization_user" "test" {
  email = "lifecycle@example.com"
}
`,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("anyscale_organization_user.test", "id", email),
					tfresource.TestCheckResourceAttr("anyscale_organization_user.test", "email", email),
				),
			},
			{
				// The step-2 provider does NOT register anyscale_organization_user.
				// A no-op plan proves Core accepted the cross-type move via
				// MoveResourceState against the unregistered source type.
				ProtoV6ProviderFactories: newOnlyFactories,
				Config:                   movedConfig,
				ConfigPlanChecks: tfresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(newAddr, plancheck.ResourceActionNoop),
					},
				},
				Check: tfresource.ComposeAggregateTestCheckFunc(
					// The moved state keeps its key and identity under the
					// new type: a botched move would plan a create.
					tfresource.TestCheckResourceAttr(newAddr, "id", email),
					tfresource.TestCheckResourceAttr(newAddr, "email", email),
				),
			},
			{
				// Same config again: the move settled, nothing left to do.
				ProtoV6ProviderFactories: newOnlyFactories,
				Config:                   movedConfig,
				ConfigPlanChecks: tfresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	// Teardown destroyed under the new type. The membership-eviction invariant
	// from the lifecycle test applies unchanged: an adopted member must not
	// be deleted from the organization by destroy.
	if mockServer.isDeleted() {
		t.Fatal("the member was DELETED from the organization by teardown destroy - destroying this resource must not evict an adopted member")
	}
}
