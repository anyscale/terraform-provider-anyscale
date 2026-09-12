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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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

// schedulerConfigGap2KnownRaw builds a fully-known raw value for this
// resource's schema with resource_flavors[0].name set to flavorName and every
// other attribute null (Computed version/created_at/creator_id; the other
// three top-level sections unset). SchedulerConfigResourceModel has no way to
// express "unknown" - every field is a concrete types.T value or absent - so
// gap 2's fixtures start from this fully-known value and then flip exactly
// one leaf to unknown with tftypes.Transform, rather than hand-authoring the
// whole schema tree by hand.
func schedulerConfigGap2KnownRaw(t *testing.T, flavorName string) (schema.Schema, tftypes.Value) {
	t.Helper()
	ctx := context.Background()

	r := &SchedulerConfigResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	planned := &SchedulerConfigResourceModel{
		ResourceFlavors: []schedulerResourceFlavorModel{
			{Name: types.StringValue(flavorName)},
		},
	}
	holder := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
	if diags := holder.Set(ctx, planned); diags.HasError() {
		t.Fatalf("failed to build the plan fixture: %v", diags)
	}
	return schemaResp.Schema, holder.Raw
}

// schedulerConfigGap2FlavorNamePath is the path to the one leaf gap 2 flips
// between known and unknown: resource_flavors[0].name. It is inside a
// serialized section (resource_flavors), not a top-level attribute -
// deliberately, per the architect's requirement 2. The shipped gate
// (req.Config.Raw.IsFullyKnown(), resource_scheduler_config.go:703) is
// whole-config today, so an unknown at top level and one inside a section are
// indistinguishable to it - but this test is written against the documented
// contract (validation only ever concerns the four input sections), not
// against the current gate's shape, so it keeps testing the right thing if
// the gate is ever narrowed to the serialized subtree.
func schedulerConfigGap2FlavorNamePath() *tftypes.AttributePath {
	return tftypes.NewAttributePath().
		WithAttributeName("resource_flavors").
		WithElementKeyInt(0).
		WithAttributeName("name")
}

// runSchedulerConfigModifyPlanRaw drives ModifyPlan directly from a
// caller-built raw value, the same harness shape as
// runSchedulerConfigModifyPlan but for gap 2's fixtures, which are built by
// mutating a raw tftypes.Value rather than through a Go model - see
// schedulerConfigGap2KnownRaw.
func runSchedulerConfigModifyPlanRaw(t *testing.T, client *Client, s schema.Schema, raw tftypes.Value) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	r := &SchedulerConfigResource{client: client}
	resp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: s, Raw: tftypes.NewValue(raw.Type(), nil)},
		Plan:   tfsdk.Plan{Schema: s, Raw: raw},
		Config: tfsdk.Config{Schema: s, Raw: raw},
	}, resp)
	return resp.Diagnostics
}

// TestSchedulerConfigModifyPlan_SkipsValidateSilentlyWhenSectionHasUnknownValue
// is gap 2 from the PR #280 review, per the architect's spec: nothing today
// proves that an unknown value inside one of the four config sections (as
// opposed to the whole config being null, which the destroy short-circuit
// already covers) makes ModifyPlan skip the cross-reference validate call
// silently - zero diagnostics, not a warning - rather than either calling
// validate against a partial document or erroring on the unknown itself.
//
// Built on the same unit harness as route 2 (an unclassifiable validate
// response was never involved here) rather than as an acceptance test:
// terraform_data would only add a harder setup and an ordering dependency
// with no coverage benefit, since this harness can place an unknown at an
// exact path directly.
//
// The unknown leaf is placed inside resource_flavors (a serialized section),
// not on a top-level attribute like version - see
// schedulerConfigGap2FlavorNamePath's comment for why that placement is the
// one that matters against the documented contract, independent of whether
// the shipped gate happens to be whole-config today.
//
// The positive control subtest is required, not incidental: on its own,
// Hits() == 0 in the skip subtest is equally consistent with "the gate
// correctly skipped" and with "the mock was never wired up" or "the harness
// never reached the client at all." The control fixture differs from the
// skip fixture by exactly that one leaf (unknown vs. the same known string),
// so nothing else can explain a hit-count difference between the two.
//
// Mutation-proof: gated with `if false && !req.Config.Raw.IsFullyKnown()`
// (resource_scheduler_config.go:703) to disable the skip while leaving the
// line otherwise intact, the skip subtest failed with:
//
//	expected zero calls to the validate endpoint when resource_flavors[0].name
//	is unknown, got 1
//
// req.Config.Get did not panic on the unknown leaf - it decoded cleanly into
// SchedulerConfigResourceModel and ModifyPlan proceeded to call validate, so
// the gate's absence surfaced as exactly the hit-count assertion failure this
// test is designed to catch, not a panic or an unrelated encode error.
// Restoring the gate (reverted byte-identical, confirmed via `git diff
// --stat`) turned it back green: 0 hits, 0 diagnostics, positive control
// passing throughout.
func TestSchedulerConfigModifyPlan_SkipsValidateSilentlyWhenSectionHasUnknownValue(t *testing.T) {
	const flavorName = "tfacc-gap2-sentinel-flavor"
	unknownPath := schedulerConfigGap2FlavorNamePath()

	t.Run("unknown leaf inside a serialized section skips validate silently", func(t *testing.T) {
		s, knownRaw := schedulerConfigGap2KnownRaw(t, flavorName)

		unknownRaw, err := tftypes.Transform(knownRaw, func(p *tftypes.AttributePath, v tftypes.Value) (tftypes.Value, error) {
			if p.Equal(unknownPath) {
				return tftypes.NewValue(v.Type(), tftypes.UnknownValue), nil
			}
			return v, nil
		})
		if err != nil {
			t.Fatalf("failed to inject an unknown value at %s: %v", unknownPath, err)
		}

		server, mock := newSchedulerConfigValidateUnavailableServer(t)
		defer server.Close()
		client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

		diags := runSchedulerConfigModifyPlanRaw(t, client, s, unknownRaw)

		if got := mock.Hits(); got != 0 {
			t.Fatalf("expected zero calls to the validate endpoint when resource_flavors[0].name is unknown, got %d", got)
		}
		if len(diags) != 0 {
			t.Fatalf("expected zero diagnostics (skipping must be silent, not a warning), got: %v", diags)
		}
	})

	t.Run("positive control: the identical fixture with that leaf known reaches validate", func(t *testing.T) {
		s, knownRaw := schedulerConfigGap2KnownRaw(t, flavorName)

		server, mock := newSchedulerConfigValidateUnavailableServer(t)
		defer server.Close()
		client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

		_ = runSchedulerConfigModifyPlanRaw(t, client, s, knownRaw)

		if got := mock.Hits(); got != 1 {
			t.Fatalf("expected exactly 1 call to the validate endpoint when resource_flavors[0].name is known (control differs from the skip case only in that leaf), got %d", got)
		}
	})
}
