package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// NewAPIDestroyCheck returns a CheckDestroy function that verifies every
// resource of resourceType in the Terraform state is gone from the Anyscale
// API. getPathFmt is a printf format string with one %s for the resource ID
// (e.g. "/api/v2/projects/%s"). 404 = success. 200 = leak (returns error).
// Transient errors (network, 5xx) log a warning and continue so a flaky API
// doesn't mask a real leak from the rest of the state.
func NewAPIDestroyCheck(resourceType, getPathFmt string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, "", getPathFmt, "")
}

// NewAPIArchivedDestroyCheck is the variant for resources that the API cannot
// permanently delete — container images, cluster environments. It verifies
// that the resource still exists but its archived field is truthy.
// archivedJSONPath is the dotted JSON path to read from the GET response
// (e.g. "result.deleted_at" or "is_archived"). A value of true (bool) or a
// non-empty/non-null string at that path counts as archived.
func NewAPIArchivedDestroyCheck(resourceType, getPathFmt, archivedJSONPath string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, "", getPathFmt, archivedJSONPath)
}

// NewAPIArchivedDestroyCheckByAttr is the attribute-keyed variant of
// NewAPIArchivedDestroyCheck.
func NewAPIArchivedDestroyCheckByAttr(resourceType, attrName, getPathFmt, archivedJSONPath string) resource.TestCheckFunc {
	return newAPIDestroyCheckImpl(resourceType, attrName, getPathFmt, archivedJSONPath)
}

// NewAPIArchivedDestroyCheckForID is the ID-pinned variant of
// NewAPIArchivedDestroyCheck: rather than discovering which resources to
// check by scanning Terraform state for resourceType, it polls the single id
// the caller supplies via a *string. Use this when the fact under test
// concerns an id a prior step has already dropped from state — e.g. a
// RequiresReplace swapped in a new id, and the point of the check is that the
// OLD id was actually archived server-side, not merely that the new resource
// looks right. Populate id with a TestCheckFunc earlier in the same Check
// chain, since its value isn't known until that step runs.
//
// Unlike the CheckDestroy-oriented family above, an unexpected status here is
// a hard error rather than a logged warning: this runs inside a Check as a
// positive assertion about one known id, not a best-effort leak scan across
// however many resources remain in state.
//
// failureHint is an optional trailing string appended to the timeout error,
// for a caller that wants the failure message to name the specific
// regression it proves rather than just the generic timeout.
func NewAPIArchivedDestroyCheckForID(resourceType string, id *string, getPathFmt, archivedJSONPath, failureHint string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if id == nil || *id == "" {
			return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): no id captured to check", resourceType)
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to get test client: %w", resourceType, err)
		}

		path := fmt.Sprintf(getPathFmt, *id)
		deadline := time.Now().Add(destroyCheckPollTimeout)
		for {
			resp, err := client.DoRequest(context.Background(), "GET", path, nil)
			if err != nil {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to check archived status of %s: %w", resourceType, *id, err)
			}

			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to read response for %s: %w", resourceType, *id, readErr)
			}
			if resp.StatusCode != 200 {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): unexpected status %d checking %s: %s",
					resourceType, resp.StatusCode, *id, truncateBody(string(body), 256))
			}

			archived, perr := extractArchivedValue(body, archivedJSONPath)
			if perr != nil {
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): failed to parse %s for %s: %w", resourceType, archivedJSONPath, *id, perr)
			}
			if archived {
				return nil
			}
			if time.Now().After(deadline) {
				hint := ""
				if failureHint != "" {
					hint = " - " + failureHint
				}
				return fmt.Errorf("NewAPIArchivedDestroyCheckForID(%s): %s was never archived (checked %s) within the poll window%s", resourceType, *id, archivedJSONPath, hint)
			}
			time.Sleep(destroyCheckPollInterval)
		}
	}
}

