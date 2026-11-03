package acctest

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccOrganizationDefaultCloudResource_RealAPI moves the organization's
// default cloud pointer against the real API: create on the pinned fixture,
// update in place to a second cloud, import, then destroy.
//
// The pointer is org-wide, so the test records the current default first and
// writes it back in t.Cleanup. The API has no way to clear the pointer, so an
// organization with NO explicit default cannot be restored once this test sets
// one: the test skips in that case rather than leave every member with an
// explicit default they did not have before.
//
// The second cloud is the organization's previous default when that differs
// from the pinned fixture, otherwise another configured cloud. Destroy is
// state-only for this resource, so CheckDestroy asserts the pointer is still
// on the second cloud.
//
// Needs an organization-owner token.
//
// Run: TF_ACC=1 ANYSCALE_TEST_ORG_MUTATION=1 ANYSCALE_TEST_ORG_NAME=<org> go test ./internal/acctest -run '^TestAccOrganizationDefaultCloudResource_RealAPI$' -v -count=1
func TestAccOrganizationDefaultCloudResource_RealAPI(t *testing.T) {
	skipUnlessOrgMutation(t)

	before, err := readLiveOrganization()
	if err != nil {
		t.Fatalf("snapshot the organization default cloud: %s", err)
	}
	if before.DefaultCloudID == nil || *before.DefaultCloudID == "" {
		t.Skipf("SKIP(org-mutation): organization %q has no explicit default cloud. The API cannot clear "+
			"the pointer, so setting one here could not be undone. Set a default for this organization "+
			"first (console or `anyscale cloud set-default`) if you want this test to run.", before.Name)
	}
	prior := *before.DefaultCloudID
	t.Logf("SNAPSHOT: organization default cloud is %s", prior)

	t.Cleanup(func() {
		if err := setLiveDefaultCloud(prior); err != nil {
			t.Errorf("RESTORE FAILED: could not set the organization default cloud back to %s: %s. "+
				"Set it by hand: anyscale cloud set-default --cloud-id %s", prior, err, prior)
			return
		}
		after, err := readLiveOrganization()
		if err != nil {
			t.Errorf("RESTORE UNVERIFIED: wrote default cloud %s but could not read it back: %s", prior, err)
			return
		}
		if after.DefaultCloudID == nil || *after.DefaultCloudID != prior {
			t.Errorf("RESTORE FAILED: organization default cloud is %v after restore, want %s",
				derefOr(after.DefaultCloudID, "<null>"), prior)
			return
		}
		t.Logf("RESTORED: organization default cloud is %s again", prior)
	})

	first := GetTestCloudID(t)
	second := prior
	if second == first {
		second = ""
		for _, c := range GetAllConfiguredClouds(t) {
			if c.ID != first {
				second = c.ID
				break
			}
		}
		if second == "" {
			t.Skipf("SKIP(org-mutation): the in-place update needs a second cloud, and the only configured "+
				"cloud is the pinned fixture %s, which is already the organization default", first)
		}
	}
	t.Logf("first cloud %s, second cloud %s", first, second)

	const addr = "anyscale_organization_default_cloud.test"
	config := func(cloudID string) string {
		return realInfraProviderBlock() + fmt.Sprintf(`
resource "anyscale_organization_default_cloud" "test" {
  cloud_id = %q
}
`, cloudID)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// Destroy only drops the resource from state; the pointer must not move.
		CheckDestroy: func(*terraform.State) error {
			return checkLiveDefaultCloud(second)
		},
		Steps: []resource.TestStep{
			{
				Config: config(first),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cloud_id", first),
					resource.TestCheckResourceAttr(addr, "id", before.ID),
					func(*terraform.State) error { return checkLiveDefaultCloud(first) },
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: config(second),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cloud_id", second),
					func(*terraform.State) error { return checkLiveDefaultCloud(second) },
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     second,
				ImportStateVerify: true,
			},
		},
	})
}

// setLiveDefaultCloud points the organization default at cloudID outside
// Terraform, through the same endpoint the resource uses.
func setLiveDefaultCloud(cloudID string) error {
	path := "/api/v2/organizations/update_default_cloud?cloud_id=" + url.QueryEscape(cloudID)
	status, body, err := orgAPI("POST", path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return fmt.Errorf("POST update_default_cloud: HTTP %d: %s", status, truncateBody(string(body), 256))
	}
	return nil
}

// checkLiveDefaultCloud asserts the organization's explicit default_cloud_id,
// read from userinfo rather than state, is want.
func checkLiveDefaultCloud(want string) error {
	org, err := readLiveOrganization()
	if err != nil {
		return err
	}
	if org.DefaultCloudID == nil || *org.DefaultCloudID != want {
		return fmt.Errorf("API reports organization default cloud %s, want %s",
			derefOr(org.DefaultCloudID, "<null>"), want)
	}
	return nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
