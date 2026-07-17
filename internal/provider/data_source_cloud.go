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
	CloudResourceID       types.String `tfsdk:"cloud_resource_id"`
	AutoAddUser           types.Bool   `tfsdk:"auto_add_user"`
	EnableLineageTracking types.Bool   `tfsdk:"enable_lineage_tracking"`
	EnableLogIngestion    types.Bool   `tfsdk:"enable_log_ingestion"`

	// C2: parity with the plural anyscale_clouds data source's per-item
	// fields. Names deliberately kept as-is (not renamed to match the
	// plural's lineage_tracking_enabled/is_aggregated_logs_enabled) - see
	// CLOUD-SYNC-DESIGN.md C7, renaming an existing attribute is breaking.
	ComputeStack           types.String `tfsdk:"compute_stack"`
	CreatedAt              types.String `tfsdk:"created_at"`
	CreatorID              types.String `tfsdk:"creator_id"`
	IsDefault              types.Bool   `tfsdk:"is_default"`
	IsAIOA                 types.Bool   `tfsdk:"is_aioa"`
	IsBringYourOwnResource types.Bool   `tfsdk:"is_bring_your_own_resource"`
	IsPrivateCloud         types.Bool   `tfsdk:"is_private_cloud"`
	IsPrivateServiceCloud  types.Bool   `tfsdk:"is_private_service_cloud"`

	// DS-CLOUD-4/DS-CLOUD-5: parity fields added to both anyscale_cloud and
	// anyscale_clouds via cloudSharedAttributes (is_k8s is schema-only shared,
	// not part of that map - see the Schema function).
	IsK8s             types.Bool   `tfsdk:"is_k8s"`
	AvailabilityZones types.List   `tfsdk:"availability_zones"`
	Version           types.String `tfsdk:"version"`
	ExternalID        types.String `tfsdk:"external_id"`
}

// Metadata returns the data source type name.
func (d *CloudDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud"
}

// Schema defines the data source schema.
func (d *CloudDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := cloudSharedAttributes()
	attributes["id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The unique identifier of the cloud. Either `id` or `name` must be specified.",
	}
	attributes["name"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The name of the cloud. Either `id` or `name` must be specified. If multiple clouds have the same name, the most recently created one will be returned.",
	}
	attributes["is_empty_cloud"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether this is an empty cloud (created without embedded resource configuration).",
	}
	attributes["cloud_deployment_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The cloud deployment ID. Deprecated and always null: the Anyscale API no longer populates this field. Use this data source's own `cloud_resource_id` attribute instead, which carries the populated identifier (e.g. to pass to the Anyscale operator during installation for a K8S cloud).",
		DeprecationMessage:  "Deprecated by the Anyscale API; the backend no longer populates this field. Will be removed in a future major release - use this data source's own `cloud_resource_id` instead.",
	}
	attributes["cloud_resource_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The unique cloud resource ID assigned by Anyscale for this cloud's default resource - the populated identifier that `cloud_deployment_id` was originally meant to be. This is what you pass to the Anyscale operator during installation for a K8S cloud. Null for a genuinely empty cloud (no resources attached yet).",
	}
	// C7: same backend field as the plural's lineage_tracking_enabled/is_aggregated_logs_enabled,
	// kept under these names since renaming a shipped attribute is breaking. See
	// CLOUD-SYNC-DESIGN.md C7 and schema_shared_attributes.go's cloudSharedAttributes doc.
	attributes["enable_lineage_tracking"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether lineage tracking is enabled for this cloud.",
	}
	attributes["enable_log_ingestion"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether aggregated log ingestion is enabled for this cloud.",
	}
	// DS-CLOUD-4: parity with the plural's is_k8s. Schema-only shared text (not
	// hoisted into cloudSharedAttributes, since the plural's own copy is
	// defined directly on its item attributes too, not through that map).
	attributes["is_k8s"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether this cloud uses Kubernetes.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve information about an existing Anyscale Cloud. You can look up a cloud by its ID or name.",
		Attributes:          attributes,
	}
}

