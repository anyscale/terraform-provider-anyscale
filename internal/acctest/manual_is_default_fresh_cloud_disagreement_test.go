package acctest

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestManualIsDefaultFreshCloudDisagreement is a manual, real-backend
// diagnostic (deliberately not TestAcc-prefixed - see
// TestManualIsDefaultSingularEndpointStability's doc comment for why) probing
// architect's "prime suspect": the singular-vs-plural is_default
// disagreement (commit 082e29f, backlog #94), but on a FRESHLY CREATED
// cloud rather than the long-standing static fixture. That distinction
// matters - TestManualIsDefaultSingularEndpointStability already showed the
// singular endpoint is stable on an old, settled cloud; commit 082e29f's
// disagreement was specifically observed on a cloud seconds old. If the
// backend's is_default computation is only unstable in a short window right
// after creation (an index/cache not yet caught up), a settled fixture would
// never show it, which would explain why re-testing against the static
// fixture wasn't sufficient here.
//
// Creates one real, minimal empty AWS cloud (no embedded resources, no real
// AWS infra needed - same shape as TestAccCloudResource_AWS_EmptyCloud),
// then hammers both GET /clouds/{id} and GET /clouds every ~1.5s for about
// 20s immediately after creation, logging every reading. Cleans up via the
// resource.Test lifecycle's own destroy step - no manual sweeper needed.
// Run manually: TF_ACC=1 go test ./internal/acctest/... -run TestManualIsDefaultFreshCloudDisagreement -v
func TestManualIsDefaultFreshCloudDisagreement(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	PreCheck(t)

	cloudName := UniqueName(t, "cloud-freshdefault")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudResourceAWSEmptyConfig(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_cloud.test", "id"),
					checkFreshCloudIsDefaultAgreement(t, "anyscale_cloud.test"),
				),
			},
		},
	})
}

// checkFreshCloudIsDefaultAgreement polls both the singular and plural cloud
// endpoints repeatedly right after creation, logging every reading via
// t.Logf so the full sequence is visible in -v output regardless of whether
// the test ultimately passes or fails.
func checkFreshCloudIsDefaultAgreement(t *testing.T, resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}
		cloudID := rs.Primary.ID

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}
		ctx := context.Background()

		var disagreements int
		for i := 0; i < 12; i++ {
			singular, err := provider.DoRequestAndParse[provider.CloudResponse](
				ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK,
			)
			if err != nil {
				return fmt.Errorf("call %d: singular GET failed: %w", i, err)
			}

			plural, err := provider.DoRequestAndParse[provider.CloudsListResponse](
				ctx, client, "GET", "/api/v2/clouds", nil, http.StatusOK,
			)
			if err != nil {
				return fmt.Errorf("call %d: plural GET failed: %w", i, err)
			}

			pluralIsDefault, foundInPlural := false, false
			for _, c := range plural.Results {
				if c.ID == cloudID {
					pluralIsDefault = c.IsDefault
					foundInPlural = true
					break
				}
			}

			if !foundInPlural {
				t.Logf("call %d: singular is_default=%v, cloud NOT found in first page of plural results (total=%d)", i, singular.Result.IsDefault, plural.Metadata.Total)
			} else {
				t.Logf("call %d: singular is_default=%v, plural is_default=%v, agree=%v", i, singular.Result.IsDefault, pluralIsDefault, singular.Result.IsDefault == pluralIsDefault)
				if singular.Result.IsDefault != pluralIsDefault {
					disagreements++
				}
			}

			time.Sleep(1500 * time.Millisecond)
		}

		if disagreements > 0 {
			return fmt.Errorf("singular/plural is_default disagreed on %d of 12 calls for a freshly created cloud", disagreements)
		}
		return nil
	}
}
