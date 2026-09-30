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
// `make sweep` runs seven registered sweepers across six endpoint families
// (clouds, projects, services, compute configs, container images - builds and
// registries - and organization invitations) and matches three name prefixes
// (see sweepableResourcePrefixes). A sweep aimed at the wrong organization is
// therefore one of the most destructive things this repo can do with valid
// credentials.
//
// This is not hypothetical: credentials in ~/.anyscale/credentials.json have
// been scoped to a personal staging org for a whole session, and acceptance
// tests leaked clouds into it. A sweep from the same shell would have targeted
// that org, and only the absence of test-looking name prefixes there would
// have protected its real resources.
//
// WHY THE PINNED FIXTURE CLOUD IS THE ORG SIGNAL, and why no org name is
// committed here: defaultKnownGoodCloudName ("tfp-test-aws-useast1-STATIC")
// exists only in the acctest org. It resolves in CI (the "Using default
// known-good test cloud" log line from GetTestCloudID in both acctest shards)
// and is absent from other orgs, so its resolution genuinely carries org
// identity, and a separate expected-org-name constant would be a second
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
				"  clouds, projects, services, compute configs, container images and invitations.\n"+
				"  Refusing rather than risk deleting resources in the wrong org.\n"+
				"  If this IS the right org, the fixture cloud is missing - restore it before sweeping",
			defaultKnownGoodCloudName, err, where)
	}
	fmt.Fprintf(os.Stderr, "[sweep] target org confirmed: fixture cloud %s resolved to %s\n",
		defaultKnownGoodCloudName, id)
	return nil
}
