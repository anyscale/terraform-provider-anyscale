package acctest

import (
	"context"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This file answers cloud_access design gate 2.1 (docs/decisions/rbac-surface-consolidation):
// does Terraform Core persist resource state that a provider wrote via
// resp.State.Set before returning an error diagnostic from Create, Update, or
// Delete? unmanaged_grants' converge-and-record contract is built entirely on
// the answer being yes. Framework source (server_createresource.go) proves
// only that the framework puts such state on the *wire response* - it says
// nothing about what Core, on the other side of that wire, chooses to persist
// to the state file. This is deliberately a real `resource.Test`, not a unit
// test against framework source, per CLAUDE.md's Design Verification Policy.
//
// The throwaway provider/resource here exist ONLY for this gate - they are
// not part of, and must never be wired into, the real anyscale provider.

type coreGateProvider struct{}

func (p *coreGateProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "coregate"
}

func (p *coreGateProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{}
}

func (p *coreGateProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (p *coreGateProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return []func() fwresource.Resource{
		func() fwresource.Resource { return &coreGateResource{} },
	}
}

func (p *coreGateProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// coreGateResourceModel is intentionally minimal: an identifying key the
// practitioner sets, plus a Computed marker the resource uses to prove what
// state Core kept after an errored apply.
type coreGateResourceModel struct {
	ID         types.String `tfsdk:"id"`
	TriggerErr types.Bool   `tfsdk:"trigger_err"`
	Marker     types.String `tfsdk:"marker"`
}

type coreGateResource struct{}

func (r *coreGateResource) Metadata(_ context.Context, req fwresource.MetadataRequest, resp *fwresource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gate"
}

func (r *coreGateResource) Schema(_ context.Context, _ fwresource.SchemaRequest, resp *fwresource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Attributes: map[string]rschema.Attribute{
			"id":          rschema.StringAttribute{Required: true},
			"trigger_err": rschema.BoolAttribute{Optional: true},
			"marker":      rschema.StringAttribute{Computed: true},
		},
	}
}

// Create always writes marker into state FIRST, then errors if trigger_err is
// set. This mirrors this repo's real converge-and-record shape: the resource
// wants a partial result to survive an error return.
func (r *coreGateResource) Create(ctx context.Context, req fwresource.CreateRequest, resp *fwresource.CreateResponse) {
	var data coreGateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Marker = types.StringValue("created-by-create")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if data.TriggerErr.ValueBool() {
		resp.Diagnostics.AddError("gate: create failed deliberately", "state was Set before this error")
		return
	}
}

// Update unconditionally errors with a distinctive message so that, in the
// tests below, seeing it fire is unambiguous proof Core called Update rather
// than Create for a given step.
func (r *coreGateResource) Update(ctx context.Context, req fwresource.UpdateRequest, resp *fwresource.UpdateResponse) {
	var data coreGateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Marker = types.StringValue("update-ran")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError("gate: update-marker fired", "Core called Update, proving prior Create state was persisted")
}

// Read is a no-op passthrough: state is authoritative for this throwaway
// resource, nothing external to refresh from.
func (r *coreGateResource) Read(_ context.Context, _ fwresource.ReadRequest, _ *fwresource.ReadResponse) {
}

// coreGateDeleteFailureBudget lets a test script exactly how many of the next
// Delete calls should fail before Delete starts succeeding - needed because
// resource.Test always performs its own real Delete during cleanup, and that
// cleanup call must succeed once the test's own assertions are done with it.
var coreGateDeleteFailureBudget atomic.Int32

// Delete fails WITHOUT calling resp.State.RemoveResource() while the budget
// is non-zero, leaving whatever was in state untouched. Framework convention
// (and the documented contract this gate exists to confirm) is that an
// errored Delete which never clears state should leave Core's copy of that
// state alone too.
func (r *coreGateResource) Delete(_ context.Context, _ fwresource.DeleteRequest, resp *fwresource.DeleteResponse) {
	if coreGateDeleteFailureBudget.Load() > 0 {
		coreGateDeleteFailureBudget.Add(-1)
		resp.Diagnostics.AddError("gate: delete failed deliberately", "state was left untouched before this error")
	}
}

func coreGateProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"coregate": providerserver.NewProtocol6WithError(&coreGateProvider{}),
	}
}

// TestAccFrameworkCoreGateErroredCreateStatePersistsResource is gate 2.1's
// Create half. Step 1 makes Create write marker then error. If Core had
// discarded that partial state, Core would have no record of this resource
// at all and step 2 would just call Create again, succeeding silently
// (trigger_err is false this time) - no error, test would fail on the
// ExpectError below.
//
// Named TestAcc*Resource, not TestFrameworkCoreGate_*, so ci.yml's
// acctest-resource shard (-run '^TestAcc[A-Za-z]+Resource') actually runs
// it. Under the old name this test was unreachable by any CI workflow:
// lint-and-unit's `make test` never sets TF_ACC (resource.Test self-skips
// without it), and the only invocation that runs the package unfiltered is
// a bare `make testacc`, which no workflow calls. It needs no real
// credentials - the provider under test is the throwaway "coregate" fake -
// so it costs nothing extra in the shard it now runs in.
//
// What actually happens, confirmed by running this: Core PERSISTS the
// partial state AND taints the resource, so step 2 plans a destroy-then-
// create replace rather than a plain update. Delete runs first as part of
// that replace and (budget=1) fails deliberately - proving persistence, but
// via taint/replace, not update. This is a real, load-bearing finding for
// the cloud_access design: a failed Create does not leave a resource in a
// retriable "try the same reconcile again" shape, it leaves a TAINTED
// resource whose next apply is destroy-then-recreate of the WHOLE resource.
func TestAccFrameworkCoreGateErroredCreateStatePersistsResource(t *testing.T) {
	coreGateDeleteFailureBudget.Store(1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: coreGateProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: `
resource "coregate_gate" "test" {
  id          = "gate-1"
  trigger_err = true
}
`,
				ExpectError: regexp.MustCompile(`gate: create failed deliberately`),
			},
			{
				Config: `
resource "coregate_gate" "test" {
  id          = "gate-1"
  trigger_err = false
}
`,
				// Proves persistence: this only fires if Core still has a
				// tainted "gate-1" to destroy. A discarded-state Core would
				// see nothing here and call Create (no error) instead.
				ExpectError: regexp.MustCompile(`gate: delete failed deliberately`),
			},
		},
	})
}

// TestAccFrameworkCoreGateErroredDeleteStatePersistsResource is gate 2.1's
// Delete half, the one flagged as least likely to match Create/Update
// because it is a different Core decision. Step 1 creates cleanly. Step 2
// destroys, which always errors without clearing state. Step 3 re-applies
// the ORIGINAL config with PlanOnly+ExpectNonEmptyPlan(false): an empty plan
// is only possible if Core still believes the resource exists with its
// last-known attributes - i.e. the errored Delete did not wipe it.
//
// Renamed alongside its Create-half sibling above - see that comment for
// why the TestAcc*Resource shape matters here (CI-shard reachability, not
// style).
func TestAccFrameworkCoreGateErroredDeleteStatePersistsResource(t *testing.T) {
	coreGateDeleteFailureBudget.Store(1)
	config := `
resource "coregate_gate" "test" {
  id          = "gate-2"
  trigger_err = false
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: coreGateProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				Config:      `# resource intentionally removed to trigger Delete`,
				ExpectError: regexp.MustCompile(`gate: delete failed deliberately`),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
