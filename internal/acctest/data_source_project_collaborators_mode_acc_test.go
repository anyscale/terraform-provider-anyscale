package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// The legacy collaborator read is refused by the API itself in a converted
// organization, where collaborators must then be null - unavailable, never an
// empty list. In a legacy organization it must still read back non-null,
// pinning that the null is the converted-org refusal and not a lazy skip.
func TestAccProjectDataSource_CollaboratorsMatchOrgMode(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	projectName := UniqueName(t, "ds-project-colab")

	var collaborationsCheck statecheck.StateCheck
	if testOrgMode(t) == testOrgModeConverted {
		collaborationsCheck = statecheck.ExpectKnownValue(
			"data.anyscale_project.test", tfjsonpath.New("collaborators"), knownvalue.Null())
	} else {
		collaborationsCheck = statecheck.ExpectKnownValue(
			"data.anyscale_project.test", tfjsonpath.New("collaborators"), knownvalue.NotNull())
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               orgModeErrorCheck(t),
		Steps: []resource.TestStep{
			{
				Config:            testAccProjectDataSourceByIDConfig(cloudID, projectName),
				ConfigStateChecks: []statecheck.StateCheck{collaborationsCheck},
			},
		},
	})
}
