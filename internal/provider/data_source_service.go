package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ServiceDataSource{}
	_ datasource.DataSourceWithConfigure = &ServiceDataSource{}
)

// NewServiceDataSource creates a new service data source.
func NewServiceDataSource() datasource.DataSource {
	return &ServiceDataSource{}
}

// ServiceDataSource defines the data source implementation.
type ServiceDataSource struct {
	client *Client
}

// ServiceDataSourceModel describes the data source data model. See
// .crystl/quest/CONTRACT_anyscale_service.md for the full field-scope contract.
type ServiceDataSourceModel struct {
	// Input attributes (at least one required)
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Narrowing filters for the by-name lookup (both optional, both also populated as output)
	ProjectID types.String `tfsdk:"project_id"`
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	// Computed outputs
	Description        types.String `tfsdk:"description"`
	CreatorID          types.String `tfsdk:"creator_id"`
	CreatedAt          types.String `tfsdk:"created_at"`
	EndedAt            types.String `tfsdk:"ended_at"`
	Hostname           types.String `tfsdk:"hostname"`
	BaseURL            types.String `tfsdk:"base_url"`
	CurrentState       types.String `tfsdk:"current_state"`
	GoalState          types.String `tfsdk:"goal_state"`
	AutoRolloutEnabled types.Bool   `tfsdk:"auto_rollout_enabled"`
	IsMultiVersion     types.Bool   `tfsdk:"is_multi_version"`
	ErrorMessage       types.String `tfsdk:"error_message"`

	// types.Object, not the plain ServiceObservabilityURLsModel/ServiceVersionModel structs: a
	// bare struct can only represent a fully-known object. CONFIRMED via a real CI acceptance
	// test crash (TestAccServiceDataSource_ByID, "Path: service_observability_urls", "Received
	// null value") - a data source must not crash on a null it can receive, independent of how
	// commonly that null occurs (architect's ruling).
	ServiceObservabilityURLs types.Object `tfsdk:"service_observability_urls"`

	PrimaryVersion types.Object         `tfsdk:"primary_version"`
	CanaryVersion  *ServiceVersionModel `tfsdk:"canary_version"`

	ServiceStatusChecklist *ServiceStatusChecklistModel `tfsdk:"service_status_checklist"`
}

// ServiceObservabilityURLsModel represents the nested service_observability_urls object.
type ServiceObservabilityURLsModel struct {
	ServiceDashboardURL                  types.String `tfsdk:"service_dashboard_url"`
	ServiceDashboardEmbeddingURL         types.String `tfsdk:"service_dashboard_embedding_url"`
	ServeDeploymentDashboardURL          types.String `tfsdk:"serve_deployment_dashboard_url"`
	ServeDeploymentDashboardEmbeddingURL types.String `tfsdk:"serve_deployment_dashboard_embedding_url"`
}

// ServiceVersionModel represents the nested primary_version / canary_version object.
type ServiceVersionModel struct {
	ID               types.String `tfsdk:"id"`
	CreatedAt        types.String `tfsdk:"created_at"`
	Version          types.String `tfsdk:"version"`
	CurrentState     types.String `tfsdk:"current_state"`
	Weight           types.Int64  `tfsdk:"weight"`
	CurrentWeight    types.Int64  `tfsdk:"current_weight"`
	TargetWeight     types.Int64  `tfsdk:"target_weight"`
	BuildID          types.String `tfsdk:"build_id"`
	ComputeConfigID  types.String `tfsdk:"compute_config_id"`
	ProductionJobIDs types.List   `tfsdk:"production_job_ids"`
	ConnectionIDs    types.List   `tfsdk:"connection_ids"`
	RayServeConfig   types.String `tfsdk:"ray_serve_config"`
}

// ServiceStatusChecklistModel represents the nested service_status_checklist object.
type ServiceStatusChecklistModel struct {
	Shared     []StatusChecklistItemModel `tfsdk:"shared"`
	PerVersion []VersionChecklistModel    `tfsdk:"per_version"`
}

// VersionChecklistModel represents one per_version entry within a ServiceStatusChecklistModel.
type VersionChecklistModel struct {
	VersionID types.String               `tfsdk:"version_id"`
	Items     []StatusChecklistItemModel `tfsdk:"items"`
}

// StatusChecklistItemModel represents one row of a service's per-component status checklist.
type StatusChecklistItemModel struct {
	Kind       types.String `tfsdk:"kind"`
	Label      types.String `tfsdk:"label"`
	State      types.String `tfsdk:"state"`
	Message    types.String `tfsdk:"message"`
	VersionID  types.String `tfsdk:"version_id"`
	ObservedAt types.String `tfsdk:"observed_at"`
}

