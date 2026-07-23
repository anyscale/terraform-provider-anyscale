package acctest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// protoV6ProviderFactoriesWithEcho extends this package's standard ProtoV6ProviderFactories
// (helpers.go) with the echoprovider.NewProviderServer() "echo" provider (both protocol 6, no
// muxing needed). Every ephemeral resource acceptance test in this package - this file and the
// sibling ephemeral_service_credentials_acc_test.go - needs this instead of the plain anyscale-
// only factories, since asserting on an ephemeral resource's Open output requires capturing it
// into a real managed resource's state first. See the "Echo Provider Pattern" section of
// .claude/skills/provider-test-patterns/references/ephemeral.md.
var protoV6ProviderFactoriesWithEcho = map[string]func() (tfprotov6.ProviderServer, error){
	"anyscale": providerserver.NewProtocol6WithError(provider.NewFramework("test")()),
	"echo":     echoprovider.NewProviderServer(),
}

// jsonStringOrNull renders s as a quoted JSON string, or the literal JSON null if s is nil. Used
// by every mock HTTP handler in both ephemeral acceptance test files so that a null case always
// emits an explicit "field": null in the raw response body rather than omitting the key - the
// test plan's hard rule (.crystl/quest/PR1-TEST-PLAN.md): omitting the key is the exact
// false-green shape that shipped the mount_targets bug (v0.15.2 era), since a mock missing a
// field can pass against broken parsing code and prove nothing.
func jsonStringOrNull(s *string) string {
	if s == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *s)
}

// mockSystemClusterCredentialsServer is a stateful httptest server serving only the two
// endpoints anyscale_system_cluster_credentials' Open ever calls: the decorated_sessions
// existence oracle (findSystemWorkloadCluster) and the system_workload describe endpoint
// (describeSystemWorkload). Unlike newMockSystemClusterServer
// (resource_system_cluster_lifecycle_acc_test.go), which also serves the full anyscale_cloud /
// anyscale_system_cluster resource surface, the mock-tier tests below never declare a managed
// anyscale_cloud or anyscale_system_cluster resource in HCL at all - they call the ephemeral
// resource directly against a literal cloud_id string, so only these two endpoints are needed.
type mockSystemClusterCredentialsServer struct {
	mu sync.Mutex

	hasCluster             bool
	status                 string
	workloadServiceURL     *string
	workloadServiceURLAuth *string

	describeCallCount   int
	describeStartValues []string // raw start_cluster query values seen, in call order
}

