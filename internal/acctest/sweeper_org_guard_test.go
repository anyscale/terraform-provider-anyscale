package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The sweep-target org guard.
//
// `make sweep` deletes real infrastructure across SIX endpoint families and
// matches three name prefixes (see sweepableResourcePrefixes). A sweep aimed
// at the wrong organization is therefore one of the most destructive things
// this repo can do with valid credentials - and nothing stopped it before
// this guard.
//
// (A seventh sweeper, anyscale_global_resource_scheduler, is gated behind the
// grs_enabled build tag - see sweeper_scheduler_test.go - so its real
// POST /api/v2/machine_pools/delete does not compile into the default build
// this guard protects; it re-joins the count if that tag is ever dropped.)
//
// It happened. On 2026-08-03 the credentials in ~/.anyscale/credentials.json
// were scoped to a user's own staging org for an entire session; acceptance
// tests ran there and leaked four clouds into it. A sweep would have been aimed
// at the same org. The only thing that would have saved the real resources in
// it was that none of them happened to carry a test-looking name prefix - luck
// about that org's naming conventions, not a property of any guard. The sweep
// was prevented that day by a human telling everyone not to run it, which
// protects nothing tomorrow.
//
// WHY THE PINNED FIXTURE CLOUD IS THE ORG SIGNAL, and why no org name is
// committed here: defaultKnownGoodCloudName ("tfp-test-aws-useast1-STATIC")
// exists only in the acctest org. It was confirmed on 2026-08-03 to resolve in
// CI (helpers.go:138 success line in run 30830762541, both acctest shards) and
// to be absent from the wrong org - so its resolution genuinely carries
// org identity, and a separate expected-org-name constant would be a second
// source of truth for a question this already answers.
//
// FAIL CLOSED, DELIBERATELY, and note this is the OPPOSITE of the right
// direction for the test suite itself: a test run that cannot determine its org
// should warn and proceed (the cost is junk resources), whereas a SWEEP that
// cannot determine its org must refuse (the cost is somebody's real
// infrastructure). Same question, opposite defaults, chosen by blast radius
// rather than by consistency. Any error here - network failure, bad token,
// ambiguous response - is a refusal, not a warning.

// sweepRequested reports whether this process was invoked to run sweepers.
//
// The -sweep flag belongs to terraform-plugin-testing (its flagSweep is
// package-private), so it cannot be read directly and is detected from os.Args
// instead. All three spellings the Go flag package accepts are handled:
// -sweep=x, --sweep=x, and -sweep x. A bare -sweep with an empty value does not
// run sweepers, so it is deliberately not treated as a sweep request.
func sweepRequested() bool {
	args := os.Args[1:]
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		flagPart := strings.TrimLeft(a, "-")
		if name, value, found := strings.Cut(flagPart, "="); found {
			if name == "sweep" && value != "" {
				return true
			}
			continue
		}
		// Separate-argument form: -sweep <value>
		if flagPart == "sweep" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && args[i+1] != "" {
			return true
		}
	}
	return false
}

// currentOrgNameForDiagnostics returns the authenticated organization's name,
// best-effort, for the refusal message only. A sweep is never allowed or
// refused on the basis of this value - it exists so the person reading the
// refusal learns WHICH org they were pointed at, which is the fact that took
// hours to surface on 2026-08-03. Returns "" when it cannot be determined.
func currentOrgNameForDiagnostics() string {
	client, err := GetTestClient()
	if err != nil {
		return ""
	}
	resp, err := client.DoRequest(context.Background(), "GET", "/api/v2/userinfo", nil)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var info struct {
		Result struct {
			Organizations []struct {
				Name string `json:"name"`
			} `json:"organizations"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return ""
	}
	if len(info.Result.Organizations) == 0 {
		return ""
	}
	return info.Result.Organizations[0].Name
}

// assertSweepTargetOrg returns nil only when the acctest fixture cloud resolves
// in the org these credentials authenticate into. Every other outcome is an
// error, i.e. a refusal to sweep.
func assertSweepTargetOrg() error {
	id, _, err := resolveCloudNameToIDCore(defaultKnownGoodCloudName)
	if err != nil {
		where := ""
		if name := currentOrgNameForDiagnostics(); name != "" {
			where = fmt.Sprintf("\n  These credentials authenticate into organization: %q", name)
		}
		return fmt.Errorf(
			"cannot confirm this is the acceptance-test organization.\n"+
				"  Looked for the fixture cloud %q and could not resolve it: %v%s\n"+
				"  Sweepers delete real resources named tfacc-*, tf-test-* and tfprovider-* across\n"+
				"  clouds, projects, services, compute configs, container images, invitations and\n"+
				"  MACHINE POOLS. Refusing rather than risk deleting resources in the wrong org.\n"+
				"  If this IS the right org, the fixture cloud is missing - restore it before sweeping",
			defaultKnownGoodCloudName, err, where)
	}
	fmt.Fprintf(os.Stderr, "[sweep] target org confirmed: fixture cloud %s resolved to %s\n",
		defaultKnownGoodCloudName, id)
	return nil
}