// Anyscale archives/deletes are asynchronous: the API accepts the request but
// the archive marker (e.g. result.deleted_at) can take a few seconds to
// persist. CheckDestroy polls for the archived variant up to this bound so it
// does not race the backend and report a false leak.
const (
	destroyCheckPollTimeout  = 30 * time.Second
	destroyCheckPollInterval = 2 * time.Second
)

// newAPIDestroyCheckImpl is the shared implementation behind the public
// CheckDestroy helpers. When archivedJSONPath is empty, a 200 response is
// treated as a leak. Otherwise, the value at archivedJSONPath must be truthy
// (bool true or non-empty/non-null string) or the resource is a leak. For the
// archived variant the check polls up to destroyCheckPollTimeout because the
// backend sets the archive marker asynchronously.
func newAPIDestroyCheckImpl(resourceType, attrName, getPathFmt, archivedJSONPath string) resource.TestCheckFunc {
	return newAPIDestroyCheckImplWithTimeout(resourceType, attrName, getPathFmt, archivedJSONPath, destroyCheckPollTimeout)
}

// newAPIDestroyCheckImplWithTimeout is newAPIDestroyCheckImpl with the poll
// bound injectable, so unit tests of the fail-closed paths need not wait out
// the real timeout.
func newAPIDestroyCheckImplWithTimeout(resourceType, attrName, getPathFmt, archivedJSONPath string, pollTimeout time.Duration) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if len(s.RootModule().Resources) == 0 {
			return nil
		}

		client, err := GetTestClient()
		if err != nil {
			// No client = can't verify; surface clearly rather than silently passing.
			return fmt.Errorf("CheckDestroy(%s): failed to get test client: %w", resourceType, err)
		}

		var leaks []string

		for name, rs := range s.RootModule().Resources {
			if rs.Type != resourceType {
				continue
			}

			id := rs.Primary.ID
			if attrName != "" {
				id = rs.Primary.Attributes[attrName]
			}
			if id == "" {
				// An empty id cannot be checked. Skipping it let a destroy
				// check pass while verifying nothing.
				field := attrName
				if field == "" {
					field = "id"
				}
				leaks = append(leaks, fmt.Sprintf("%s has an empty %s in state; cannot verify it was destroyed", name, field))
				continue
			}

			path := fmt.Sprintf(getPathFmt, id)

			// Poll so we don't race the backend's asynchronous archive/delete.
			// Only a 404, a truthy archive marker, or a 403 for an ID the
			// resource type's own list no longer contains counts as destroyed.
			// A transport error or 5xx is retried until destroyCheckPollTimeout
			// and then fails; any other status fails at once.
			deadline := time.Now().Add(pollTimeout)
			for {
				resp, err := client.DoRequest(context.Background(), "GET", path, nil)
				if err != nil {
					if time.Now().After(deadline) {
						leaks = append(leaks, fmt.Sprintf("%s (id=%s): could not verify destroy, request error until the poll deadline: %v", name, id, err))
						break
					}
					time.Sleep(destroyCheckPollInterval)
					continue
				}

				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()

				if resp.StatusCode == 404 {
					break // gone — success
				}
				if resp.StatusCode == 403 {
					// With SpiceDB authorization the backend answers 403, not 404,
					// for an ID it has no record of, so 403 counts as destroyed.
					// Where the type has a list endpoint the ID must also be absent
					// from it, so a 403 for an object that still exists fails.
					absent, determined, lerr := absentFromList(client, resourceType, id)
					switch {
					case lerr != nil:
						leaks = append(leaks, fmt.Sprintf("%s (id=%s): %s returned 403 and the list check failed: %v", name, id, path, lerr))
					case !determined:
						log.Printf("[INFO] CheckDestroy(%s): %s returned 403 (no list to cross-check); destroyed", resourceType, path)
					case !absent:
						leaks = append(leaks, fmt.Sprintf("%s (id=%s): %s returned 403 but the ID is still listed", name, id, path))
					default:
						log.Printf("[INFO] CheckDestroy(%s): %s returned 403 and %s is absent from the full list; destroyed", resourceType, path, id)
					}
					break
				}
				if resp.StatusCode >= 500 {
					if time.Now().After(deadline) {
						leaks = append(leaks, fmt.Sprintf("%s (id=%s): could not verify destroy, %s returned %d until the poll deadline", name, id, path, resp.StatusCode))
						break
					}
					time.Sleep(destroyCheckPollInterval)
					continue
				}
				if resp.StatusCode != 200 && resp.StatusCode != 201 {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s): could not verify destroy, %s returned unexpected status %d: %s", name, id, path, resp.StatusCode, truncateBody(string(body), 256)))
					break
				}

				// 200/201: the resource still exists.
				if archivedJSONPath == "" {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s) still returns 200 from %s", name, id, path))
					break
				}
				if readErr != nil {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s): could not verify destroy, failed to read body: %v", name, id, readErr))
					break
				}
				archived, perr := extractArchivedValue(body, archivedJSONPath)
				if perr != nil {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s): could not verify destroy, failed to parse %s: %v", name, id, archivedJSONPath, perr))
					break
				}
				if archived {
					break // archived — success
				}

				// Still present and not yet archived: the async delete may still
				// be in flight. Retry until the deadline, then record a leak.
				if time.Now().After(deadline) {
					leaks = append(leaks, fmt.Sprintf("%s (id=%s) exists at %s and %s is not truthy", name, id, path, archivedJSONPath))
					break
				}
				time.Sleep(destroyCheckPollInterval)
			}
		}

		if len(leaks) > 0 {
			return fmt.Errorf("CheckDestroy(%s) found leaked or unverifiable resources:\n  %s", resourceType, strings.Join(leaks, "\n  "))
		}
		return nil
	}
}

