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
	_ datasource.DataSource              = &CloudDataSource{}
	_ datasource.DataSourceWithConfigure = &CloudDataSource{}
)

// NewCloudDataSource returns a new cloud data source.
func NewCloudDataSource() datasource.DataSource {
	return &CloudDataSource{}
}

// CloudDataSource defines the data source implementation.
type CloudDataSource struct {
	client *Client
}

// CloudDataSourceModel describes the data source data model.
type CloudDataSourceModel struct {
	// Input - either ID or Name must be specified
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Computed outputs
	CloudProvider         types.String `tfsdk:"cloud_provider"`
	Region                types.String `tfsdk:"region"`
	Status                types.String `tfsdk:"status"`
	State                 types.String `tfsdk:"state"`
	IsEmptyCloud          types.Bool   `tfsdk:"is_empty_cloud"`
	CloudDeploymentID     types.String `tfsdk:"cloud_deployment_id"`
	AutoAddUser           types.Bool   `tfsdk:"auto_add_user"`
	EnableLineageTracking types.Bool   `tfsdk:"enable_lineage_tracking"`
	EnableLogIngestion    types.Bool   `tfsdk:"enable_log_ingestion"`
}

// Metadata returns the data source type name.
func (d *CloudDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud"
}

// Schema defines the data source schema.
func (d *CloudDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve information about an existing Anyscale Cloud. You can look up a cloud by its ID or name.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The unique identifier of the cloud. Either `id` or `name` must be specified.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the cloud. Either `id` or `name` must be specified. If multiple clouds have the same name, the most recently created one will be returned.",
			},

			// Computed fields
			"cloud_provider": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The cloud provider (AWS, GCP, AZURE, or GENERIC).",
			},
			"region": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The region where the cloud is deployed.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The operational status of the cloud (e.g., ready, pending, failed).",
			},
			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The lifecycle state of the cloud (e.g., ACTIVE, CREATING, FAILED).",
			},
			"is_empty_cloud": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is an empty cloud (created without embedded resource configuration).",
			},
			"cloud_deployment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The cloud deployment ID. For K8S clouds, this is passed to the Anyscale operator during installation.",
			},
			"auto_add_user": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether users are automatically added to this cloud.",
			},
			"enable_lineage_tracking": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether lineage tracking is enabled for this cloud.",
			},
			"enable_log_ingestion": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether aggregated log ingestion is enabled for this cloud.",
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *CloudDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *CloudDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config CloudDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate that either ID or Name is provided
	if config.ID.IsNull() && config.Name.IsNull() {
		resp.Diagnostics.AddError(
			"Missing Required Attribute",
			"Either 'id' or 'name' must be specified to look up a cloud.",
		)
		return
	}

	var cloudID string
	var err error

	if !config.ID.IsNull() {
		// Look up by ID
		cloudID = config.ID.ValueString()
		tflog.Info(ctx, "Looking up cloud by ID", map[string]any{"id": cloudID})
	} else {
		// Look up by name
		name := config.Name.ValueString()
		tflog.Info(ctx, "Looking up cloud by name", map[string]any{"name": name})

		cloudID, err = d.findCloudByName(ctx, name)
		if err != nil {
			resp.Diagnostics.AddError(
				"Cloud Lookup Failed",
				fmt.Sprintf("Failed to find cloud with name '%s': %s", name, err.Error()),
			)
			return
		}

		if cloudID == "" {
			resp.Diagnostics.AddError(
				"Cloud Not Found",
				fmt.Sprintf("No cloud found with name '%s'", name),
			)
			return
		}
	}

	if err := d.readCloudIntoModel(ctx, cloudID, &config); err != nil {
		if strings.Contains(err.Error(), "not found") {
			resp.Diagnostics.AddError(
				"Cloud Not Found",
				fmt.Sprintf("Cloud with ID '%s' not found in Anyscale", cloudID),
			)
			return
		}
		AddAPIError(&resp.Diagnostics, "read cloud", err)
		return
	}

	tflog.Info(ctx, "Successfully retrieved cloud", map[string]any{
		"id":       cloudID,
		"name":     config.Name.ValueString(),
		"provider": config.CloudProvider.ValueString(),
		"region":   config.Region.ValueString(),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// readCloudIntoModel fetches a cloud by ID and populates every computed field
// on the model from the live API response - no field is left at a hardcoded
// placeholder. is_empty_cloud and cloud_deployment_id require a second call
// (the cloud payload itself doesn't carry them) using the same resource-listing
// semantics anyscale_cloud_resource relies on.
func (d *CloudDataSource) readCloudIntoModel(ctx context.Context, cloudID string, config *CloudDataSourceModel) error {
	apiResp, err := d.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer CloseBody(ctx, apiResp.Body)

	if apiResp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("cloud not found")
	}

	body, err := io.ReadAll(apiResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if apiResp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to read cloud: %s - %s", apiResp.Status, string(body))
	}

	var cloudResp CloudResponse
	if err := json.Unmarshal(body, &cloudResp); err != nil {
		return fmt.Errorf("failed to parse JSON response: %w", err)
	}

	config.ID = types.StringValue(cloudResp.Result.ID)
	config.Name = types.StringValue(cloudResp.Result.Name)
	config.CloudProvider = types.StringValue(cloudResp.Result.Provider)
	config.Region = types.StringValue(cloudResp.Result.Region)

	if cloudResp.Result.Status != "" {
		config.Status = types.StringValue(cloudResp.Result.Status)
	} else {
		config.Status = types.StringNull()
	}

	if cloudResp.Result.State != "" {
		config.State = types.StringValue(cloudResp.Result.State)
	} else {
		config.State = types.StringNull()
	}

	// Cloud-level boolean settings come straight off the cloud payload.
	config.AutoAddUser = types.BoolValue(cloudResp.Result.AutoAddUser)
	config.EnableLineageTracking = types.BoolValue(cloudResp.Result.LineageTrackingEnabled)
	config.EnableLogIngestion = types.BoolValue(cloudResp.Result.IsAggregatedLogsEnabled)

	// is_empty_cloud and cloud_deployment_id aren't on the cloud payload itself -
	// derive them from the cloud's resources the same way anyscale_cloud_resource
	// does: no resources attached at all means the empty-cloud pattern is in
	// effect; the default/primary resource (if any) carries the deployment ID.
	resources, err := listCloudResources(ctx, d.client, cloudID)
	if err != nil {
		return fmt.Errorf("failed to list cloud resources: %w", err)
	}

	config.IsEmptyCloud = types.BoolValue(len(resources) == 0)

	if defaultResource := findDefaultInCloudResources(resources); defaultResource != nil {
		config.CloudDeploymentID = types.StringValue(defaultResource.CloudDeploymentID)
	} else {
		config.CloudDeploymentID = types.StringNull()
	}

	return nil
}

// findCloudByName looks for a cloud with the given name
func (d *CloudDataSource) findCloudByName(ctx context.Context, name string) (string, error) {
	resp, err := d.client.DoRequest(ctx, "GET", "/api/v2/clouds", nil)
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
		return "", err
	}

	// Find clouds with matching name
	// If multiple exist, return the most recently created one
	var matchedCloudID string
	var latestCreatedAt string

	for _, cloud := range cloudsResp.Results {
		if cloud.Name == name {
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	if matchedCloudID != "" && len(cloudsResp.Results) > 1 {
		// Log warning if multiple clouds with same name exist
		tflog.Warn(ctx, "Multiple clouds found with same name, returning most recent", map[string]any{
			"name":       name,
			"cloud_id":   matchedCloudID,
			"created_at": latestCreatedAt,
		})
	}

	return matchedCloudID, nil
}
