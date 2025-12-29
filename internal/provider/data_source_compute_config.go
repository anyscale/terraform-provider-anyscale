package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ComputeConfigDataSource{}
	_ datasource.DataSourceWithConfigure = &ComputeConfigDataSource{}
)

// NewComputeConfigDataSource returns a new compute config data source.
func NewComputeConfigDataSource() datasource.DataSource {
	return &ComputeConfigDataSource{}
}

// ComputeConfigDataSource defines the data source implementation.
type ComputeConfigDataSource struct {
	client *Client
}

// ComputeConfigDataSourceModel describes the data source data model.
type ComputeConfigDataSourceModel struct {
	// Input - either ID or Name must be specified
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Cloud identification - can provide either ID or Name as input, also computed as output
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	// Computed outputs (excluding CloudID and CloudName which are above)
	Region                 types.String `tfsdk:"region"`
	IdleTerminationMinutes types.Int64  `tfsdk:"idle_termination_minutes"`
	MaximumUptimeMinutes   types.Int64  `tfsdk:"maximum_uptime_minutes"`
	EnableCrossZoneScaling types.Bool   `tfsdk:"enable_cross_zone_scaling"`
	AutoSelectWorkerConfig types.Bool   `tfsdk:"auto_select_worker_config"`
	Anonymous              types.Bool   `tfsdk:"anonymous"`
	ProjectID              types.String `tfsdk:"project_id"`
	Version                types.Int64  `tfsdk:"version"`
	CreatedAt              types.String `tfsdk:"created_at"`
	LastModifiedAt         types.String `tfsdk:"last_modified_at"`
}

// Metadata returns the data source type name.
func (d *ComputeConfigDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_config"
}

