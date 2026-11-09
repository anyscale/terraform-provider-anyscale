package acctest

// TestAccIAMOrganizationInvitationResource_MovedFromOldType_MockServer
// proves a moved{} block upgrades state written under the pre-rename type
// name anyscale_organization_invitation to
// anyscale_iam_organization_invitation: the moved state plans no-op against
// the new provider (which does not register the old name) and settles to an
// empty plan.
//
// Step 1 creates under the OLD type name through a wrapper that registers
// ONLY the old name - the position an existing user is in after upgrading
// the provider; everything but Metadata is the production resource. Step 2
// runs the real provider with a moved{} block. The invitation id minted in
// step 1 must survive the move byte-identical: a re-created invitation would
// carry a different id, so comparing the two ids proves the state MOVED
// rather than being destroyed and re-created (which would silently orphan
// the pending invitation).
//
// The address Core reports inside resource.Test is
// registry.terraform.io/hashicorp/anyscale (harness keying), so the mover's
// gate compares only the provider TYPE segment, which this test exercises.

import (
	"context"
	"fmt"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// preRenameOrganizationInvitationResource re-registers the real invitation
// resource under its pre-rename type name (Metadata override only).
type preRenameOrganizationInvitationResource struct {
	*provider.OrganizationInvitationResource
}

func (r *preRenameOrganizationInvitationResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_invitation"
}

// preRenameInvitationProvider registers only the old invitation resource.
type preRenameInvitationProvider struct {
	fwprovider.Provider
}

func (p *preRenameInvitationProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource {
			r := provider.NewOrganizationInvitationResource()
			inner, ok := r.(*provider.OrganizationInvitationResource)
			if !ok {
				panic("provider.NewOrganizationInvitationResource no longer returns *provider.OrganizationInvitationResource; update the moved-state test wrapper")
			}
			return &preRenameOrganizationInvitationResource{OrganizationInvitationResource: inner}
		},
	}
}

func newPreRenameInvitationProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	preRename := &preRenameInvitationProvider{Provider: provider.NewFramework("test")()}
	return map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": providerserver.NewProtocol6WithError(preRename),
	}
}

func TestAccIAMOrganizationInvitationResource_MovedFromOldType_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, _ := newMockInvitationServer(t, false)
	const newAddr = "anyscale_iam_organization_invitation.test"
	const email = "lifecycle-test-1@example.com"

	providerBlock := testAccProviderBlock(httpServer.URL)

	movedConfig := providerBlock + `
resource "anyscale_iam_organization_invitation" "test" {
  email = "lifecycle-test-1@example.com"
}

moved {
  from = anyscale_organization_invitation.test
  to   = anyscale_iam_organization_invitation.test
}
`

	newOnlyFactories := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": providerserver.NewProtocol6WithError(provider.NewFramework("test")()),
	}

	var invitationID string

	tfresource.Test(t, tfresource.TestCase{
		// No TestCase-level factories: every step declares its own.
		Steps: []tfresource.TestStep{
			{
				// Create under the OLD type: production resource, production
				// schema, real invitation id.
				ProtoV6ProviderFactories: newPreRenameInvitationProviderFactories(),
				Config: providerBlock + `
resource "anyscale_organization_invitation" "test" {
  email = "lifecycle-test-1@example.com"
}
`,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttrSet("anyscale_organization_invitation.test", "id"),
					tfresource.TestCheckResourceAttr("anyscale_organization_invitation.test", "email", email),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["anyscale_organization_invitation.test"]
						if !ok {
							return fmt.Errorf("resource not found in state")
						}
						invitationID = rs.Primary.ID
						if invitationID == "" {
							return fmt.Errorf("invitation has an empty id")
						}
						return nil
					},
				),
			},
			{
				// The step-2 provider does NOT register the old invitation
				// type, so the no-op plan proves Core accepted the cross-type
				// move via MoveResourceState.
				ProtoV6ProviderFactories: newOnlyFactories,
				Config:                   movedConfig,
				ConfigPlanChecks: tfresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(newAddr, plancheck.ResourceActionNoop),
					},
				},
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr(newAddr, "email", email),
					// The moved state is the SAME invitation: id identical to
					// the one created under the old type, proving move-not-
					// recreate.
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources[newAddr]
						if !ok {
							return fmt.Errorf("resource %s not found in state", newAddr)
						}
						if rs.Primary.ID != invitationID {
							return fmt.Errorf("moved invitation id = %q, want the pre-move id %q - the state was re-created, not moved", rs.Primary.ID, invitationID)
						}
						return nil
					},
				),
			},
			{
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
}
