package provider

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure AnyscaleProvider satisfies various provider interfaces.
var _ provider.Provider = &AnyscaleProvider{}

// AnyscaleProvider defines the provider implementation for the Framework.
type AnyscaleProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance testing.
	version string
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
				Description: "The Anyscale API URL. Can also be set via ANYSCALE_API_URL environment variable. Defaults to https://console.anyscale.com",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The Anyscale API token. Can also be set via ANYSCALE_CLI_TOKEN environment variable or read from ~/.anyscale/credentials.json.",
			},
		},
	}
}

// Configure prepares a Anyscale API client for data sources and resources.
func (p *AnyscaleProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config AnyscaleProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Get API URL from config or environment
	apiURL := "https://console.anyscale.com"
	if !config.ApiUrl.IsNull() {
		apiURL = config.ApiUrl.ValueString()
	} else if envURL := os.Getenv("ANYSCALE_API_URL"); envURL != "" {
		apiURL = envURL
	}

	// Get token from config, environment, or credentials file
	var token string
	if !config.Token.IsNull() {
		token = config.Token.ValueString()
	} else if envToken := os.Getenv("ANYSCALE_CLI_TOKEN"); envToken != "" {
		token = envToken
	} else {
		// Try to read from credentials file
		client, err := NewClient(apiURL)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("token"),
				"Unable to Create Anyscale API Client",
				"Unable to read token from environment or credentials file: "+err.Error(),
			)
			return
		}
		token = client.Token
	}

	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing Anyscale API Token",
			"The provider cannot create the Anyscale API client as there is a missing or empty value for the Anyscale API token. "+
				"Set the token value in the configuration or use the ANYSCALE_CLI_TOKEN environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
		return
	}

	// Create the API client
	client := &Client{
		BaseURL: apiURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: time.Second * 30,
		},
	}

	// Make the client available to resources and data sources
	resp.DataSourceData = client
	resp.ResourceData = client
}

// Resources defines the resources implemented in the provider.
func (p *AnyscaleProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewComputeConfigResource,
		NewCloudResourceResource,
		NewCloudResource,
	}
}

// DataSources defines the data sources implemented in the provider.
func (p *AnyscaleProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewCloudDataSource,
		NewComputeConfigDataSource,
	}
}
