package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// apiExistsCheck describes how one resource type is looked up in the Anyscale API.
type apiExistsCheck struct {
	// noun names the resource in errors, e.g. "project".
	noun string
	// id returns the ID to look up from the resource's state; "" fails the check.
	id func(*terraform.InstanceState) string
	// getPathFmt is the GET path with one %s for the ID, e.g. "/api/v2/projects/%s".
	getPathFmt string
	// accepted are the statuses that mean the resource exists.
	accepted []int
	// checkBody, when set, runs on the body of an accepted response.
	checkBody func(id string, body []byte) error
}

// primaryID returns the resource's primary ID.
func primaryID(rs *terraform.InstanceState) string { return rs.ID }

// attrID returns an ID reader for the named attribute.
func attrID(name string) func(*terraform.InstanceState) string {
	return func(rs *terraform.InstanceState) string { return rs.Attributes[name] }
}

// computeConfigLookupID is the version-specific config_id, falling back to the
// primary ID for state that predates config_id.
func computeConfigLookupID(rs *terraform.InstanceState) string {
	if id := rs.Attributes["config_id"]; id != "" {
		return id
	}
	return rs.ID
}

// checkCloudResponseID fails unless a GET /clouds/{id} body is the cloud asked for.
func checkCloudResponseID(id string, body []byte) error {
	var cloudResp provider.CloudResponse
	if err := json.Unmarshal(body, &cloudResp); err != nil {
		return fmt.Errorf("failed to parse API response: %w", err)
	}
	if cloudResp.Result.ID != id {
		return fmt.Errorf("cloud ID mismatch: expected %s, got %s", id, cloudResp.Result.ID)
	}
	return nil
}

// NewAPIExistsCheck returns a TestCheckFunc that fails unless resourceName's
// resource is found in the Anyscale API as c describes, using the test client.
func NewAPIExistsCheck(resourceName string, c apiExistsCheck) resource.TestCheckFunc {
	return newAPIExistsCheckWithClient(GetTestClient, resourceName, c)
}

// newAPIExistsCheckWithClient is NewAPIExistsCheck with the client supplied,
// so the check itself can be tested against a mock server.
func newAPIExistsCheckWithClient(getClient func() (*provider.Client, error), resourceName string, c apiExistsCheck) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found in state: %s", resourceName)
		}
		id := c.id(rs.Primary)
		if id == "" {
			return fmt.Errorf("no %s ID is set on %s", c.noun, resourceName)
		}
		client, err := getClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf(c.getPathFmt, id), nil)
		if err != nil {
			return fmt.Errorf("failed to get %s %s: %w", c.noun, id, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read %s %s response: %w", c.noun, id, err)
		}
		if !slices.Contains(c.accepted, resp.StatusCode) {
			return fmt.Errorf("%s %s not found in API: status %d: %s", c.noun, id, resp.StatusCode, body)
		}
		if c.checkBody != nil {
			return c.checkBody(id, body)
		}
		return nil
	}
}