// Metadata returns the data source type name.
func (d *ServiceDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

// Schema defines the schema for the data source.
func (d *ServiceDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := serviceSharedAttributes()
	attributes["id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The unique identifier of the service. Either `id` or `name` must be specified.",
	}
	attributes["name"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The name of the service. Either `id` or `name` must be specified. This is an exact match: service names are unique only within a project (not organization-wide), so if the same name exists in more than one project, this lookup fails with an error asking you to set `project_id` (and/or `cloud_id`) to disambiguate, rather than silently guessing.",
	}
	attributes["project_id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The project ID this service belongs to. Can be used to narrow a lookup by `name` when the same name exists in more than one project.",
	}
	attributes["cloud_id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The cloud ID this service belongs to. Can be used as a filter when looking up by name.",
	}
	attributes["cloud_name"] = schema.StringAttribute{
		Optional:            true,
		MarkdownDescription: "The cloud name this service belongs to. Can be used as a filter when looking up by name. Will be resolved to `cloud_id`.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches details about an Anyscale Service by ID or name. Use this data source to look up a service - one deployed by the `anyscale_service` resource, or one deployed and managed some other way (e.g. the Anyscale CLI or console) - without taking over its lifecycle. To create a service or roll out new versions from Terraform, use the `anyscale_service` resource instead; this data source stays read-only.",
		Attributes:          attributes,
	}
}

// Configure adds the provider configured client to the data source.
func (d *ServiceDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *ServiceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ServiceDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate inputs
	if config.ID.IsNull() && config.Name.IsNull() {
		AddConfigError(&resp.Diagnostics, "Missing Required Attribute",
			"Either 'id' or 'name' must be specified to look up a service.")
		return
	}

	// Resolve cloud_name to cloud_id if provided
	cloudID, ok := resolveCloudIDFilter(ctx, d.client, config.CloudID, config.CloudName, &resp.Diagnostics)
	if !ok {
		return
	}

	projectID := config.ProjectID.ValueString()

	// Determine lookup strategy
	var serviceID string
	var err error

	if !config.ID.IsNull() {
		// Direct lookup by ID
		serviceID = config.ID.ValueString()
		tflog.Debug(ctx, "Looking up service by ID", map[string]any{"service_id": serviceID})
	} else {
		// Lookup by name
		serviceName := config.Name.ValueString()
		tflog.Debug(ctx, "Looking up service by name", map[string]any{
			"service_name": serviceName,
			"project_id":   projectID,
			"cloud_id":     cloudID,
		})

		serviceID, err = d.findServiceByName(ctx, serviceName, projectID, cloudID)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "find service by name", err)
			return
		}
	}

	// Fetch service details
	service, err := d.getService(ctx, serviceID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read service", err)
		return
	}

	diags := populateServiceDataSourceModel(ctx, &config, service)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// findServiceByName resolves a service by exact name, optionally narrowed by projectID/cloudID.
//
// Unlike PickMostRecentMatch (used by every other by-name lookup in this provider - cloud,
// project, compute config, container image), this errors on more than one exact match rather than
// silently picking the most recent. Service names are unique only within a project
// (backend Service model: project_id and name are independent required columns with no
// organization-wide uniqueness), so two different projects each holding an identically-named
// service is a normal, expected state - not an accidental collision the way a duplicate cloud or
// project name would be. Silently resolving to "the most recent one" would let an unrelated
// team's later same-named deploy quietly re-point an existing data source at a different service
// on the next refresh. See .crystl/quest/CONTRACT_anyscale_service.md for the full rationale.
func (d *ServiceDataSource) findServiceByName(ctx context.Context, name, projectID, cloudID string) (string, error) {
	// Build query parameters. The list endpoint's "name" param is a case-insensitive substring
	// match server-side (services_dao.py: Service.name.ilike) - this is only a cheap pre-filter;
	// the exact match happens client-side below.
	params := url.Values{}
	params.Add("name", name)
	if projectID != "" {
		params.Add("project_id", projectID)
	}
	if cloudID != "" {
		params.Add("cloud_id", cloudID)
	}

	results, err := PaginatedRequest(ctx, d.client, "/api/v2/services-v2", params,
		func(body []byte) ([]ServiceResult, *string, error) {
			var servicesResp ServicesListResponse
			if err := json.Unmarshal(body, &servicesResp); err != nil {
				return nil, nil, err
			}
			return servicesResp.Results, servicesResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to list services: %w", err)
	}

	var matches []ServiceResult
	for _, s := range results {
		if s.Name == name {
			matches = append(matches, s)
		}
	}

	scope := ""
	if projectID != "" {
		scope += fmt.Sprintf(" in project '%s'", projectID)
	}
	if cloudID != "" {
		scope += fmt.Sprintf(" in cloud '%s'", cloudID)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no service found with name '%s'%s", name, scope)
	case 1:
		return matches[0].ID, nil
	default:
		return "", fmt.Errorf(
			"found %d services named '%s'%s; service names are unique only within a project - set project_id (and/or cloud_id) to disambiguate",
			len(matches), name, scope,
		)
	}
}

// getService fetches a single service by ID.
func (d *ServiceDataSource) getService(ctx context.Context, serviceID string) (*ServiceResult, error) {
	serviceResp, err := DoRequestAndParse[ServiceResponse](
		ctx, d.client, "GET", fmt.Sprintf("/api/v2/services-v2/%s", serviceID), nil, http.StatusOK,
	)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, fmt.Errorf("service not found")
		}
		return nil, fmt.Errorf("failed to get service: %w", err)
	}

	return &serviceResp.Result, nil
}
