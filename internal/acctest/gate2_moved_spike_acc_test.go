package acctest

// GATE-2 MECHANISM SPIKE (assayer, IAM org-rename quest) — deliberately NOT
// TestAcc-prefixed so no CI acctest shard ever runs it.
//
// Question: does resource.Test (terraform-plugin-testing v1.16.0) with
// per-step ProtoV5ProviderFactories really re-serve provider schemas per step,
// permitting step 1 to create state under a resource type the step-2 provider
// does NOT register, and step 2 to move that state across types with a moved{}
// block (Core sends MoveResourceState to the target provider)?
//
// Name-agnostic by design: the scratch types anyscale_spike_old/_new stand in
// for the pre/post-rename pair. If this fails at runtime, the Gate-2 test in
// the org-rename PR cannot use per-step factories and the party falls back to
// the documented weaker shape. TF_ACC-gated like every resource.Test.

import (
	"context"

	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	provschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func TestGate2MovedSpike(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC must be set for resource.Test")
	}

	// Captured by the mover so the test can assert what Core actually sent.
	witness := &spikeMoveWitness{}

	// NewProtocol5WithError IS a factory function: map it directly.
	newSpikeServer := func(oldOnly bool) func() (tfprotov5.ProviderServer, error) {
		return providerserver.NewProtocol5WithError(&spikeProvider{oldOnly: oldOnly, witness: witness})
	}

	tfresource.UnitTest(t, tfresource.TestCase{
		// No factories at TestCase level: every step declares its own
		// (plugin-testing v1.16.0 constraint, testing.go:807-831).
		Steps: []tfresource.TestStep{
			{
				ProtoV5ProviderFactories: map[string]func() (tfprotov5.ProviderServer, error){
					"anyscale": newSpikeServer(true),
				},
				Config: `resource "anyscale_spike_old" "thing" { name = "probe" }`,
			},
			{
				ProtoV5ProviderFactories: map[string]func() (tfprotov5.ProviderServer, error){
					"anyscale": newSpikeServer(false),
				},
				Config: `
resource "anyscale_spike_new" "thing" { name = "probe" }

moved {
  from = anyscale_spike_old.thing
  to   = anyscale_spike_new.thing
}
`,
				// The whole point: the step-2 provider schema lacks the old
				// type, so a no-op plan here proves Core accepted the move via
				// MoveResourceState against unregistered source type.
				ConfigPlanChecks: tfresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_spike_new.thing", plancheck.ResourceActionNoop),
					},
				},
			},
			{
				ProtoV5ProviderFactories: map[string]func() (tfprotov5.ProviderServer, error){
					"anyscale": newSpikeServer(false),
				},
				Config: `
resource "anyscale_spike_new" "thing" { name = "probe" }

moved {
  from = anyscale_spike_old.thing
  to   = anyscale_spike_new.thing
}
`,
				ConfigPlanChecks: tfresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	witness.mu.Lock()
	defer witness.mu.Unlock()
	switch {
	case witness.sourceTypeName == "":
		t.Fatal("mover never ran: Core sent no MoveResourceState request (or the daemon served a cached schema)")
	case witness.sourceTypeName != "anyscale_spike_old":
		t.Fatalf("mover saw source type %q, want anyscale_spike_old", witness.sourceTypeName)
	case witness.schemaVersion != 0:
		t.Fatalf("mover saw source schema version %d, want 0", witness.schemaVersion)
	}
	t.Logf("witnessed MoveResourceState: source type %q, provider address %q, schema version %d",
		witness.sourceTypeName, witness.sourceAddress, witness.schemaVersion)
}

// spikeMoveWitness records what Core actually sent to the mover.
type spikeMoveWitness struct {
	mu             sync.Mutex
	sourceTypeName string
	sourceAddress  string
	schemaVersion  int64
}

// spikeProvider registers exactly one resource: the old (pre-rename) type
// when oldOnly, the new (post-rename) type otherwise. Neither registration
// knows the other.
type spikeProvider struct {
	oldOnly bool
	witness *spikeMoveWitness
}

func (p *spikeProvider) Metadata(ctx context.Context, req fwprovider.MetadataRequest, resp *fwprovider.MetadataResponse) {
	resp.TypeName = "anyscale"
}

func (p *spikeProvider) Schema(ctx context.Context, req fwprovider.SchemaRequest, resp *fwprovider.SchemaResponse) {
	resp.Schema = provschema.Schema{}
}

func (p *spikeProvider) Configure(ctx context.Context, req fwprovider.ConfigureRequest, resp *fwprovider.ConfigureResponse) {
}

func (p *spikeProvider) Resources(ctx context.Context) []func() resource.Resource {
	name := "anyscale_spike_new"
	if p.oldOnly {
		name = "anyscale_spike_old"
	}
	return []func() resource.Resource{
		func() resource.Resource {
			return &spikeResource{
				typeName: name,
				witness:  p.witness,
			}
		},
	}
}

func (p *spikeProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}

type spikeResourceModel struct {
	Id   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

// spikeResource is a state-only resource: CRUD never calls an API, so the
// spike isolates the moved{}/MoveResourceState machinery. The new-type
// registration implements ResourceWithMoveState accepting the old type.
type spikeResource struct {
	typeName string
	witness  *spikeMoveWitness
}

func (r *spikeResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = r.typeName
}

func spikeSchema() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true},
			"name": schema.StringAttribute{Required: true},
		},
	}
}

func (r *spikeResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = spikeSchema()
}

func (r *spikeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m spikeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.Id = types.StringValue("spike-id")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *spikeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

func (r *spikeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m spikeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *spikeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State.RemoveResource(ctx)
}

// MoveState implements resource.ResourceWithMoveState: the new-type resource
// accepts state from the old type, mirroring the shape forge plans for the
// real resources.
func (r *spikeResource) MoveState(ctx context.Context) []resource.StateMover {
	if r.typeName != "anyscale_spike_new" {
		return nil
	}
	stateSchema := spikeSchema()
	// NB: framework StateMover has NO SourceTypeName/SourceProviderRegexp
	// struct fields — every mover runs for every request and must gate on
	// req.SourceTypeName / req.SourceProviderAddress inside StateMove.
	return []resource.StateMover{
		{
			SourceSchema: &stateSchema,
			StateMover: func(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
				if req.SourceTypeName != "anyscale_spike_old" {
					return
				}
				if r.witness != nil {
					r.witness.mu.Lock()
					r.witness.sourceTypeName = req.SourceTypeName
					r.witness.sourceAddress = req.SourceProviderAddress
					r.witness.schemaVersion = req.SourceSchemaVersion
					r.witness.mu.Unlock()
				}
				if req.SourceState == nil {
					// No SourceSchema-decoded state: fall back to raw.
					if req.SourceRawState == nil {
						return
					}
					// Copy raw-source decoded into target schema is more
					// machinery than the spike needs — a mover that skips is
					// recorded by the witness and asserted at test end.
					return
				}
				resp.TargetState = *req.SourceState
			},
		},
	}
}
