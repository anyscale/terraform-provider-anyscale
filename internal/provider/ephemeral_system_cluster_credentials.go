package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ ephemeral.EphemeralResource              = &SystemClusterCredentialsEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &SystemClusterCredentialsEphemeralResource{}
)

// NewSystemClusterCredentialsEphemeralResource creates a new System Cluster credentials
// ephemeral resource - this provider's first ephemeral resource, establishing the pattern
// anyscale_service_credentials (ephemeral_service_credentials.go) mirrors.
func NewSystemClusterCredentialsEphemeralResource() ephemeral.EphemeralResource {
	return &SystemClusterCredentialsEphemeralResource{}
}

// SystemClusterCredentialsEphemeralResource defines the ephemeral resource implementation.
type SystemClusterCredentialsEphemeralResource struct {
	client *Client
}

// SystemClusterCredentialsEphemeralResourceModel describes the ephemeral resource data model.
type SystemClusterCredentialsEphemeralResourceModel struct {
	CloudID                types.String `tfsdk:"cloud_id"`
	WorkloadServiceURL     types.String `tfsdk:"workload_service_url"`
	WorkloadServiceURLAuth types.String `tfsdk:"workload_service_url_auth"`
}

func (e *SystemClusterCredentialsEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_cluster_credentials"
}

func (e *SystemClusterCredentialsEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Fetches live System Cluster credentials for a cloud without ever writing them to Terraform state or plan output - the defining property of an ephemeral resource. This is different from a ` + "`Sensitive`" + ` attribute on a regular resource or data source, which is still persisted to state in plaintext regardless of the ` + "`Sensitive`" + ` marking; use this ephemeral resource instead whenever the value must never land in state at all. Requires Terraform 1.10 or later - ephemeral resources are a Terraform Core / Plugin Framework primitive with no earlier-version fallback.

Every read re-fetches fresh: there is no caching, renewal, or automatic refresh between separate reads (this resource implements Open only, with no Renew or Close). ` + "`workload_service_url_auth`" + ` is only non-null while the System Cluster is currently ` + "`Running`" + ` and its live URL and token are available. Any other null case - including no System Cluster existing yet, one that has not reached ` + "`Running`" + `, or the rare case where ` + "`Running`" + ` has been reached but the credential is not yet materialized - is always accompanied by a warning diagnostic explaining why.

Mirrors the ` + "`anyscale_system_cluster`" + ` resource and data source's own existence-check-first behavior: reading this ephemeral resource never creates, starts, or otherwise provisions a System Cluster as a side effect, regardless of whether one exists yet.`,
		Attributes: map[string]schema.Attribute{
			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the Anyscale Cloud whose System Cluster credentials to fetch.",
			},
			"workload_service_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The URL the task and actor observability dashboards use to reach this System Cluster's workload service, or `null` if the cloud has no System Cluster yet. Matches the same-named attribute on the `anyscale_system_cluster` resource and data source.",
			},
			"workload_service_url_auth": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The bearer credential for authenticating to `workload_service_url`, shaped `{workload_service_url}/auth/?token={token}`. Never written to Terraform state or plan output - fetch it fresh via this ephemeral resource immediately before use rather than storing it anywhere yourself. `null` unless the System Cluster is currently `Running`; a null value here is always accompanied by a warning diagnostic explaining why, since no state is left behind afterward for you to inspect.",
			},
		},
	}
}

func (e *SystemClusterCredentialsEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Ephemeral Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	e.client = client
}

// Open mirrors data_source_system_cluster.go's existence-check-first Read: findSystemWorkloadCluster
// FIRST, describeSystemWorkload only once that confirms a cluster exists. This ephemeral resource
// must never trigger describeSystemWorkload's create-on-read side effect (see
// system_workload_helpers.go's package doc comment) - a naive "just describe it" Open would silently
// provision a real System Cluster during what a user thinks is a side-effect-free credentials read.
func (e *SystemClusterCredentialsEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var config SystemClusterCredentialsEphemeralResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := config.CloudID.ValueString()

	found, err := findSystemWorkloadCluster(ctx, e.client, cloudID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "check for system cluster", err)
		return
	}

	// reason drives the single warning below - "no System Cluster exists yet" and "exists but not
	// Running" are one underlying condition (workload_service_url_auth is null) reached two
	// different ways, not two different diagnostics, so both funnel through the same check. Each
	// branch phrases its own reason so the rendered sentence reads naturally, rather than splicing
	// a "no cluster exists" clause into a "currently Running (X)" template built for a status enum
	// value.
	//
	// The warning below is gated on the ACTUAL resulting value (WorkloadServiceURLAuth.IsNull()),
	// not on state - the schema promises a null is always explained, and state==Running is this
	// provider's understanding of when auth is populated, not a guarantee the wire response can't
	// violate (e.g. a narrow window where status has flipped to Running but the url/token have not
	// yet materialized). Gating on the real value keeps the doc promise true regardless.
	var reason string
	if found == nil {
		reason = "no System Cluster exists yet for this cloud"
		config.WorkloadServiceURL = types.StringNull()
		config.WorkloadServiceURLAuth = types.StringNull()
	} else {
		described, err := describeSystemWorkload(ctx, e.client, cloudID, false)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "read system cluster", err)
			return
		}

		config.WorkloadServiceURL = types.StringPointerValue(described.WorkloadServiceURL)
		config.WorkloadServiceURLAuth = types.StringPointerValue(described.WorkloadServiceURLAuth)

		state := "unknown"
		if described.Status != nil {
			state = *described.Status
		}
		if state == systemClusterStateRunning {
			reason = "the System Cluster is currently Running, but a live credential is not yet available"
		} else {
			reason = fmt.Sprintf("the System Cluster is currently %s, not Running", state)
		}
	}

	if config.WorkloadServiceURLAuth.IsNull() {
		resp.Diagnostics.AddWarning(
			"workload_service_url_auth Is Null",
			fmt.Sprintf("Cloud %s has no live workload_service_url_auth available: %s.", cloudID, reason),
		)
	}

	resp.Diagnostics.Append(resp.Result.Set(ctx, &config)...)
}
