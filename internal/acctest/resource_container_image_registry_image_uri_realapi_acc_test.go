package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Changing image_uri against the real API: the new build lands on the same cluster
// environment, revision increments by one, the previous build is still served, and a
// refresh plans clean. Revisions are asserted relative to the one Create leaves, so the
// test does not depend on how many builds Create registers. The mock-server test proves
// what the provider sends; this proves the backend accepts it and Read round-trips the
// result.
func TestAccContainerImageRegistryResource_ImageURIUpdate_RealAPI(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	const addr = "anyscale_container_image_registry.test"
	name := UniqueName(t, "img-registry-uri-update")
	// Fake ECR-style URIs: the API accepts the format even if the image doesn't exist.
	imageV1 := "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:v1"
	imageV2 := "123456789012.dkr.ecr.us-west-2.amazonaws.com/my-ray-image:v2"

	var firstID, firstBuildID string
	var firstRevision int

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheck("anyscale_container_image_registry", "/api/v2/application_templates/%s", "result.archived_at"),
		Steps: []resource.TestStep{
			{
				Config: testAccContainerImageRegistryResourceBYODConfig(name, imageV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "image_uri", imageV1),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[addr]
						firstID = rs.Primary.ID
						firstBuildID = rs.Primary.Attributes["build_id"]
						rev, err := strconv.Atoi(rs.Primary.Attributes["revision"])
						if err != nil || rev < 1 {
							return fmt.Errorf("revision after create = %q, want a positive integer", rs.Primary.Attributes["revision"])
						}
						firstRevision = rev
						return nil
					},
				),
			},
			{
				Config: testAccContainerImageRegistryResourceBYODConfig(name, imageV2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "image_uri", imageV2),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[addr]
						want := firstRevision + 1
						if got := rs.Primary.Attributes["revision"]; got != strconv.Itoa(want) {
							return fmt.Errorf("revision = %s, want %d (one more than after create)", got, want)
						}
						if got, wantNV := rs.Primary.Attributes["name_version"], name+":"+strconv.Itoa(want); got != wantNV {
							return fmt.Errorf("name_version = %q, want %q", got, wantNV)
						}
						if rs.Primary.ID != firstID {
							return fmt.Errorf("id changed from %q to %q: the environment was replaced", firstID, rs.Primary.ID)
						}
						if rs.Primary.Attributes["build_id"] == firstBuildID {
							return fmt.Errorf("build_id is still %q: no new build was registered", firstBuildID)
						}
						return testAccCheckRegistryBuildsInAPI(firstID, firstBuildID, imageV1, rs.Primary.Attributes["build_id"], imageV2, want)
					},
				),
			},
		},
	})
}

// testAccCheckRegistryBuildsInAPI checks the backend's view after an image_uri change:
// the environment is live with the new build as latest, and the first build is still
// served with its original image.
func testAccCheckRegistryBuildsInAPI(envID, oldBuildID, oldImage, newBuildID, newImage string, newRevision int) error {
	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("failed to get test client: %w", err)
	}
	get := func(path string, out any) error {
		resp, err := client.DoRequest(context.Background(), http.MethodGet, path, nil)
		if err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, body)
		}
		return json.Unmarshal(body, out)
	}

	var env struct {
		Result struct {
			ArchivedAt  *string `json:"archived_at"`
			LatestBuild *struct {
				ID       string `json:"id"`
				Revision int    `json:"revision"`
			} `json:"latest_build"`
		} `json:"result"`
	}
	if err := get("/api/v2/application_templates/"+envID, &env); err != nil {
		return err
	}
	if env.Result.ArchivedAt != nil {
		return fmt.Errorf("environment %s is archived after an in-place update", envID)
	}
	if env.Result.LatestBuild == nil || env.Result.LatestBuild.ID != newBuildID || env.Result.LatestBuild.Revision != newRevision {
		return fmt.Errorf("environment %s latest_build = %+v, want id %s revision %d", envID, env.Result.LatestBuild, newBuildID, newRevision)
	}

	for _, b := range []struct{ id, image string }{{oldBuildID, oldImage}, {newBuildID, newImage}} {
		var build struct {
			Result struct {
				ApplicationTemplateID string  `json:"application_template_id"`
				DockerImageName       *string `json:"docker_image_name"`
			} `json:"result"`
		}
		if err := get("/api/v2/builds/"+b.id, &build); err != nil {
			return err
		}
		if build.Result.ApplicationTemplateID != envID {
			return fmt.Errorf("build %s belongs to %q, want %q", b.id, build.Result.ApplicationTemplateID, envID)
		}
		if build.Result.DockerImageName == nil || *build.Result.DockerImageName != b.image {
			return fmt.Errorf("build %s docker_image_name = %v, want %q", b.id, build.Result.DockerImageName, b.image)
		}
	}
	return nil
}
