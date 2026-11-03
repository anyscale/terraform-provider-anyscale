package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// Shared helpers for the real-API tests of resources that change
// organization-wide state: the default cloud pointer, the scheduler config, and
// organization membership. Those writes are visible to every member of the
// organization while the test runs, so they are gated separately from the
// ordinary acceptance suite and each test snapshots the value it changes and
// writes it back in t.Cleanup.

const orgMutationEnv = "ANYSCALE_TEST_ORG_MUTATION"

// skipUnlessOrgMutation skips the test unless ANYSCALE_TEST_ORG_MUTATION=1.
// Once it is set, ANYSCALE_TEST_ORG_NAME is mandatory and must match the
// organization the credentials authenticate against: an org-wide write must
// never land in whichever organization the token in scope happens to reach.
func skipUnlessOrgMutation(t *testing.T) {
	t.Helper()
	SkipIfNotAcceptanceTest(t)
	if strings.TrimSpace(os.Getenv(orgMutationEnv)) != "1" {
		t.Skipf("SKIP(org-mutation): this test changes organization-wide state that every member sees "+
			"while it runs (it snapshots the value and restores it afterwards). Set %s=1 and "+
			"ANYSCALE_TEST_ORG_NAME=<org> to run it against a test organization.", orgMutationEnv)
	}
	PreCheck(t)
	requireTestOrgName(t, "this test changes organization-wide state")
}

// requireTestOrgName fails unless ANYSCALE_TEST_ORG_NAME is set and equals the
// name of the organization the credentials authenticate against. Unlike
// requireRealInfraTestUser it fails closed when the organization cannot be
// read: a write whose target cannot be confirmed must not happen.
func requireTestOrgName(t *testing.T, why string) {
	t.Helper()
	want := strings.TrimSpace(os.Getenv("ANYSCALE_TEST_ORG_NAME"))
	org := authenticatedOrgName(t)
	if want == "" {
		t.Fatalf("ANYSCALE_TEST_ORG_NAME is not set, and %s. Name the organization rather than inheriting "+
			"it from whichever credential is in scope. These credentials authenticate against %q.", why, org)
	}
	if org == "" {
		t.Fatalf("could not read the authenticated organization from /api/v2/userinfo, so the target of "+
			"this write cannot be confirmed against ANYSCALE_TEST_ORG_NAME=%q; refusing to run", want)
	}
	if !strings.EqualFold(want, org) {
		t.Fatalf("ANYSCALE_TEST_ORG_NAME is %q but these credentials authenticate against %q. Refusing to "+
			"write to an unintended organization (ANYSCALE_CLI_TOKEN takes precedence over "+
			"~/.anyscale/credentials.json).", want, org)
	}
	t.Logf("REAL-API TARGET ORGANIZATION: %q", org)
}

// orgAPI calls the real API outside Terraform. It returns an error instead of
// failing the test so cleanups can report a failed restore with t.Errorf and
// keep going.
func orgAPI(method, path string, payload []byte) (int, []byte, error) {
	client, err := GetTestClient()
	if err != nil {
		return 0, nil, fmt.Errorf("test client: %w", err)
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	resp, err := client.DoRequest(context.Background(), method, path, body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	return resp.StatusCode, out, nil
}

// liveOrganization is the subset of userinfo's organization these tests read.
type liveOrganization struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	DefaultCloudID *string `json:"default_cloud_id"`
}

// readLiveOrganization reads the token's organization from /api/v2/userinfo.
// userinfo types organizations as a list but always returns exactly the
// token-scoped one.
func readLiveOrganization() (liveOrganization, error) {
	status, body, err := orgAPI("GET", "/api/v2/userinfo", nil)
	if err != nil {
		return liveOrganization{}, err
	}
	if status != 200 {
		return liveOrganization{}, fmt.Errorf("GET /api/v2/userinfo: HTTP %d: %s", status, truncateBody(string(body), 256))
	}
	var parsed struct {
		Result struct {
			Organizations []liveOrganization `json:"organizations"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return liveOrganization{}, fmt.Errorf("parse userinfo: %w", err)
	}
	if len(parsed.Result.Organizations) == 0 {
		return liveOrganization{}, fmt.Errorf("userinfo returned no organization")
	}
	return parsed.Result.Organizations[0], nil
}
