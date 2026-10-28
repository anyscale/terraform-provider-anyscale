package provider

import (
	"context"
	"errors"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure AnyscaleProvider satisfies various provider interfaces.
var (
	_ provider.Provider                       = &AnyscaleProvider{}
	_ provider.ProviderWithEphemeralResources = &AnyscaleProvider{}
)

// AnyscaleProvider defines the provider implementation for the Framework.
type AnyscaleProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance testing.
	version string

	// azureOverride, when non-nil, replaces the Azure control-plane classification of
	// the API URL. Tests only: mock servers listen on 127.0.0.1.
	azureOverride *bool
}

// AnyscaleProviderModel describes the provider data model.
type AnyscaleProviderModel struct {
	ApiUrl types.String `tfsdk:"api_url"`
	Token  types.String `tfsdk:"token"`
}

// NewFramework returns a new Framework provider instance.
func NewFramework(version string) func() provider.Provider {
	return func() provider.Provider {
		return &AnyscaleProvider{
			version: version,
		}
	}
}

// NewFrameworkWithAzureClassification returns a provider instance that treats its API URL
// as an Azure control plane (azure = true) or not, whatever the URL. For acceptance tests
// against mock servers; production code uses NewFramework.
func NewFrameworkWithAzureClassification(version string, azure bool) func() provider.Provider {
	return func() provider.Provider {
		return &AnyscaleProvider{version: version, azureOverride: &azure}
	}
}

// Metadata returns the provider type name.
func (p *AnyscaleProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "anyscale"
	resp.Version = p.version
}

// Schema defines the provider-level schema for configuration data.
func (p *AnyscaleProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Anyscale provider is used to interact with Anyscale resources.",
		Attributes: map[string]schema.Attribute{
			"api_url": schema.StringAttribute{
				Optional:    true,
				Description: "The Anyscale API URL. Can also be set via ANYSCALE_API_URL, ANYSCALE_API_HOST, or ANYSCALE_HOST environment variables (checked in that order). Defaults to https://console.anyscale.com. Must be known at plan time.",
			},
			"token": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "The Anyscale API token. Falls back to the ANYSCALE_CLI_TOKEN environment variable, then ~/.anyscale/credentials.json; an empty value counts as unset. Must be known at plan time. See the [Anyscale API keys documentation](https://docs.anyscale.com/auth/api-keys) for how to generate one.",
			},
		},
	}
}

// addMissingCredentialsError reports why no token could be resolved. A
// credentials file that is absent means nothing was supplied, so the message
// lists every source tried; a file that exists but cannot be used is a
// different problem and gets its own summary.
func addMissingCredentialsError(diags *diag.Diagnostics, err error) {
	credentialsPath, _ := credentialsFilePath()
	if errors.Is(err, errCredentialsFileNotFound) {
		diags.AddAttributeError(
			path.Root("token"),
			"No Anyscale API Token Found",
			"The provider looked for an API token in these places, in order, and found none:\n"+
				"  1. the `token` argument of the provider block (unset or empty)\n"+
				"  2. the ANYSCALE_CLI_TOKEN environment variable (unset or empty)\n"+
				"  3. the credentials file "+credentialsPath+" (does not exist)\n"+
				"Set one of them, or run `anyscale login` to create the credentials file. "+
				"See https://docs.anyscale.com/auth/api-keys to generate an API key.",
		)
		return
	}
	diags.AddAttributeError(
		path.Root("token"),
		"Invalid Anyscale Credentials File",
		"No token was given in the `token` argument or ANYSCALE_CLI_TOKEN, and the credentials file "+credentialsPath+
			" could not be used: "+err.Error()+". Fix or remove the file, set ANYSCALE_CLI_TOKEN, or run `anyscale login` again.",
	)
}

