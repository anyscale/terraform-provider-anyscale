package provider

import (
	"context"
	"fmt"
	"net/http"

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
	ConfigID               types.String `tfsdk:"config_id"`    // Version-specific API ID
	NameVersion            types.String `tfsdk:"name_version"` // Formatted as "name:version" for use with Anyscale APIs
	Versions               types.List   `tfsdk:"versions"`     // List of available version numbers
	Region                 types.String `tfsdk:"region"`
	IdleTerminationMinutes types.Int64  `tfsdk:"idle_termination_minutes"`
	MaximumUptimeMinutes   types.Int64  `tfsdk:"maximum_uptime_minutes"`
	EnableCrossZoneScaling types.Bool   `tfsdk:"enable_cross_zone_scaling"`
	AutoSelectWorkerConfig types.Bool   `tfsdk:"auto_select_worker_config"`
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
			"config_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The version-specific API ID of the compute config. This is the API identifier for the specific version.",
			},
			"name_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The compute config name and version formatted as `name:version` for use with Anyscale APIs.",
			},
			"versions": schema.ListAttribute{
				ElementType:         types.Int64Type,
				Computed:            true,
				MarkdownDescription: "List of all available version numbers for this compute config, sorted in ascending order.",
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
		AddConfigError(&resp.Diagnostics,
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
		AddConfigError(&resp.Diagnostics,
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

			cloudID, err = ResolveCloudNameToID(ctx, d.client, cloudName)
			if err != nil {
				AddConfigError(&resp.Diagnostics,
					"Cloud Name Resolution Failed",
					fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
				)
				return
			}
		}

		tflog.Info(ctx, "Looking up compute config by name", map[string]any{"name": name, "cloud_id": cloudID})

		configID, err = d.findComputeConfigByName(ctx, name, cloudID)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "find compute config", err)
			return
		}

		if configID == "" {
			AddConfigError(&resp.Diagnostics,
				"Compute Config Not Found",
				fmt.Sprintf("No compute config found with name '%s'", name),
			)
			return
		}
	}

	// Fetch compute config details from API
	type ComputeConfigResponse struct {
		Result map[string]interface{} `json:"result"`
	}

	computeResp, err := DoRequestAndParse[ComputeConfigResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_computes/%s", configID),
		nil,
		http.StatusOK,
	)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch compute config", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "fetch compute config", err)
		return
	}

	// Extract result from response
	resultData := computeResp.Result

	// Populate the data source model
	apiID := resultData["id"].(string)
	config.ID = types.StringValue(apiID)
	config.ConfigID = types.StringValue(apiID) // Also set config_id for consistency with resource

	// Set name from API response
	var configName string
	if name, ok := resultData["name"].(string); ok {
		configName = name
		config.Name = types.StringValue(name)
	}

	// Version
	if version, ok := resultData["version"].(float64); ok {
		config.Version = types.Int64Value(int64(version))
		// Set name_version formatted as "name:version" for use with Anyscale APIs
		if configName != "" {
			config.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", configName, int64(version)))
		}
	}

	// Fetch all versions of this compute config by name
	if configName != "" {
		versions, err := d.fetchComputeConfigVersions(ctx, configName)
		if err != nil {
			tflog.Warn(ctx, "Failed to fetch versions list", map[string]any{"error": err.Error()})
			config.Versions = types.ListNull(types.Int64Type)
		} else {
			versionsList, diags := types.ListValueFrom(ctx, types.Int64Type, versions)
			resp.Diagnostics.Append(diags...)
			config.Versions = versionsList
		}
	} else {
		config.Versions = types.ListNull(types.Int64Type)
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
			type CloudResponse struct {
				Result map[string]interface{} `json:"result"`
			}

			cloudResp, err := DoRequestAndParse[CloudResponse](
				ctx,
				d.client,
				"GET",
				fmt.Sprintf("/api/v2/clouds/%s", cloudID),
				nil,
				http.StatusOK,
			)
			if err == nil {
				if cloudName, ok := cloudResp.Result["name"].(string); ok {
					config.CloudName = types.StringValue(cloudName)
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
	}

	// Add cloud_id filter if provided
	if cloudID != "" {
		searchPayload["cloud_id"] = cloudID
	}

	searchBody, err := MarshalRequestBody(searchPayload)
	if err != nil {
		return "", err
	}

	type SearchResponse struct {
		Results []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
			Anonymous bool   `json:"anonymous"`
		} `json:"results"`
	}

	searchResp, err := DoRequestAndParse[SearchResponse](
		ctx,
		d.client,
		"POST",
		"/ext/v0/cluster_computes/search",
		searchBody,
		http.StatusOK,
	)
	if err != nil {
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

	WarnIfMultipleMatches(ctx, "compute config", name, len(searchResp.Results), matchedConfigID)

	tflog.Info(ctx, "Found compute config by name", map[string]any{
		"name":      name,
		"config_id": matchedConfigID,
	})

	return matchedConfigID, nil
}

// fetchComputeConfigVersions retrieves all version numbers for a compute config by name
func (d *ComputeConfigDataSource) fetchComputeConfigVersions(ctx context.Context, name string) ([]int64, error) {
	// Use the search API to find all versions of this compute config
	searchPayload := map[string]interface{}{
		"name": map[string]string{
			"equals": name,
		},
		"include_anonymous": false,
	}

	searchBody, err := MarshalRequestBody(searchPayload)
	if err != nil {
		return nil, err
	}

	type SearchResponse struct {
		Results []struct {
			ID      string  `json:"id"`
			Name    string  `json:"name"`
			Version float64 `json:"version"`
		} `json:"results"`
	}

	searchResp, err := DoRequestAndParse[SearchResponse](
		ctx,
		d.client,
		"POST",
		"/ext/v0/cluster_computes/search",
		searchBody,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}

	// Collect unique version numbers and sort them
	versionSet := make(map[int64]bool)
	for _, cfg := range searchResp.Results {
		versionSet[int64(cfg.Version)] = true
	}

	versions := make([]int64, 0, len(versionSet))
	for v := range versionSet {
		versions = append(versions, v)
	}

	// Sort versions in ascending order
	for i := 0; i < len(versions)-1; i++ {
		for j := i + 1; j < len(versions); j++ {
			if versions[i] > versions[j] {
				versions[i], versions[j] = versions[j], versions[i]
			}
		}
	}

	tflog.Debug(ctx, "Found compute config versions", map[string]any{
		"name":     name,
		"versions": versions,
	})

	return versions, nil
}