// newMockSystemClusterCredentialsServer starts a mock with no System Cluster session registered
// (hasCluster: false, the zero value) - call setState before running a test against it.
func newMockSystemClusterCredentialsServer(t *testing.T, cloudID string) (*httptest.Server, *mockSystemClusterCredentialsServer) {
	t.Helper()
	s := &mockSystemClusterCredentialsServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/decorated_sessions/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		hasCluster := s.hasCluster
		s.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		if !hasCluster {
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{
			"results": [{"id": "cluster_%[1]s", "cloud_id": %[1]q, "is_system_cluster": true, "cloud": null}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`, cloudID)
	})

	mux.HandleFunc("/api/v2/system_workload/"+cloudID+"/describe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on describe", r.Method)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// The create-on-read guard under test: Open must always pass start_cluster=false. Recorded
		// here (not asserted here) so callers can assert on it after resource.Test returns.
		startCluster := r.URL.Query().Get("start_cluster")

		s.mu.Lock()
		s.describeCallCount++
		s.describeStartValues = append(s.describeStartValues, startCluster)
		status := s.status
		urlPtr := s.workloadServiceURL
		authPtr := s.workloadServiceURLAuth
		s.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {
			"cluster_id": %[1]q,
			"workload_service_url": %[2]s,
			"workload_service_url_auth": %[3]s,
			"status": %[4]q,
			"is_enabled": true
		}}`, "cluster_"+cloudID, jsonStringOrNull(urlPtr), jsonStringOrNull(authPtr), status)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, s
}

// setState configures the mock's scenario. hasCluster toggles whether decorated_sessions reports
// a matching System Cluster session (false is the "no System Cluster exists yet" case - describe
// must never be called when this is false). status/workloadServiceURL/workloadServiceURLAuth
// drive describe's response when hasCluster is true; a nil pointer renders as an explicit JSON
// null (see jsonStringOrNull).
func (s *mockSystemClusterCredentialsServer) setState(hasCluster bool, status string, workloadServiceURL, workloadServiceURLAuth *string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasCluster = hasCluster
	s.status = status
	s.workloadServiceURL = workloadServiceURL
	s.workloadServiceURLAuth = workloadServiceURLAuth
}

func (s *mockSystemClusterCredentialsServer) snapshot() (describeCallCount int, describeStartValues []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.describeCallCount, append([]string(nil), s.describeStartValues...)
}

// systemClusterCredentialsConfig builds an anyscale_system_cluster_credentials ephemeral
// resource block plus an echo capture resource pointed at the mock server. Deliberately no
// anyscale_cloud or anyscale_system_cluster managed resource in HCL at all - see the package doc
// comment on mockSystemClusterCredentialsServer above for why the mock only needs to serve two
// endpoints.
func systemClusterCredentialsConfig(serverURL, cloudID, echoResourceName string) string {
	return fmt.Sprintf(`
%s

ephemeral "anyscale_system_cluster_credentials" "test" {
  cloud_id = %q
}

provider "echo" {
  data = ephemeral.anyscale_system_cluster_credentials.test
}

resource "echo" %q {}
`, testAccProviderBlock(serverURL), cloudID, echoResourceName)
}

// openSystemClusterCredentialsDirect calls anyscale_system_cluster_credentials' Open method
// directly (bypassing the Terraform CLI entirely) against the given mock server, and returns the
// resulting model plus diagnostics.
//
// This exists ONLY because terraform-plugin-testing has no supported way to assert on a
// WARNING-severity diagnostic: TestStep.ExpectError is checked against an actual returned command
// ERROR (helper/resource/testing_new.go's `step.ExpectError.MatchString(err.Error())`, gated
// behind `if err != nil`) - a warning-only diagnostic never produces a non-nil err, since the
// plan/apply still succeeds regardless. There is also no plan/state artifact an ephemeral
// resource leaves behind afterward for a Check/statecheck to inspect. So the only way to assert
// on this warning's exact title/detail text - which the test plan requires, to prove cases 2/3/4
// below all share the same underlying message pattern rather than three different messages for
// one condition - is to call Open directly and read resp.Diagnostics.
//
// The resource.Test + echo assertions elsewhere in this file remain the primary proof that the
// VALUE contract (null vs. non-null, exact shape) holds through the real Terraform CLI/protocol
// plumbing; this helper only supplements that with the one thing the CLI harness cannot see.
func openSystemClusterCredentialsDirect(t *testing.T, serverURL, cloudID string) (*provider.SystemClusterCredentialsEphemeralResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	er := provider.NewSystemClusterCredentialsEphemeralResource()

	schemaResp := &ephemeral.SchemaResponse{}
	er.Schema(ctx, ephemeral.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema() returned diagnostics: %s", schemaResp.Diagnostics)
	}

	configurable, ok := er.(ephemeral.EphemeralResourceWithConfigure)
	if !ok {
		t.Fatal("anyscale_system_cluster_credentials does not implement EphemeralResourceWithConfigure")
	}
	configureResp := &ephemeral.ConfigureResponse{}
	configurable.Configure(ctx, ephemeral.ConfigureRequest{
		ProviderData: provider.NewClientWithToken(serverURL, "mock-token"),
	}, configureResp)
	if configureResp.Diagnostics.HasError() {
		t.Fatalf("Configure() returned diagnostics: %s", configureResp.Diagnostics)
	}

	configObj := types.ObjectValueMust(
		map[string]attr.Type{
			"cloud_id":                  types.StringType,
			"workload_service_url":      types.StringType,
			"workload_service_url_auth": types.StringType,
		},
		map[string]attr.Value{
			"cloud_id":                  types.StringValue(cloudID),
			"workload_service_url":      types.StringNull(),
			"workload_service_url_auth": types.StringNull(),
		},
	)
	rawVal, err := configObj.ToTerraformValue(ctx)
	if err != nil {
		t.Fatalf("failed to build ephemeral.OpenRequest.Config.Raw: %s", err)
	}

	// OpenResponse.Result must be pre-populated with the schema (and, mirroring the real framework
	// server plumbing in internal/fwserver/server_openephemeralresource.go, a copy of the config's
	// raw value) before calling Open directly like this - resp.Result.Schema is an interface field
	// with no usable zero value, and Open's own resp.Result.Set(ctx, &config) call panics with a nil
	// pointer dereference deep inside fwschemadata.Data.Set if it is left unset. The real
	// provider-server RPC path always does this for every ephemeral resource before invoking Open;
	// bypassing that path (as this direct-call helper deliberately does) means reproducing it here.
	openResp := &ephemeral.OpenResponse{
		Result: tfsdk.EphemeralResultData{
			Schema: schemaResp.Schema,
			Raw:    rawVal.Copy(),
		},
	}
	er.Open(ctx, ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Raw:    rawVal,
			Schema: schemaResp.Schema,
		},
	}, openResp)

	var model provider.SystemClusterCredentialsEphemeralResourceModel
	if !openResp.Diagnostics.HasError() {
		openResp.Diagnostics.Append(openResp.Result.Get(ctx, &model)...)
	}

	return &model, openResp.Diagnostics
}

