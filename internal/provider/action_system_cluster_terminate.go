package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// systemClusterStateTerminated is the target state this action waits for - the terminate
// counterpart to resource_system_cluster.go's systemClusterStateRunning (system_workload_helpers.go).
const systemClusterStateTerminated = "Terminated"

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ action.Action              = &SystemClusterTerminateAction{}
	_ action.ActionWithConfigure = &SystemClusterTerminateAction{}
)

// NewSystemClusterTerminateAction creates a new System Cluster terminate action - this
// provider's first Action, establishing the pattern for future imperative side-effects layered
// on a resource (e.g. anyscale_service's canary promote/rollback, deferred per its own resource
// doc comment) without bending that resource's declarative CRUD lifecycle to carry a verb.
func NewSystemClusterTerminateAction() action.Action {
	return &SystemClusterTerminateAction{}
}

// SystemClusterTerminateAction defines the action implementation.
type SystemClusterTerminateAction struct {
	client *Client
}

// SystemClusterTerminateActionModel describes the action's config data model. Actions have no
// Computed/Sensitive attributes to speak of - action/schema.StringAttribute exposes only
// Required/Optional, since Invoke receives nothing but the practitioner's own config (no prior
// state, no plan cycle) - so this model is deliberately just the one input.
type SystemClusterTerminateActionModel struct {
	CloudID types.String `tfsdk:"cloud_id"`
}

func (a *SystemClusterTerminateAction) Metadata(ctx context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_cluster_terminate"
}

func (a *SystemClusterTerminateAction) Schema(ctx context.Context, req action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Terminates the System Cluster for an Anyscale Cloud - an imperative side effect, not a declarative resource. Requires Terraform 1.14 or later; invoke it standalone via ` + "`terraform apply -invoke=action.anyscale_system_cluster_terminate.<label>`" + ` (or ` + "`plan`" + ` with the same flag), or wire it to a resource via a ` + "`lifecycle.action_trigger`" + ` block. Actions are a comparatively new Terraform Core / Plugin Framework primitive - as of this provider's framework version, the framework's own documentation still describes the Action API as a technical preview, though its shape has had no breaking changes across several framework releases.

~> **Note:** this action does NOT alter ` + "`anyscale_system_cluster`" + `'s Terraform state. Running it terminates the real System Cluster, but Terraform will not refresh or update the ` + "`anyscale_system_cluster`" + ` resource as a result - that resource's own ` + "`state`" + ` attribute will keep showing its last-read value until its own next ` + "`plan`" + `/` + "`apply`" + ` observes the change. This is the same declarative-lifecycle-stays-untouched design already documented on that resource's own ` + "`terraform destroy`" + ` behavior.

Mirrors ` + "`anyscale_system_cluster`" + `'s own existing client wiring and wait-loop conventions (` + "`system_workload_helpers.go`" + `), establishing this provider's first Action - the pattern future imperative operations (e.g. ` + "`anyscale_service`" + `'s deferred canary promote/rollback) are expected to follow.`,
		Attributes: map[string]schema.Attribute{
			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the Anyscale Cloud whose System Cluster to terminate.",
			},
		},
	}
}

func (a *SystemClusterTerminateAction) Configure(ctx context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Action Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	a.client = client
}

// Invoke calls terminateSystemCluster, then waits for the System Cluster to actually reach
// Terminated - reusing describeSystemWorkload/evaluateSystemClusterState directly (the same
// primitives resource_system_cluster.go's wait loop is built on) rather than
// waitForSystemClusterState itself: that helper is deliberately fail-fast on timeout (a
// correctness gate for a resource's Create/Update - see its own doc comment), which is the wrong
// shape here. An Action's terminate-then-wait is fire-and-forget from Terraform's perspective
// once Invoke returns, so a timeout here means "confirmation is inconclusive," not "this failed" -
// it becomes a warning, and progress is reported via SendProgress along the way, neither of which
// waitForSystemClusterState supports.
func (a *SystemClusterTerminateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var config SystemClusterTerminateActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := config.CloudID.ValueString()

	resp.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Terminating System Cluster for cloud %s...", cloudID),
	})

	if err := terminateSystemCluster(ctx, a.client, cloudID); err != nil {
		switch {
		case strings.Contains(err.Error(), "unexpected status 404"):
			resp.Diagnostics.AddError(
				"No System Cluster Exists",
				fmt.Sprintf("Cloud %s has no System Cluster to terminate.", cloudID),
			)
		case strings.Contains(err.Error(), "unexpected status 409"):
			resp.Diagnostics.AddError(
				"System Cluster Already Terminated",
				fmt.Sprintf("Cloud %s's System Cluster is already Terminated.", cloudID),
			)
		default:
			AddAPIError(&resp.Diagnostics, "terminate system cluster", err)
		}
		return
	}

	a.waitForTerminated(ctx, resp, cloudID, defaultSystemClusterCreateTimeout)
}

// waitForTerminated pins the real poll interval; waitForTerminatedWithTiming takes it as a
// parameter so tests can drive the timeout/interrupted paths without paying real wall-clock time.
func (a *SystemClusterTerminateAction) waitForTerminated(ctx context.Context, resp *action.InvokeResponse, cloudID string, timeout time.Duration) {
	a.waitForTerminatedWithTiming(ctx, resp, cloudID, timeout, defaultSystemClusterPollInterval)
}

func (a *SystemClusterTerminateAction) waitForTerminatedWithTiming(ctx context.Context, resp *action.InvokeResponse, cloudID string, timeout, interval time.Duration) {
	deadline := time.Now().Add(timeout)

	for {
		result, err := describeSystemWorkload(ctx, a.client, cloudID, false)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "check system cluster termination status", err)
			return
		}

		status := "unknown"
		if result.Status != nil {
			status = *result.Status
		}
		resp.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Cloud %s's System Cluster is %s...", cloudID, status),
		})

		if done, evalErr := evaluateSystemClusterState(result, systemClusterStateTerminated, cloudID); done {
			if evalErr != nil {
				AddAPIError(&resp.Diagnostics, "wait for system cluster termination", evalErr)
			}
			return
		}

		if time.Now().After(deadline) {
			resp.Diagnostics.AddWarning(
				"Termination Not Yet Confirmed",
				fmt.Sprintf("Cloud %s's System Cluster termination was initiated but had not reached Terminated after %s (currently %s). It may still be terminating in the background - check again later.", cloudID, timeout, status),
			)
			return
		}

		select {
		case <-ctx.Done():
			resp.Diagnostics.AddWarning(
				"Termination Not Yet Confirmed",
				fmt.Sprintf("Cloud %s's System Cluster termination was initiated, but confirmation was interrupted (%s). It may still be terminating in the background - check again later.", cloudID, ctx.Err()),
			)
			return
		case <-time.After(interval):
		}
	}
}