// Configure adds the provider configured client to the data source.
func (d *CloudDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *CloudDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config CloudDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate that either ID or Name is provided
	if config.ID.IsNull() && config.Name.IsNull() {
		AddConfigError(&resp.Diagnostics,
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
			AddAPIError(&resp.Diagnostics, fmt.Sprintf("find cloud with name '%s'", name), err)
			return
		}

		if cloudID == "" {
			AddConfigError(&resp.Diagnostics,
				"Cloud Not Found",
				fmt.Sprintf("No cloud found with name '%s'", name),
			)
			return
		}
	}

	if err := d.readCloudIntoModel(ctx, cloudID, &config); err != nil {
		if strings.Contains(err.Error(), "not found") {
			AddConfigError(&resp.Diagnostics,
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

	// DS-CLOUD-3: shared with the plural DS's identical guard (stringOrNull) so
	// the two behave the same way, not just similarly.
	config.Status = stringOrNull(cloudResp.Result.Status)
	config.State = stringOrNull(cloudResp.Result.State)

	// Cloud-level boolean settings come straight off the cloud payload.
	config.AutoAddUser = types.BoolValue(cloudResp.Result.AutoAddUser)
	config.EnableLineageTracking = types.BoolValue(cloudResp.Result.LineageTrackingEnabled)
	config.EnableLogIngestion = types.BoolValue(cloudResp.Result.IsAggregatedLogsEnabled)

	// C2: parity fields with the plural anyscale_clouds data source - mapped
	// the same unconditional way plural's fetchClouds does, so the two
	// report identical values for the same cloud rather than diverging on
	// edge cases like an empty compute_stack from a pre-field-existing cloud.
	config.ComputeStack = types.StringValue(cloudResp.Result.ComputeStack)
	config.CreatedAt = types.StringValue(cloudResp.Result.CreatedAt)
	config.CreatorID = types.StringValue(cloudResp.Result.CreatorID)
	config.IsDefault = types.BoolValue(cloudResp.Result.IsDefault)
	config.IsAIOA = types.BoolValue(cloudResp.Result.IsAIOA)
	config.IsBringYourOwnResource = types.BoolValue(cloudResp.Result.IsBringYourOwnResource)
	config.IsPrivateCloud = types.BoolValue(cloudResp.Result.IsPrivateCloud)
	config.IsPrivateServiceCloud = types.BoolValue(cloudResp.Result.IsPrivateServiceCloud)

	// DS-CLOUD-4/DS-CLOUD-5 (Phase B parity fields). is_k8s/version are plain
	// bool/string on the backend Cloud model (always populated, no null case).
	// external_id is genuinely Optional[str] server-side (validated to start
	// with "org_" when set) - StringPointerValue so an unset external_id is
	// Terraform null, never "".
	config.IsK8s = types.BoolValue(cloudResp.Result.IsK8s)
	config.Version = types.StringValue(cloudResp.Result.Version)
	config.ExternalID = types.StringPointerValue(cloudResp.Result.ExternalID)

	azList, azDiags := types.ListValueFrom(ctx, types.StringType, cloudResp.Result.AvailabilityZones)
	if azDiags.HasError() {
		return fmt.Errorf("failed to convert availability_zones: %v", azDiags)
	}
	config.AvailabilityZones = azList

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
		config.CloudDeploymentID = stringOrNull(defaultResource.CloudDeploymentID)
		config.CloudResourceID = types.StringValue(defaultResource.CloudResourceID)
	} else {
		config.CloudDeploymentID = types.StringNull()
		config.CloudResourceID = types.StringNull()
	}

	return nil
}

// findCloudByName looks for a cloud with the given name, across every page of
// GET /api/v2/clouds rather than just the first (DS-CLOUD-2/X-4: a valid name
// used to resolve to "not found" once an org's cloud list exceeded one page).
// On duplicate names, picks the most recently created match (X-2).
func (d *CloudDataSource) findCloudByName(ctx context.Context, name string) (string, error) {
	results, err := PaginatedRequest(ctx, d.client, "/api/v2/clouds", nil,
		func(body []byte) ([]CloudResult, *string, error) {
			var cloudsResp CloudsListResponse
			if err := json.Unmarshal(body, &cloudsResp); err != nil {
				return nil, nil, fmt.Errorf("failed to parse clouds response: %w", err)
			}
			return cloudsResp.Results, cloudsResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}

	matchedCloudID := PickMostRecentMatch(ctx, "cloud", name, results,
		func(c CloudResult) bool { return c.Name == name },
		func(c CloudResult) string { return c.ID },
		func(c CloudResult) string { return c.CreatedAt },
	)

	return matchedCloudID, nil
}