// extractArchivedValue walks a dotted JSON path and returns whether the value
// at the leaf is truthy. true (bool) or a non-empty/non-null string are
// truthy; all other values (false, null, missing key, numbers, arrays) are
// not. Designed for tolerant detection of "this resource has been archived
// rather than deleted" markers (is_archived bool, deleted_at string, etc.).
func extractArchivedValue(body []byte, jsonPath string) (bool, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return false, err
	}

	cur := root
	for _, segment := range strings.Split(jsonPath, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false, nil
		}
		cur, ok = m[segment]
		if !ok {
			return false, nil
		}
	}

	switch v := cur.(type) {
	case bool:
		return v, nil
	case string:
		return v != "", nil
	default:
		return false, nil
	}
}

// destroyListPaths are the list endpoints absentFromList can page through, by resource type.
var destroyListPaths = map[string]string{
	"anyscale_cloud":   "/api/v2/clouds",
	"anyscale_project": "/api/v2/projects",
	"anyscale_service": "/api/v2/services-v2",
}

// absentFromList reports whether id is missing from every page of resourceType's list.
// determined is false when the type has no known list endpoint.
func absentFromList(client *provider.Client, resourceType, id string) (absent, determined bool, err error) {
	listPath, ok := destroyListPaths[resourceType]
	if !ok {
		return false, false, nil
	}
	ids, err := provider.PaginatedRequest(context.Background(), client, listPath, nil,
		func(body []byte) ([]string, *string, error) {
			var page struct {
				Results []struct {
					ID string `json:"id"`
				} `json:"results"`
				Metadata struct {
					NextPagingToken *string `json:"next_paging_token"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return nil, nil, err
			}
			out := make([]string, 0, len(page.Results))
			for _, r := range page.Results {
				out = append(out, r.ID)
			}
			return out, page.Metadata.NextPagingToken, nil
		})
	if err != nil {
		return false, true, err
	}
	for _, listed := range ids {
		if listed == id {
			return false, true, nil
		}
	}
	return true, true, nil
}
