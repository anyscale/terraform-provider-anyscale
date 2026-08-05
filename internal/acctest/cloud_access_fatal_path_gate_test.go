package acctest

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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

// This file answers AC-21a only (docs/decisions/rbac-surface-consolidation):
// does writing the FULL PLANNED `member` map to state after a mid-reconcile
// failure - never erroring, only warning - trip Terraform Core's "provider
// produced inconsistent result after apply" for a collection with no
// Computed elements? Answered: no.
//
// AC-21b (the criterion, not the design question) is NOT met by this file:
// it needs the real nested `member` schema, since Core checks consistency
// per attribute PATH and this gate's flat `map[string]string` has no path on
// which `deny_roles`' null-vs-empty-list divergence could occur. Do not read
// the two as the same shape.
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

// fatalPathGateResourceModel mirrors two properties of the real schema:
// `members`, a Required flat map with no Computed elements (see file header
// for why this is not the real nested `member`), and `shortfall`, a Computed
// list with no plan modifier like `unmanaged_grants`. `simulate_partial_failure`
// stands in for "the reconcile hit a fatal error partway through", since
// there is no real backend here to fail.
type fatalPathGateResourceModel struct {
	ID                     types.String `tfsdk:"id"`
	Members                types.Map    `tfsdk:"members"`
	Shortfall              types.List   `tfsdk:"shortfall"`
	SimulatePartialFailure types.Bool   `tfsdk:"simulate_partial_failure"`
	// SimulateReadBackDivergence stands in for a post-apply re-read that
	// comes back DIFFERENT from what was planned - e.g. an eventually-
	// consistent authorization-service read that hasn't caught up yet. See
	// TestAccCloudAccessReadBackDivergenceGateResource (AC-21c).
	SimulateReadBackDivergence types.Bool `tfsdk:"simulate_read_back_divergence"`
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
			"simulate_partial_failure":      rschema.BoolAttribute{Optional: true},
			"simulate_read_back_divergence": rschema.BoolAttribute{Optional: true},
		},
	}
}

// fatalPathGateShortfall computes the AC-21a shortfall list for a given
// SimulatePartialFailure trigger - shared by Create and Update so the two
// lifecycle methods can't drift on what "a simulated fatal error" means.
func fatalPathGateShortfall(ctx context.Context, simulateFailure bool) (types.List, diag.Diagnostics) {
	entries := []string{}
	if simulateFailure {
		entries = []string{"member-that-the-simulated-fatal-error-never-reached"}
	}
	return types.ListValueFrom(ctx, types.StringType, entries)
}

// fatalPathGateDiverge stands in for a stale post-apply re-read: it returns a
// map that differs from planned in exactly one value, the shape a genuinely
// eventually-consistent read would produce. Used only when
// SimulateReadBackDivergence is set - the happy path and the AC-21a path
// both write planned unchanged.
func fatalPathGateDiverge(ctx context.Context, planned types.Map) (types.Map, diag.Diagnostics) {
	elements := planned.Elements()
	diverged := make(map[string]attr.Value, len(elements))
	for k, v := range elements {
		diverged[k] = v
	}
	for k := range diverged {
		diverged[k] = types.StringValue("stale-value-from-a-lagging-read")
		break // one changed value is enough to diverge from the plan.
	}
	return types.MapValue(types.StringType, diverged)
}

// Create writes plan.Members verbatim to state - the full planned map, never
// a partial one - regardless of SimulatePartialFailure. That is the AC-21a
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

	// AC-21c's trigger: overwrite plan.Members with something that differs
	// from the plan, standing in for a stale post-apply re-read. This is the
	// ONLY place this file ever writes something other than the planned
	// value for `members` - every other path (including AC-21a's) writes it
	// unchanged.
	if plan.SimulateReadBackDivergence.ValueBool() {
		diverged, divergeDiags := fatalPathGateDiverge(ctx, plan.Members)
		resp.Diagnostics.Append(divergeDiags...)
		plan.Members = diverged
	}

	// THE ASSERTION THIS FILE EXISTS FOR (AC-21a's half): absent divergence,
	// plan.Members is untouched here. The full planned map goes to state
	// exactly as planned, never trimmed to "only what a real backend call
	// would have reached" - that is the behavior AC-21a asks whether Core
	// will accept.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Update mirrors Create - see its comment. Duplicated rather than factored
// through a shared helper because *resource.CreateResponse and
// *resource.UpdateResponse share no common interface for .State/.Diagnostics
// in this framework version, and this file's only job is to be a faithful,
// legible AC-21a gate, not a reusable library.
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

	if plan.SimulateReadBackDivergence.ValueBool() {
		diverged, divergeDiags := fatalPathGateDiverge(ctx, plan.Members)
		resp.Diagnostics.Append(divergeDiags...)
		plan.Members = diverged
	}

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

// TestAccCloudAccessFatalPathGateResource is AC-21a (see the file header
// for why this is not also AC-21b). Step 1 creates with
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
				// No ExpectError: a clean apply here IS the AC-21a answer.
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

// TestAccCloudAccessReadBackDivergenceGateResource is AC-21c: the converse
// AC-21a never tested. AC-21a proved only that writing a value MATCHING the
// plan for a Required, non-Computed collection succeeds. The general rule
// that a post-apply read may only touch Computed attributes rests on the
// OTHER direction too - that writing a DIFFERING value for a non-Computed
// attribute actually trips "provider produced inconsistent result after
// apply" rather than being silently tolerated. This is the missing half of
// the mechanism, not a restatement of AC-21a.
//
// Create writes a `members` value differing from the plan in one entry,
// standing in for a stale post-apply re-read (a general eventual-consistency
// hazard, not specific to a grant failure). The apply must fail with Core's
// own inconsistency error, not a resource-raised one.
func TestAccCloudAccessReadBackDivergenceGateResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fatalPathGateProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: `
resource "fatalgate_gate" "test" {
  id = "gate-divergence-1"
  members = {
    "alice@example.com" = "writer"
  }
  simulate_read_back_divergence = true
}
`,
				// The error Core raises for this is a framework-level
				// consistency failure, not anything this fake resource's own
				// Diagnostics ever add - matching on "inconsistent" is
				// specific enough to rule out a coincidental match while not
				// depending on Core's exact wording.
				ExpectError: regexp.MustCompile(`(?i)inconsistent`),
			},
		},
	})
}