// Configure prepares a Anyscale API client for data sources and resources.
func (p *AnyscaleProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config AnyscaleProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// A value that is not known until apply must not fall through to the
	// environment or the default host: the provider would silently talk to a
	// different control plane (or fail with a misleading "missing token").
	if config.ApiUrl.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_url"),
			"Unknown Anyscale API URL",
			"The provider cannot be configured because `api_url` is not known until apply. "+
				"Set it to a value known at plan time, or leave it out and set the ANYSCALE_API_URL environment variable instead.",
		)
	}
	if config.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Unknown Anyscale API Token",
			"The provider cannot be configured because `token` is not known until apply. "+
				"Set it to a value known at plan time, or leave it out and set the ANYSCALE_CLI_TOKEN environment variable "+
				"(or run `anyscale login`) instead.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	apiURL, apiURLSource := resolveAPIURL(config.ApiUrl.ValueString())

	// Get token from config, environment, or credentials file. An empty
	// `token = ""` counts as unset, like api_url, so a module that passes
	// through an empty variable falls back to the other sources.
	var token, tokenSource string
	if config.Token.ValueString() != "" {
		token = config.Token.ValueString()
		tokenSource = "provider configuration"
	} else {
		var err error
		token, tokenSource, err = resolveAuthToken(ctx)
		if err != nil {
			addMissingCredentialsError(&resp.Diagnostics, err)
			return
		}
	}

	// Log which source won, never the token itself: a run that authenticates as
	// an unexpected identity is otherwise impossible to explain from TF_LOG=debug.
	tflog.Debug(ctx, "resolved Anyscale API credentials", map[string]any{
		"token_source":   tokenSource,
		"api_url":        apiURL,
		"api_url_source": apiURLSource,
	})

	// Delegate to NewClientWithToken rather than a third hand-rolled Client
	// literal, so its HTTPClient construction (no blanket Timeout - DoRequest
	// applies a default per-call deadline via context instead) has one
	// definition, not three.
	client := NewClientWithToken(apiURL, token)
	client.azureControlPlaneHost = azureControlPlaneHost(apiURL)
	if p.azureOverride != nil {
		client.azureControlPlaneHost = ""
		if *p.azureOverride {
			client.azureControlPlaneHost = "test-azure-override"
		}
	}

	// Make the client available to resources, data sources, and ephemeral resources
	resp.DataSourceData = client
	resp.ResourceData = client
	resp.EphemeralResourceData = client
}

// Resources defines the resources implemented in the provider.
func (p *AnyscaleProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewComputeConfigResource,
		NewCloudResourceResource,
		NewCloudResource,
		// anyscale_cloud_access is authoritative from the first apply: Create,
		// Update and Delete manage real cloud access, and any member not declared
		// is revoked. ModifyPlan discloses that revoke on create by naming who
		// would lose access, since Read has never shown them any other way. See
		// docs/decisions/rbac-surface-consolidation/README.md for the design.
		NewCloudAccessResource,
		NewCloudIAMMappingResource,
		NewProjectResource,
		NewOrganizationInvitationResource,
		NewOrganizationUserResource,
		NewOrganizationUserRoleResource,
		NewOrganizationDefaultCloudResource,
		NewContainerImageBuildResource,
		NewContainerImageRegistryResource,
		NewServiceResource,
		NewSchedulerConfigResource,
		NewSystemClusterResource,
		NewUserGroupResource,
		NewUserGroupMembersResource,
		NewRoleBindingResource,
		NewRuntimeEnvPackageResource,
	}
}

// EphemeralResources defines the ephemeral resources implemented in the provider. This is the
// provider's first use of the primitive - see ephemeral_service_credentials.go's doc comment for
// the pattern.
func (p *AnyscaleProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewServiceCredentialsEphemeralResource,
	}
}

// DataSources defines the data sources implemented in the provider.
func (p *AnyscaleProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewCloudDataSource,
		NewCloudIAMMappingDataSource,
		NewCloudsDataSource,
		NewComputeConfigDataSource,
		NewContainerImageDataSource,
		NewContainerImagesDataSource,
		NewOrganizationDataSource,
		NewOrganizationUserDataSource,
		NewOrganizationUsersDataSource,
		NewProjectDataSource,
		NewProjectsDataSource,
		NewSchedulerConfigDataSource,
		NewServiceDataSource,
		NewServicesDataSource,
		NewSystemClusterDataSource,
		NewUserDataSource,
		NewUserGroupDataSource,
		NewUserGroupsDataSource,
		NewRoleDataSource,
	}
}

// resolveAPIURL returns the API URL and where it came from: the provider's api_url
// argument, else the first non-empty of ANYSCALE_API_URL, ANYSCALE_API_HOST and
// ANYSCALE_HOST, else the default. ANYSCALE_HOST is the variable the Anyscale CLI reads;
// the other two are provider-specific aliases.
func resolveAPIURL(configured string) (apiURL, source string) {
	if configured != "" {
		return configured, "provider configuration"
	}
	for _, envVar := range []string{"ANYSCALE_API_URL", "ANYSCALE_API_HOST", "ANYSCALE_HOST"} {
		if envURL := os.Getenv(envVar); envURL != "" {
			return envURL, envVar
		}
	}
	return "https://console.anyscale.com", "default"
}
