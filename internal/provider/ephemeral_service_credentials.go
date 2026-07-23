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
	_ ephemeral.EphemeralResource              = &ServiceCredentialsEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &ServiceCredentialsEphemeralResource{}
)

// NewServiceCredentialsEphemeralResource creates a new Service credentials ephemeral resource,
// mirroring anyscale_system_cluster_credentials' pattern (ephemeral_system_cluster_credentials.go,
// this provider's first ephemeral resource).
func NewServiceCredentialsEphemeralResource() ephemeral.EphemeralResource {
	return &ServiceCredentialsEphemeralResource{}
}

// ServiceCredentialsEphemeralResource defines the ephemeral resource implementation.
type ServiceCredentialsEphemeralResource struct {
	client *Client
}

// ServiceCredentialsEphemeralResourceModel describes the ephemeral resource data model.
type ServiceCredentialsEphemeralResourceModel struct {
	ServiceID          types.String `tfsdk:"service_id"`
	AuthToken          types.String `tfsdk:"auth_token"`
	SecondaryAuthToken types.String `tfsdk:"secondary_auth_token"`
	BaseURL            types.String `tfsdk:"base_url"`
}

func (e *ServiceCredentialsEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_credentials"
}

func (e *ServiceCredentialsEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Fetches live authentication credentials for a running ` + "`anyscale_service`" + ` without ever writing them to Terraform state or plan output - the defining property of an ephemeral resource. This is different from a ` + "`Sensitive`" + ` attribute on a regular resource or data source, which is still persisted to state in plaintext regardless of the ` + "`Sensitive`" + ` marking; use this ephemeral resource instead whenever the value must never land in state at all. Requires Terraform 1.10 or later - ephemeral resources are a Terraform Core / Plugin Framework primitive with no earlier-version fallback.

Every read re-fetches fresh: there is no caching, renewal, or automatic refresh between separate reads (this resource implements Open only, with no Renew or Close). ` + "`auth_token`" + ` and ` + "`secondary_auth_token`" + ` are ` + "`null`" + ` whenever the service does not have bearer authentication enabled - a service-level configuration choice, not a lifecycle state. Unlike ` + "`anyscale_system_cluster_credentials`" + `, this resource does not gate on or independently track any state of its own; a null value here simply reflects what the API itself returns.`,
		Attributes: map[string]schema.Attribute{
			"service_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the `anyscale_service` whose credentials to fetch.",
			},
			"auth_token": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The primary bearer token for authenticating to this service - send it as an `Authorization: Bearer <token>` header. Never written to Terraform state or plan output - fetch it fresh via this ephemeral resource immediately before use rather than storing it anywhere yourself. `null` if the service does not have bearer authentication enabled.",
			},
			"secondary_auth_token": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "A secondary bearer token, present alongside `auth_token` only while a token rotation is in progress - authenticate with it the same way, as an `Authorization: Bearer <token>` header. `null` outside of an active rotation.",
			},
			"base_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The service's base URL, echoed here as a convenience so the endpoint and its credentials can be fetched together without a separate `anyscale_service` data source lookup. Matches the same-named attribute on that data source.",
			},
		},
	}
}

func (e *ServiceCredentialsEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
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

// Open fetches the service via the existing getServiceByID helper (shared with
// resource_service.go's wait loop and Create/Read/Update/Delete - see service_helpers.go) and
// surfaces auth_token/secondary_auth_token directly from the wire response.
//
// Deliberate asymmetry from the system_cluster ephemeral resource: an unknown service_id IS a real
// error here (a service GET 404 is keyed by the service's own id - a genuine not-found, unlike a
// cloud with no System Cluster, which is an expected empty state), and bearer-enabled is not a
// condition this resource tracks or gates on independently - a null token is whatever the API
// returns, passed through with no added diagnostic.
func (e *ServiceCredentialsEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var config ServiceCredentialsEphemeralResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	service, err := getServiceByID(ctx, e.client, config.ServiceID.ValueString())
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read service", err)
		return
	}

	config.AuthToken = types.StringPointerValue(service.AuthToken)
	config.SecondaryAuthToken = types.StringPointerValue(service.SecondaryAuthToken)
	config.BaseURL = types.StringValue(service.BaseURL)

	resp.Diagnostics.Append(resp.Result.Set(ctx, &config)...)
}