// Schema defines the data source schema.
func (d *ComputeConfigDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve information about an existing Anyscale Compute Configuration. You can look up a compute config by its ID or name.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The unique identifier of the compute config. Either `id` or `name` must be specified.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the compute config. Either `id` or `name` must be specified. This field is computed when looking up by ID.",
			},

			// Computed fields
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The ID of the Anyscale cloud. Can be used to filter compute configs when looking up by name. Either `cloud_id` or `cloud_name` can be specified.",
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the Anyscale cloud. Can be used to filter compute configs when looking up by name. Either `cloud_id` or `cloud_name` can be specified. If provided, will be resolved to cloud_id.",
			},
			"region": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The region to launch clusters in.",
			},
			"idle_termination_minutes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Number of minutes after which idle clusters will be terminated. 0 means disabled.",
			},
			"maximum_uptime_minutes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Maximum uptime in minutes before cluster termination.",
			},
			"enable_cross_zone_scaling": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether instances can run across multiple availability zones.",
			},
			"auto_select_worker_config": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether worker node groups are automatically selected based on workload.",
			},
			"anonymous": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is an anonymous compute config (not shown in UI list).",
			},
			"project_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The project ID this compute config is associated with.",
			},
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The version number of this compute config.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The timestamp when the compute config was created.",
			},
			"last_modified_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The timestamp when the compute config was last modified.",
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *ComputeConfigDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *ComputeConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ComputeConfigDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate that either ID or Name is provided
	if config.ID.IsNull() && config.Name.IsNull() {
		resp.Diagnostics.AddError(
			"Missing Required Attribute",
			"Either 'id' or 'name' must be specified to look up a compute config.",
		)
		return
	}

	var configID string
	var err error

	if !config.ID.IsNull() {
		// Look up by ID
		configID = config.ID.ValueString()
		tflog.Info(ctx, "Looking up compute config by ID", map[string]any{"id": configID})
	} else {
		// Look up by name
		name := config.Name.ValueString()
		cloudID := ""

		// Resolve cloud_id from either cloud_id or cloud_name
		if !config.CloudID.IsNull() {
			cloudID = config.CloudID.ValueString()
		} else if !config.CloudName.IsNull() {
			cloudName := config.CloudName.ValueString()
			tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": cloudName})

			cloudID, err = d.resolveCloudNameToID(ctx, cloudName)
			if err != nil {
				resp.Diagnostics.AddError(
					"Cloud Name Resolution Failed",
					fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
				)
				return
			}
		}

		tflog.Info(ctx, "Looking up compute config by name", map[string]any{"name": name, "cloud_id": cloudID})

		configID, err = d.findComputeConfigByName(ctx, name, cloudID)
		if err != nil {
			resp.Diagnostics.AddError(
				"Compute Config Lookup Failed",
				fmt.Sprintf("Failed to find compute config with name '%s': %s", name, err.Error()),
			)
			return
		}

		if configID == "" {
			resp.Diagnostics.AddError(
				"Compute Config Not Found",
				fmt.Sprintf("No compute config found with name '%s'", name),
			)
			return
		}
	}

	// Fetch compute config details from API
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/compute_templates/%s", configID), nil)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch compute config", map[string]any{"error": err.Error()})
		resp.Diagnostics.AddError("API Request Failed", err.Error())
		return
	}
	defer func() {
		if closeErr := apiResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	if apiResp.StatusCode == http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Compute Config Not Found",
			fmt.Sprintf("Compute config with ID '%s' not found in Anyscale", configID),
		)
		return
	}

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		resp.Diagnostics.AddError("Response Read Error", err.Error())
		return
	}

	if apiResp.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(
			"API Error",
			fmt.Sprintf("Failed to read compute config: %s - %s", apiResp.Status, string(body)),
		)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		resp.Diagnostics.AddError("JSON Unmarshal Error", err.Error())
		return
	}

	// Extract result from response
	resultData, ok := result["result"].(map[string]interface{})
	if !ok {
		resp.Diagnostics.AddError("Invalid Response", "API did not return expected result structure")
		return
	}

	// Populate the data source model
	config.ID = types.StringValue(resultData["id"].(string))

	// Name - handle anonymous configs
	if anonymous, ok := resultData["anonymous"].(bool); ok && anonymous {
		config.Anonymous = types.BoolValue(true)
		config.Name = types.StringNull() // Anonymous configs don't have user-visible names
	} else {
		config.Anonymous = types.BoolValue(false)
		if name, ok := resultData["name"].(string); ok {
			config.Name = types.StringValue(name)
		}
	}

	// Version
	if version, ok := resultData["version"].(float64); ok {
		config.Version = types.Int64Value(int64(version))
	}

	// Timestamps
	if createdAt, ok := resultData["created_at"].(string); ok {
		config.CreatedAt = types.StringValue(createdAt)
	}
	if lastModifiedAt, ok := resultData["last_modified_at"].(string); ok {
		config.LastModifiedAt = types.StringValue(lastModifiedAt)
	}

	// Project ID
	if projectID, ok := resultData["project_id"].(string); ok {
		config.ProjectID = types.StringValue(projectID)
	} else {
		config.ProjectID = types.StringNull()
	}

	// Extract config object
	if configData, ok := resultData["config"].(map[string]interface{}); ok {
		if cloudID, ok := configData["cloud_id"].(string); ok {
			config.CloudID = types.StringValue(cloudID)

			// Fetch cloud name for the cloud_id
			cloudResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
			if err == nil {
				defer func() {
					if closeErr := cloudResp.Body.Close(); closeErr != nil {
						tflog.Warn(ctx, "Failed to close cloud response body", map[string]any{"error": closeErr.Error()})
					}
				}()
				if cloudResp.StatusCode == http.StatusOK {
					cloudBody, err := io.ReadAll(cloudResp.Body)
					if err == nil {
						var cloudResult map[string]interface{}
						if err := json.Unmarshal(cloudBody, &cloudResult); err == nil {
							if cloudData, ok := cloudResult["result"].(map[string]interface{}); ok {
								if cloudName, ok := cloudData["name"].(string); ok {
									config.CloudName = types.StringValue(cloudName)
								}
							}
						}
					}
				}
			}
			// If we can't fetch cloud name, just leave it null - it's not critical
			if config.CloudName.IsNull() {
				tflog.Debug(ctx, "Could not fetch cloud name", map[string]any{"cloud_id": cloudID})
			}
		}

		if region, ok := configData["region"].(string); ok {
			config.Region = types.StringValue(region)
		}

		if idleTermination, ok := configData["idle_termination_minutes"].(float64); ok {
			config.IdleTerminationMinutes = types.Int64Value(int64(idleTermination))
		}

		if maximumUptime, ok := configData["maximum_uptime_minutes"].(float64); ok {
			config.MaximumUptimeMinutes = types.Int64Value(int64(maximumUptime))
		} else {
			config.MaximumUptimeMinutes = types.Int64Null()
		}

		if enableCrossZone, ok := configData["enable_cross_zone_scaling"].(bool); ok {
			config.EnableCrossZoneScaling = types.BoolValue(enableCrossZone)
		} else {
			config.EnableCrossZoneScaling = types.BoolValue(false)
		}

		if autoSelect, ok := configData["auto_select_worker_config"].(bool); ok {
			config.AutoSelectWorkerConfig = types.BoolValue(autoSelect)
		} else {
			config.AutoSelectWorkerConfig = types.BoolValue(false)
		}
	}

	tflog.Info(ctx, "Successfully retrieved compute config", map[string]any{
		"id":   configID,
		"name": config.Name.ValueString(),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findComputeConfigByName looks for a compute config with the given name using the search API
func (d *ComputeConfigDataSource) findComputeConfigByName(ctx context.Context, name string, cloudID string) (string, error) {
	// Use the search API to find compute configs by name
	searchPayload := map[string]interface{}{
		"name": map[string]string{
			"equals": name,
		},
		"include_anonymous": false,
		"archive_status":    "NOT_ARCHIVED",
	}

	// Add cloud_id filter if provided
	if cloudID != "" {
		searchPayload["cloud_id"] = cloudID
	}

	searchBody, err := json.Marshal(searchPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal search request: %w", err)
	}

	resp, err := d.client.DoRequest(ctx, "POST", "/api/v2/compute_templates/search", strings.NewReader(string(searchBody)))
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to search compute configs: %s - %s", resp.Status, string(body))
	}

	var searchResp struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
			Anonymous bool   `json:"anonymous"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", err
	}

	if len(searchResp.Results) == 0 {
		return "", nil // Not found
	}

	// If multiple configs exist with the same name, return the most recently created one
	var matchedConfigID string
	var latestCreatedAt string

	for _, cfg := range searchResp.Results {
		if matchedConfigID == "" || cfg.CreatedAt > latestCreatedAt {
			matchedConfigID = cfg.ID
			latestCreatedAt = cfg.CreatedAt
		}
	}

	if len(searchResp.Results) > 1 {
		// Log warning if multiple configs with same name exist
		tflog.Warn(ctx, "Multiple compute configs found with same name, returning most recent", map[string]any{
			"name":       name,
			"config_id":  matchedConfigID,
			"created_at": latestCreatedAt,
			"count":      len(searchResp.Results),
		})
	}

	tflog.Info(ctx, "Found compute config by name", map[string]any{
		"name":      name,
		"config_id": matchedConfigID,
	})

	return matchedConfigID, nil
}

// resolveCloudNameToID looks up a cloud by name and returns its ID
func (d *ComputeConfigDataSource) resolveCloudNameToID(ctx context.Context, cloudName string) (string, error) {
	resp, err := d.client.DoRequest(ctx, "GET", "/api/v2/clouds", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to list clouds: %s - %s", resp.Status, string(body))
	}

	var cloudsResp struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &cloudsResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Find clouds with matching name
	// If multiple exist, return the most recently created one
	var matchedCloudID string
	var latestCreatedAt string

	for _, cloud := range cloudsResp.Results {
		if cloud.Name == cloudName {
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	if matchedCloudID == "" {
		return "", fmt.Errorf("no cloud found with name '%s'", cloudName)
	}

	if len(cloudsResp.Results) > 1 {
		tflog.Warn(ctx, "Multiple clouds found with same name, using most recent", map[string]any{
			"cloud_name": cloudName,
			"cloud_id":   matchedCloudID,
			"created_at": latestCreatedAt,
		})
	}

	tflog.Info(ctx, "Resolved cloud name to ID", map[string]any{
		"cloud_name": cloudName,
		"cloud_id":   matchedCloudID,
	})

	return matchedCloudID, nil
}
