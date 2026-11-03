package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
	CloudProvider          types.String `tfsdk:"cloud_provider"`
	Region                 types.String `tfsdk:"region"`
	Status                 types.String `tfsdk:"status"`
	State                  types.String `tfsdk:"state"`
	IsEmptyCloud           types.Bool   `tfsdk:"is_empty_cloud"`
	CloudResourceID        types.String `tfsdk:"cloud_resource_id"`
	AutoAddUser            types.Bool   `tfsdk:"auto_add_user"`
	LineageTrackingEnabled types.Bool   `tfsdk:"lineage_tracking_enabled"`
	AggregatedLogsEnabled  types.Bool   `tfsdk:"aggregated_logs_enabled"`

	// C2: parity with the plural anyscale_clouds data source's per-item
	// fields. lineage_tracking_enabled/aggregated_logs_enabled above were
	// the odd ones out here until this provider's naming-unification rename -
	// this singular data source (and the anyscale_cloud resource) previously
	// called them enable_lineage_tracking/enable_log_ingestion, while the
	// plural anyscale_clouds data source always used the backend's own names.
	// Now identical on both sides; see CHANGELOG.md for the migration note.
	ComputeStack   types.String `tfsdk:"compute_stack"`
	CreatedAt      types.String `tfsdk:"created_at"`
	CreatorID      types.String `tfsdk:"creator_id"`
	IsDefault      types.Bool   `tfsdk:"is_default"`
	IsPrivateCloud types.Bool   `tfsdk:"is_private_cloud"`

	// DS-CLOUD-4/DS-CLOUD-5: parity fields added to both anyscale_cloud and
	// anyscale_clouds via cloudSharedAttributes (is_k8s is schema-only shared,
	// not part of that map - see the Schema function).
	IsK8s             types.Bool   `tfsdk:"is_k8s"`
	AvailabilityZones types.List   `tfsdk:"availability_zones"`
	Version           types.String `tfsdk:"version"`
	ExternalID        types.String `tfsdk:"external_id"`
}

// applyCloudSummary copies the attributes anyscale_cloud shares with
// anyscale_clouds from the one mapper both use, cloudSummaryFromResult.
func (m *CloudDataSourceModel) applyCloudSummary(s CloudSummaryModel) {
	m.ID = s.ID
	m.Name = s.Name
	m.CloudProvider = s.CloudProvider
	m.ComputeStack = s.ComputeStack
	m.Region = s.Region
	m.Status = s.Status
	m.State = s.State
	m.CreatedAt = s.CreatedAt
	m.CreatorID = s.CreatorID
	m.IsDefault = s.IsDefault
	m.IsK8s = s.IsK8s
	m.IsPrivateCloud = s.IsPrivateCloud
	m.AutoAddUser = s.AutoAddUser
	m.LineageTrackingEnabled = s.LineageTrackingEnabled
	m.AggregatedLogsEnabled = s.AggregatedLogsEnabled
	m.AvailabilityZones = s.AvailabilityZones
	m.Version = s.Version
	m.ExternalID = s.ExternalID
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
	attributes["cloud_resource_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The unique cloud resource ID assigned by Anyscale for this cloud's default resource. This is what you pass to the Anyscale operator during installation for a K8S cloud. Null for a genuinely empty cloud (no resources attached yet).",
	}
	// Uniform attribute name with the plural anyscale_clouds data source's
	// own lineage_tracking_enabled/aggregated_logs_enabled - this data
	// source (and the anyscale_cloud resource) previously called these
	// enable_lineage_tracking/enable_log_ingestion, while the plural data
	// source called the second one is_aggregated_logs_enabled; see
	// CHANGELOG.md and schema_shared_attributes.go's cloudSharedAttributes
	// doc comment for the naming-unification history.
	attributes["lineage_tracking_enabled"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether lineage tracking is enabled for this cloud. Previously named `enable_lineage_tracking`; see CHANGELOG.md and [Naming differences](../guides/cloud-resources.md#naming-differences-between-resources-and-data-sources).",
	}
	attributes["aggregated_logs_enabled"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether aggregated log ingestion is enabled for this cloud. Previously named `enable_log_ingestion`; see CHANGELOG.md and [Naming differences](../guides/cloud-resources.md#naming-differences-between-resources-and-data-sources).",
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
	if client, ok := clientFromProviderData(req.ProviderData, "Unexpected Data Source Configure Type", &resp.Diagnostics); ok {
		d.client = client
	}
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
		if errors.Is(err, ErrNotFound) {
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
// placeholder. is_empty_cloud and cloud_resource_id require a second call
// (the cloud payload itself doesn't carry them) using the same resource-listing
// semantics anyscale_cloud_resource relies on.
func (d *CloudDataSource) readCloudIntoModel(ctx context.Context, cloudID string, config *CloudDataSourceModel) error {
	cloudResp, err := DoRequestAndParse[CloudResponse](ctx, d.client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: cloud not found", ErrNotFound)
		}
		return err
	}

	summary, err := cloudSummaryFromResult(ctx, cloudResp.Result)
	if err != nil {
		return err
	}
	config.applyCloudSummary(summary)

	// is_empty_cloud and cloud_resource_id aren't on the cloud payload itself -
	// derive them from the cloud's resources the same way anyscale_cloud_resource
	// does: no resources attached at all means the empty-cloud pattern is in
	// effect; the default/primary resource (if any) carries the resource ID.
	resources, err := listCloudResources(ctx, d.client, cloudID)
	if err != nil {
		return err
	}

	config.IsEmptyCloud = types.BoolValue(len(resources) == 0)

	if defaultResource := findDefaultInCloudResources(resources); defaultResource != nil {
		config.CloudResourceID = types.StringValue(defaultResource.CloudResourceID)
	} else {
		config.CloudResourceID = types.StringNull()
	}

	return nil
}

// findCloudByName looks for a cloud with the given name across every page of
// GET /api/v2/clouds. On duplicate names, picks the most recently created match
// (X-2).
func (d *CloudDataSource) findCloudByName(ctx context.Context, name string) (string, error) {
	results, err := listAllClouds(ctx, d.client, nil)
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
