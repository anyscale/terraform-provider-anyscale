package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// schedulerConfigValidateUnavailableServer serves ONLY
// /api/v2/scheduler/config/validate, answering every request with a status
// this package's own classifier (schedulerServerEvaluatedDocument,
// scheduler_api.go) does not recognize as "the server evaluated the
// document" - neither 400 nor 422. That is exactly the
// ErrSchedulerValidationUnavailable branch in ModifyPlan, not the
// rejected-by-the-server branch: a 501 means the endpoint could not evaluate
// anything, which is the scenario gap 1 is about.
//
// It also records the raw request body of every hit, so a test can assert the
// document that reached the wire is the one under test and not an empty
// fixture that happened to trigger the same warning regardless of content -
// see the sentinel assertion below.
type schedulerConfigValidateUnavailableServer struct {
	mu     sync.Mutex
	hits   int
	bodies []string
}

func newSchedulerConfigValidateUnavailableServer(t *testing.T) (*httptest.Server, *schedulerConfigValidateUnavailableServer) {
	t.Helper()
	s := &schedulerConfigValidateUnavailableServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/scheduler/config/validate", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits++
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		w.WriteHeader(http.StatusNotImplemented)
	})
	return httptest.NewServer(mux), s
}

func (s *schedulerConfigValidateUnavailableServer) Hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *schedulerConfigValidateUnavailableServer) LastBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return ""
	}
	return s.bodies[len(s.bodies)-1]
}

// runSchedulerConfigModifyPlan drives ModifyPlan directly, the same harness
// shape as runCloudAccessModifyPlanWithClient (cloud_access_reconcile_test.go)
// adapted for one difference forced by this resource's own gate: that
// precedent leaves ModifyPlanRequest.Config zero-valued because the
// cloud_access gate reads req.Plan. This resource's gate
// (resource_scheduler_config.go:703) reads req.Config.Raw.IsFullyKnown() and
// req.Config.Get, so Config is built from the SAME raw value as Plan here -
// measured, not assumed: an omitted Config passes that IsFullyKnown check (a
// zero tftypes.Value reports fully known) and then panics on Get, which would
// make this helper unusable for anything but the destroy case if Config were
// left zero.
//
// planned must be non-null: a null Plan is the destroy signal ModifyPlan
// returns on before ever reaching the Config gate (resource_scheduler_config.go:688),
// so a null-Plan harness cannot exercise this code path at all - it would
// "pass" a no-warning assertion for reaching an early return, not for the gate
// judging the config fully known.
func runSchedulerConfigModifyPlan(t *testing.T, client *Client, planned *SchedulerConfigResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	r := &SchedulerConfigResource{client: client}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	holder := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := holder.Set(ctx, planned); diags.HasError() {
		t.Fatalf("failed to build the plan fixture: %v", diags)
	}

	resp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)},
		Plan:   tfsdk.Plan{Schema: schemaResp.Schema, Raw: holder.Raw},
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: holder.Raw},
	}, resp)
	return resp.Diagnostics
}

// TestSchedulerConfigModifyPlan_WarnsAndProceedsWhenValidateIsUnreachable is
// gap 1 from the PR #280 review: nothing today proves that an unreachable
// validate endpoint produces the warning the docs promise (AddWarning,
// "Anyscale Scheduler Configuration Not Validated") rather than silently
// doing nothing. Silence is the dangerous outcome here, and it is the same
// user-visible shape as the bug #280 fixed - a plan that looks fine either
// way - which is why this needs its own coverage rather than living as a
// footnote on the cross-reference test.
//
// This is route 2 of the two the team agreed gap 1 needs (route 1 is the
// resource.Test-driven acceptance test asserting hit-count + no ExpectError;
// it cannot assert the WARNING'S CONTENT at all -
// terraform-plugin-testing v1.16.0 has no ExpectWarning mechanism, confirmed
// by a zero-hit grep across the module with ExpectError as a 13-file positive
// control). Route 2 is the one that actually closes the gap: it is the only
// place the exact diagnostic summary can be asserted, and a build that
// deleted the AddWarning call outright would still pass route 1 (call
// happened, plan proceeded) while failing this one.
//
// Mutation-proof: this test was run against a build with the AddWarning call
// deleted from the ErrSchedulerValidationUnavailable branch in ModifyPlan
// (resource_scheduler_config.go) - it failed, finding zero warnings where it
// expects exactly one. Restoring the call turns it back green.
func TestSchedulerConfigModifyPlan_WarnsAndProceedsWhenValidateIsUnreachable(t *testing.T) {
	server, mock := newSchedulerConfigValidateUnavailableServer(t)
	defer server.Close()
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	const flavorName = "tfacc-gap1-sentinel-flavor"
	planned := &SchedulerConfigResourceModel{
		ResourceFlavors: []schedulerResourceFlavorModel{
			{Name: types.StringValue(flavorName)},
		},
	}

	diags := runSchedulerConfigModifyPlan(t, client, planned)

	if got := mock.Hits(); got != 1 {
		t.Fatalf("expected exactly 1 call to the validate endpoint, got %d", got)
	}

	// Sentinel: the warning's presence alone would also be produced by a
	// config that never carried the fixture at all - forge's own control run
	// got this same warning on a populated-but-all-null Config whose wire body
	// was `{"config":{}}`, an empty document, because the mock's 501 fires
	// unconditionally regardless of what was sent. Asserting the flavor name
	// reached the wire converts "a POST happened" into "the document under
	// test was POSTed", and it fails under either direction of the
	// zero/omitted-Config hazard the team measured on this harness.
	if body := mock.LastBody(); !strings.Contains(body, flavorName) {
		t.Fatalf("expected the validate request body to contain sentinel %q, got: %s", flavorName, body)
	}

	if diags.ErrorsCount() != 0 {
		t.Fatalf("expected no error diagnostics (validate-unavailable must warn, not error), got: %v", diags)
	}

	var found bool
	for _, d := range diags {
		if d.Severity() == diag.SeverityWarning && d.Summary() == "Anyscale Scheduler Configuration Not Validated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning diagnostic summarized %q, got: %v",
			"Anyscale Scheduler Configuration Not Validated", diags)
	}
}
