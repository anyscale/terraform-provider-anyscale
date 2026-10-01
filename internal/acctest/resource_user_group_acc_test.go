package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real-API coverage for anyscale_user_group. These tests create and delete
// only groups they name with UniqueName; no group carries a role and no user
// is added, so they are safe in the shared test org and run in the ordinary
// acctest-resource shard. Membership tests that touch a real person live in
// resource_user_group_members_acc_test.go behind ANYSCALE_TEST_USER_EMAIL.

func testAccUserGroupConfig(name string) string {
	return fmt.Sprintf(`
resource "anyscale_user_group" "test" {
  name = %q
}
`, name)
}

// userGroupAPI calls the user_groups API directly, outside Terraform, and
// returns the status and body.
func userGroupAPI(t *testing.T, method, path string, payload any) (int, []byte) {
	t.Helper()
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body = bytes.NewReader(b)
	}
	resp, err := client.DoRequest(context.Background(), method, path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// createUserGroupOutOfBand creates a group directly through the API and
// registers a cleanup that deletes it. It returns the group ID.
func createUserGroupOutOfBand(t *testing.T, name string) string {
	t.Helper()
	status, body := userGroupAPI(t, "POST", "/api/v2/user_groups/", map[string]string{"name": name})
	if status != http.StatusCreated {
		t.Fatalf("create group %q out of band: status %d: %s", name, status, truncateBody(string(body), 256))
	}
	var resp struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Result.ID == "" {
		t.Fatalf("parse created group: %v: %s", err, truncateBody(string(body), 256))
	}
	id := resp.Result.ID
	t.Cleanup(func() {
		if status, body := userGroupAPI(t, "DELETE", "/api/v2/user_groups/"+id, nil); status != http.StatusNoContent && status != http.StatusNotFound {
			t.Errorf("cleanup: delete group %s: status %d: %s", id, status, truncateBody(string(body), 256))
		}
	})
	return id
}

// testAccCheckUserGroupInAPI asserts the group in state exists in the API
// with the expected name and is a Terraform-managed (source "user") group.
func testAccCheckUserGroupInAPI(addr, wantName string, captured *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		id := rs.Primary.ID
		if !strings.HasPrefix(id, "ug_") {
			return fmt.Errorf("id %q does not have the ug_ prefix the API issues", id)
		}
		client, err := GetTestClient()
		if err != nil {
			return err
		}
		resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/user_groups/"+id, nil)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET group %s: status %d: %s", id, resp.StatusCode, truncateBody(string(body), 256))
		}
		var got struct {
			Result struct {
				Name   string  `json:"name"`
				Source *string `json:"source"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if got.Result.Name != wantName {
			return fmt.Errorf("API name = %q, want %q", got.Result.Name, wantName)
		}
		if got.Result.Source == nil || *got.Result.Source != "user" {
			return fmt.Errorf("API source = %v, want \"user\"", got.Result.Source)
		}
		if captured != nil {
			*captured = id
		}
		return nil
	}
}

// TestAccUserGroupResource_basic covers create, an in-place rename (PATCH,
// never a replacement: the ID must survive), a clean refresh, and import.
func TestAccUserGroupResource_basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	name := UniqueName(t, "ug-basic")
	renamed := name + "-renamed"
	var firstID, secondID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_user_group", "/api/v2/user_groups/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_user_group.test", "name", name),
					testAccCheckUserGroupInAPI("anyscale_user_group.test", name, &firstID),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: testAccUserGroupConfig(renamed),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_user_group.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_user_group.test", "name", renamed),
					testAccCheckUserGroupInAPI("anyscale_user_group.test", renamed, &secondID),
					func(*terraform.State) error {
						if firstID != secondID {
							return fmt.Errorf("rename changed the group ID from %s to %s", firstID, secondID)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "anyscale_user_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccUserGroupResource_disappears: a group deleted outside Terraform is
// removed from state on refresh and planned for re-creation, not reported as
// an error.
func TestAccUserGroupResource_disappears(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	name := UniqueName(t, "ug-gone")
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_user_group", "/api/v2/user_groups/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupConfig(name),
				Check:  testAccCheckUserGroupInAPI("anyscale_user_group.test", name, &id),
			},
			{
				PreConfig: func() {
					if status, body := userGroupAPI(t, "DELETE", "/api/v2/user_groups/"+id, nil); status != http.StatusNoContent {
						t.Fatalf("out-of-band delete of %s: status %d: %s", id, status, truncateBody(string(body), 256))
					}
				},
				Config: testAccUserGroupConfig(name),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_user_group.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckUserGroupInAPI("anyscale_user_group.test", name, nil),
					func(s *terraform.State) error {
						if got := s.RootModule().Resources["anyscale_user_group.test"].Primary.ID; got == id {
							return fmt.Errorf("re-created group reused the deleted ID %s", id)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccUserGroupResource_DuplicateNameError: creating a group whose name is
// already taken fails with an actionable message naming the import command,
// and leaves nothing behind in state.
func TestAccUserGroupResource_DuplicateNameError(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	name := UniqueName(t, "ug-dup")
	existingID := createUserGroupOutOfBand(t, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccUserGroupConfig(name),
				ExpectError: regexp.MustCompile(`(?s)already exists.*terraform import.*` + regexp.QuoteMeta(existingID)),
			},
		},
	})
}
