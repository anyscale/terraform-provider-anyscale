package acctest

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file answers AC-21 (docs/decisions/rbac-surface-consolidation,
// "Acceptance criteria for the write path"), the design blocker the whole
// cloud_access fatal-path story depends on: does writing the FULL PLANNED
// `member` map to state after a real mid-reconcile failure - never erroring,
// only warning and recording the shortfall - trip Terraform Core's
// "provider produced inconsistent result after apply" check? If it does, the
// never-error-after-a-write ruling (see gate 2.1,
// framework_core_state_persistence_gate_test.go) has no safe shape to land
// in and must be revisited before any write path ships.
//
// Answered here with a throwaway fake resource, the same trick gate 2.1
// used, rather than waiting on forge's real implementation - architect's
// direction was to run this early precisely because it does not need one.
// The fake resource's `members` attribute is deliberately built to match the
// real `member` schema's one load-bearing property: none of its nested
// fields (base_role/deny_roles/projects on the real resource) are Computed,
// so Core's plan for `members` is never Unknown - it is always exactly the
// practitioner's config. That shape, not any code this gate resource runs,
// is what forces "return the full planned map or fail the consistency
// check" - this test exists to confirm that reasoning against real Core
// behavior rather than trust it from schema inspection alone (Design
// Verification Policy, gate 2).
//
// The throwaway provider/resource here exist ONLY for this gate - they are
// not part of, and must never be wired into, the real anyscale provider.

type fatalPathGateProvider struct{}

func (p *fatalPathGateProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "fatalgate"
}

func (p *fatalPathGateProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{}
}

func (p *fatalPathGateProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (p *fatalPathGateProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return []func() fwresource.Resource{
		func() fwresource.Resource { return &fatalPathGateResource{} },
	}
}

func (p *fatalPathGateProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// fatalPathGateResourceModel mirrors the two properties of the real schema
// that matter for AC-21: `members` (a Required map with no Computed nested
// fields, exactly like `member`) and `shortfall` (a Computed list with no
// plan modifier, exactly like `unmanaged_grants` - see the "No
// UseStateForUnknown" comment on that attribute in resource_cloud_access.go).
// `simulate_partial_failure` is the only thing this fake resource adds:
// a practitioner-controlled trigger standing in for "the reconcile hit a
// fatal error partway through", since there is no real backend here to fail.
type fatalPathGateResourceModel struct {
	ID                     types.String `tfsdk:"id"`
	Members                types.Map    `tfsdk:"members"`
	Shortfall              types.List   `tfsdk:"shortfall"`
	SimulatePartialFailure types.Bool   `tfsdk:"simulate_partial_failure"`
}

type fatalPathGateResource struct{}

func (r *fatalPathGateResource) Metadata(_ context.Context, req fwresource.MetadataRequest, resp *fwresource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gate"
}

func (r *fatalPathGateResource) Schema(_ context.Context, _ fwresource.SchemaRequest, resp *fwresource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{Required: true},
			"members": rschema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
			},
			"shortfall": rschema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				// Deliberately no PlanModifiers - see the model comment above.
			},
			"simulate_partial_failure": rschema.BoolAttribute{Optional: true},
		},
	}
}

// fatalPathGateShortfall computes the AC-21 shortfall list for a given
// SimulatePartialFailure trigger - shared by Create and Update so the two
// lifecycle methods can't drift on what "a simulated fatal error" means.
func fatalPathGateShortfall(ctx context.Context, simulateFailure bool) (types.List, diag.Diagnostics) {
	entries := []string{}
	if simulateFailure {
		entries = []string{"member-that-the-simulated-fatal-error-never-reached"}
	}
	return types.ListValueFrom(ctx, types.StringType, entries)
}

