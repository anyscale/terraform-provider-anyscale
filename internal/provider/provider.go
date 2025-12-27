package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// New returns the Terraform provider schema
func New() *schema.Provider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			"api_url": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("ANYSCALE_API_URL", "https://console.anyscale.com"),
				Description: "The Anyscale API URL. Can also be set via ANYSCALE_API_URL environment variable.",
			},
			"token": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("ANYSCALE_CLI_TOKEN", ""),
				Description: "The Anyscale API token. Can also be set via ANYSCALE_CLI_TOKEN environment variable or read from ~/.anyscale/credentials.json.",
			},
		},
		ResourcesMap: map[string]*schema.Resource{
			"anyscale_cloud":          ResourceCloud(),
			"anyscale_cloud_resource": ResourceCloudResource(),
			"anyscale_compute_config": ResourceComputeConfig(),
		},
		DataSourcesMap: map[string]*schema.Resource{
			// Data sources will be added here
		},
		ConfigureContextFunc: configureProvider,
	}
}

// configureProvider initializes the Anyscale API client
func configureProvider(ctx context.Context, d *schema.ResourceData) (any, diag.Diagnostics) {
	var diags diag.Diagnostics

	apiURL := d.Get("api_url").(string)
	token := d.Get("token").(string)

	// If token is provided in config, use it directly
	// Otherwise, NewClient will handle reading from env or credentials file
	var client *Client
	var err error

	if token != "" {
		// Token provided explicitly in configuration
		client = &Client{
			BaseURL: apiURL,
			Token:   token,
			HTTPClient: &http.Client{
				Timeout: time.Second * 30,
			},
		}
	} else {
		// Try to get token from env or credentials file
		client, err = NewClient(apiURL)
		if err != nil {
			return nil, diag.FromErr(err)
		}
	}

	return client, diags
}