// assertSystemClusterAuthNullWarning asserts that diags contains exactly one warning, titled
// exactly "workload_service_url_auth Is Null" (the schema's own documented title - see
// ephemeral_system_cluster_credentials.go's MarkdownDescription), with detail text matching the
// current wording: "Cloud {cloudID} has no live workload_service_url_auth available: {wantReason}."
func assertSystemClusterAuthNullWarning(t *testing.T, diags diag.Diagnostics, cloudID, wantReason string) {
	t.Helper()

	if diags.HasError() {
		t.Fatalf("Open returned unexpected error diagnostics: %s", diags.Errors())
	}

	warnings := diags.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("got %d warning diagnostic(s), want exactly 1: %v", len(warnings), warnings)
	}

	const wantSummary = "workload_service_url_auth Is Null"
	if got := warnings[0].Summary(); got != wantSummary {
		t.Errorf("warning summary = %q, want %q", got, wantSummary)
	}

	wantDetail := fmt.Sprintf("Cloud %s has no live workload_service_url_auth available: %s.", cloudID, wantReason)
	if got := warnings[0].Detail(); got != wantDetail {
		t.Errorf("warning detail = %q, want %q", got, wantDetail)
	}
}

// TestAccSystemClusterCredentialsEphemeralResource is the mock-tier suite for
// anyscale_system_cluster_credentials, covering every scenario in the locked test plan
// (.crystl/quest/PR1-TEST-PLAN.md): Running (case 1), exists-but-not-Running (case 2), no cluster
// exists at all (case 3, including the create-on-read guard - the single most important
// assertion in this file), the Running-but-auth-still-null edge case added during architect
// review (case 4), and a fresh-Open-not-stale check across two steps (case 5).
func TestAccSystemClusterCredentialsEphemeralResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	t.Run("ClusterRunning", func(t *testing.T) {
		cloudID := "cld_sccred_running"
		server, mock := newMockSystemClusterCredentialsServer(t, cloudID)
		url := "https://sc-running.example.com"
		auth := url + "/auth/?token=mock-token-abc123"
		mock.setState(true, "Running", &url, &auth)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: systemClusterCredentialsConfig(server.URL, cloudID, "test_running"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_running", tfjsonpath.New("data").AtMapKey("workload_service_url"), knownvalue.StringExact(url)),
						statecheck.ExpectKnownValue("echo.test_running", tfjsonpath.New("data").AtMapKey("workload_service_url_auth"), knownvalue.StringExact(auth)),
					},
				},
			},
		})

		count, starts := mock.snapshot()
		if count == 0 {
			t.Fatal("describe was never called even though a System Cluster session exists")
		}
		for _, v := range starts {
			if v != "false" {
				t.Errorf("describe called with start_cluster=%s, want \"false\" (create-on-read guard)", v)
			}
		}

		// A Running cluster with a real live auth value must NOT warn.
		_, diags := openSystemClusterCredentialsDirect(t, server.URL, cloudID)
		if diags.HasError() || len(diags.Warnings()) != 0 {
			t.Errorf("Open returned unexpected diagnostics for a Running cluster with a live auth value: %s", diags)
		}
	})

	t.Run("ClusterExistsNotRunning", func(t *testing.T) {
		cloudID := "cld_sccred_notrunning"
		server, mock := newMockSystemClusterCredentialsServer(t, cloudID)
		url := "https://sc-notrunning.example.com"
		mock.setState(true, "StartingUp", &url, nil)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: systemClusterCredentialsConfig(server.URL, cloudID, "test_notrunning"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_notrunning", tfjsonpath.New("data").AtMapKey("workload_service_url_auth"), knownvalue.Null()),
					},
				},
			},
		})

		_, diags := openSystemClusterCredentialsDirect(t, server.URL, cloudID)
		assertSystemClusterAuthNullWarning(t, diags, cloudID, "the System Cluster is currently StartingUp, not Running")
	})

	t.Run("NoClusterExists", func(t *testing.T) {
		cloudID := "cld_sccred_nocluster"
		server, mock := newMockSystemClusterCredentialsServer(t, cloudID)
		mock.setState(false, "", nil, nil)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: systemClusterCredentialsConfig(server.URL, cloudID, "test_nocluster"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_nocluster", tfjsonpath.New("data").AtMapKey("workload_service_url"), knownvalue.Null()),
						statecheck.ExpectKnownValue("echo.test_nocluster", tfjsonpath.New("data").AtMapKey("workload_service_url_auth"), knownvalue.Null()),
					},
				},
			},
		})

		// THE SINGLE MOST IMPORTANT ASSERTION IN THIS FILE (per the test plan): describe must never
		// be called when no System Cluster session exists for this cloud. A naive Open that skipped
		// the existence check would silently provision a real cluster via describeSystemWorkload's
		// create-on-read side effect during what a user thinks is a side-effect-free credentials
		// read. Mutation-proven: temporarily forcing Open to always take the "cluster exists" branch
		// makes this assertion fail (verified by hand, then reverted - see the PR description / task
		// summary for the byte-identical diff confirmation).
		if count, _ := mock.snapshot(); count != 0 {
			t.Fatalf("describe was called %d time(s) when no System Cluster session exists for this cloud - "+
				"Open must never call describeSystemWorkload without first confirming a cluster exists via "+
				"findSystemWorkloadCluster", count)
		}

		_, diags := openSystemClusterCredentialsDirect(t, server.URL, cloudID)
		assertSystemClusterAuthNullWarning(t, diags, cloudID, "no System Cluster exists yet for this cloud")

		// Re-check after the direct call too - two independent invocations against the same mock,
		// both must leave the create-on-read guard fully untripped.
		if count, _ := mock.snapshot(); count != 0 {
			t.Fatalf("describe was called %d time(s) across the CLI apply and the direct Open call combined, want 0", count)
		}
	})

	// RunningButAuthNotYetAvailable is the edge case added during architect review: the wire can
	// report status Running while workload_service_url_auth is still null (not yet materialized).
	// The warning must fire on the ACTUAL null value, not on system-cluster state - this is its own
	// scenario, not a variation of ClusterExistsNotRunning above (the previous implementation gated
	// on state and would have silently skipped the warning in exactly this case).
	t.Run("RunningButAuthNotYetAvailable", func(t *testing.T) {
		cloudID := "cld_sccred_runningnullauth"
		server, mock := newMockSystemClusterCredentialsServer(t, cloudID)
		url := "https://sc-runningnullauth.example.com"
		mock.setState(true, "Running", &url, nil)

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: systemClusterCredentialsConfig(server.URL, cloudID, "test_runningnullauth"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_runningnullauth", tfjsonpath.New("data").AtMapKey("workload_service_url_auth"), knownvalue.Null()),
					},
				},
			},
		})

		_, diags := openSystemClusterCredentialsDirect(t, server.URL, cloudID)
		assertSystemClusterAuthNullWarning(t, diags, cloudID, "the System Cluster is currently Running, but a live credential is not yet available")
	})

	// FreshOpenNotStale proves a second, later Open reflects newly-changed mock data rather than a
	// stale/cached prior result. Per the echoprovider multi-step guidance: always a new echo
	// resource per step, never reuse one - reusing one would silently pass this test by replaying
	// step 1's frozen echo state instead of actually observing a fresh Open.
	t.Run("FreshOpenNotStale", func(t *testing.T) {
		cloudID := "cld_sccred_freshopen"
		server, mock := newMockSystemClusterCredentialsServer(t, cloudID)
		url1 := "https://sc-fresh-1.example.com"
		auth1 := url1 + "/auth/?token=mock-token-fresh1"
		mock.setState(true, "Running", &url1, &auth1)

		url2 := "https://sc-fresh-2.example.com"
		auth2 := url2 + "/auth/?token=mock-token-fresh2"

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
			TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
			Steps: []resource.TestStep{
				{
					Config: systemClusterCredentialsConfig(server.URL, cloudID, "test_fresh_1"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_fresh_1", tfjsonpath.New("data").AtMapKey("workload_service_url_auth"), knownvalue.StringExact(auth1)),
					},
				},
				{
					PreConfig: func() {
						mock.setState(true, "Running", &url2, &auth2)
					},
					Config: systemClusterCredentialsConfig(server.URL, cloudID, "test_fresh_2"),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("echo.test_fresh_2", tfjsonpath.New("data").AtMapKey("workload_service_url_auth"), knownvalue.StringExact(auth2)),
					},
				},
			},
		})
	})
}