// Create writes plan.Members verbatim to state - the full planned map, never
// a partial one - regardless of SimulatePartialFailure. That is the AC-21
// behavior under test: a real reconcile would have applied only some of
// these members to a real backend before hitting a fatal error, and the
// design ruling is to report the shortfall (in `shortfall`) and warn, but
// still claim the full planned set in state. It never returns a
// Diagnostics error - success is the point (see gate 2.1: an errored Create
// leaves the resource TAINTED, which is exactly the destructive
// destroy-and-recreate this design exists to avoid).
func (r *fatalPathGateResource) Create(ctx context.Context, req fwresource.CreateRequest, resp *fwresource.CreateResponse) {
	var plan fatalPathGateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	simulateFailure := plan.SimulatePartialFailure.ValueBool()
	shortfall, diags := fatalPathGateShortfall(ctx, simulateFailure)
	resp.Diagnostics.Append(diags...)
	if simulateFailure {
		resp.Diagnostics.AddWarning(
			"Simulated Partial Reconcile Failure",
			"one member's write was not attempted because of a simulated fatal error; recorded in shortfall",
		)
	}
	plan.Shortfall = shortfall

	// THE ASSERTION THIS FILE EXISTS FOR: plan.Members is untouched here. The
	// full planned map goes to state exactly as planned, never trimmed to
	// "only what a real backend call would have reached" - that is the
	// behavior AC-21 asks whether Core will accept.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Update mirrors Create - see its comment. Duplicated rather than factored
// through a shared helper because *resource.CreateResponse and
// *resource.UpdateResponse share no common interface for .State/.Diagnostics
// in this framework version, and this file's only job is to be a faithful,
// legible AC-21 gate, not a reusable library.
func (r *fatalPathGateResource) Update(ctx context.Context, req fwresource.UpdateRequest, resp *fwresource.UpdateResponse) {
	var plan fatalPathGateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	simulateFailure := plan.SimulatePartialFailure.ValueBool()
	shortfall, diags := fatalPathGateShortfall(ctx, simulateFailure)
	resp.Diagnostics.Append(diags...)
	if simulateFailure {
		resp.Diagnostics.AddWarning(
			"Simulated Partial Reconcile Failure",
			"one member's write was not attempted because of a simulated fatal error; recorded in shortfall",
		)
	}
	plan.Shortfall = shortfall

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *fatalPathGateResource) Read(_ context.Context, _ fwresource.ReadRequest, _ *fwresource.ReadResponse) {
}

func (r *fatalPathGateResource) Delete(_ context.Context, _ fwresource.DeleteRequest, _ *fwresource.DeleteResponse) {
}

func fatalPathGateProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fatalgate": providerserver.NewProtocol6WithError(&fatalPathGateProvider{}),
	}
}

// TestAccCloudAccessFatalPathGateResource is AC-21. Step 1 creates with
// simulate_partial_failure=true and two members: if Core's consistency check
// rejects a full-planned-map return after this fake resource's Diagnostics
// carry only a warning (never an error), the apply fails right here with a
// framework-raised "inconsistent result" error, not a resource-raised one -
// the two are distinguishable and this test lets Core speak for itself.
// Step 2 flips the trigger off and re-applies (now through Update, closing
// part of gate 2.1's "Update half remains untested" note as a side effect,
// though that item's real subject is an ERRORED Update, not this one) and
// asserts the plan converges to empty with shortfall cleared - the AC-22/
// AC-24 shape, that a later clean reconcile un-shortfalls what an earlier
// one recorded.
func TestAccCloudAccessFatalPathGateResource(t *testing.T) {
	resourceName := "fatalgate_gate.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fatalPathGateProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: `
resource "fatalgate_gate" "test" {
  id = "gate-fatal-1"
  members = {
    "alice@example.com" = "writer"
    "bob@example.com"   = "collaborator"
  }
  simulate_partial_failure = true
}
`,
				// No ExpectError: a clean apply here IS the AC-21 answer.
				// Core would raise its own "inconsistent result" error if the
				// full-planned-map return were rejected, and that error would
				// surface as a hard test failure below, not a silent skip.
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "members.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "members.alice@example.com", "writer"),
					resource.TestCheckResourceAttr(resourceName, "members.bob@example.com", "collaborator"),
					resource.TestCheckResourceAttr(resourceName, "shortfall.#", "1"),
				),
			},
			{
				Config: `
resource "fatalgate_gate" "test" {
  id = "gate-fatal-1"
  members = {
    "alice@example.com" = "writer"
    "bob@example.com"   = "collaborator"
  }
  simulate_partial_failure = false
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "shortfall.#", "0"),
				),
			},
			{
				// A trailing no-op plan on the converged (shortfall-cleared)
				// config - the AC-13 shape, checked here for the fake
				// resource rather than assumed.
				Config: `
resource "fatalgate_gate" "test" {
  id = "gate-fatal-1"
  members = {
    "alice@example.com" = "writer"
    "bob@example.com"   = "collaborator"
  }
  simulate_partial_failure = false
}
`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