// TestAccSystemClusterCredentialsEphemeralResource_RealInfra proves the NON-NULL Running shape
// against live infra - the null-vs-absent question itself is already source-definitive per the
// test plan's architect-traced state matrix and does not need a real run. Reuses whatever
// cloud/System-Cluster fixture GetTestCloudID resolves; assumes it already has a Running System
// Cluster (provisioning that fixture is owned elsewhere in this quest, not by this test) and does
// not create or start one itself. Exact token/host values are real secrets/infra and cannot be
// pinned, so these assert SHAPE only - the mock-tier ClusterRunning subtest above is the
// exact-value equivalent.
func TestAccSystemClusterCredentialsEphemeralResource_RealInfra(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	SkipIfNoRealInfra(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactoriesWithEcho,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
ephemeral "anyscale_system_cluster_credentials" "test" {
  cloud_id = %q
}

provider "echo" {
  data = ephemeral.anyscale_system_cluster_credentials.test
}

resource "echo" "test_realinfra" {}
`, cloudID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"echo.test_realinfra",
						tfjsonpath.New("data").AtMapKey("workload_service_url"),
						knownvalue.StringRegexp(regexp.MustCompile(`^https://`)),
					),
					statecheck.ExpectKnownValue(
						"echo.test_realinfra",
						tfjsonpath.New("data").AtMapKey("workload_service_url_auth"),
						knownvalue.StringRegexp(regexp.MustCompile(`^https://.+/auth/\?token=.+$`)),
					),
				},
			},
		},
	})
}
