package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ServicesDataSource{}
	_ datasource.DataSourceWithConfigure = &ServicesDataSource{}
)

// NewServicesDataSource creates a new services data source.
func NewServicesDataSource() datasource.DataSource {
	return &ServicesDataSource{}
}

// ServicesDataSource defines the data source implementation.
type ServicesDataSource struct {
	client *Client
}

// ServicesDataSourceModel describes the data source data model. See
// .crystl/quest/CONTRACT_anyscale_service.md for the full field-scope contract.
type ServicesDataSourceModel struct {
	// Filter inputs (all optional)
	NameContains types.String `tfsdk:"name_contains"`
	ProjectID    types.String `tfsdk:"project_id"`
	CloudID      types.String `tfsdk:"cloud_id"`
	CloudName    types.String `tfsdk:"cloud_name"`
	CreatorID    types.String `tfsdk:"creator_id"`

	// Computed output
	Services []ServiceSummaryModel `tfsdk:"services"`
}

// ServiceSummaryModel represents one service in the list. Unlike anyscale_project/
// anyscale_projects, this carries the SAME full field set as the singular data source's model
// rather than a trimmed-for-performance subset: the backend's list response item
// (DecoratedListServiceAPIModel) is a strict superset of the get response item (the only extra
// field is `type`, which this provider excludes as a redundant discriminator - see
// CONTRACT_anyscale_service.md), so there is no extra per-item API call to avoid the way
// anyscale_projects avoids one for collaborators. Trimming here would only lose information for
// free.
type ServiceSummaryModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	ProjectID          types.String `tfsdk:"project_id"`
	CloudID            types.String `tfsdk:"cloud_id"`
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

	// types.Object, not the plain ServiceObservabilityURLsModel/ServiceVersionModel structs -
	// see the singular data source's identical fields for why.
	ServiceObservabilityURLs types.Object `tfsdk:"service_observability_urls"`

	PrimaryVersion types.Object         `tfsdk:"primary_version"`
	CanaryVersion  *ServiceVersionModel `tfsdk:"canary_version"`

	ServiceStatusChecklist *ServiceStatusChecklistModel `tfsdk:"service_status_checklist"`
}

// Metadata returns the data source type name.
func (d *ServicesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_services"
}

// Schema defines the schema for the data source.
func (d *ServicesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	itemAttributes := serviceSharedAttributes()
	itemAttributes["id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The unique identifier of the service.",
	}
	itemAttributes["name"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The name of the service.",
	}
	itemAttributes["project_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The project ID this service belongs to.",
	}
	itemAttributes["cloud_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The cloud ID this service belongs to.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists and filters Anyscale Services. Unlike `anyscale_projects` (which omits collaborator details for performance), each item here carries the same full detail as `anyscale_service`, since the backend's list response already includes it at no extra API-call cost.",

		Attributes: map[string]schema.Attribute{
			"name_contains": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter services by partial, case-insensitive name match. (The underlying API filter parameter is confusingly named `name` even though it is a substring match; this attribute is named `name_contains` instead, consistent with `anyscale_projects`, so the provider's own naming convention - a bare `name` means exact match, `name_contains` means substring - stays predictable.)",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter services by project ID.",
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter services by cloud ID.",
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter services by cloud name. Will be resolved to cloud_id.",
			},
			"creator_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Filter services by creator ID.",
			},
			"services": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of services matching the filters.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: itemAttributes,
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *ServicesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *ServicesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ServicesDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve cloud_name to cloud_id if provided
	cloudID, ok := resolveCloudIDFilter(ctx, d.client, config.CloudID, config.CloudName, &resp.Diagnostics)
	if !ok {
		return
	}

	// Build query parameters. name_contains forwards to the wire's "name" param, which is a
	// substring match server-side (services_dao.py: Service.name.ilike) despite its wire name.
	params := url.Values{}
	if !config.NameContains.IsNull() {
		params.Add("name", config.NameContains.ValueString())
	}
	if !config.ProjectID.IsNull() {
		params.Add("project_id", config.ProjectID.ValueString())
	}
	if cloudID != "" {
		params.Add("cloud_id", cloudID)
	}
	if !config.CreatorID.IsNull() {
		params.Add("creator_id", config.CreatorID.ValueString())
	}

	tflog.Debug(ctx, "Fetching services with filters", map[string]any{
		"filters": params.Encode(),
	})

	services, diags := d.fetchServices(ctx, params)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Services fetched successfully", map[string]any{"count": len(services)})

	config.Services = services

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// fetchServices fetches services with the given query parameters, handling pagination
// automatically, and maps them to the list's item model.
func (d *ServicesDataSource) fetchServices(ctx context.Context, params url.Values) ([]ServiceSummaryModel, diag.Diagnostics) {
	var diags diag.Diagnostics

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
		diags.AddError("list services", err.Error())
		return nil, diags
	}

	allServices := make([]ServiceSummaryModel, 0, len(results))
	for _, s := range results {
		summary, sDiags := serviceResultToSummaryModel(ctx, s)
		diags.Append(sDiags...)
		allServices = append(allServices, summary)
	}

	return allServices, diags
}

// serviceResultToSummaryModel maps a ServiceResult to the plural data source's per-item model.
func serviceResultToSummaryModel(ctx context.Context, s ServiceResult) (ServiceSummaryModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	model := ServiceSummaryModel{
		ID:                     types.StringValue(s.ID),
		Name:                   types.StringValue(s.Name),
		ProjectID:              types.StringValue(s.ProjectID),
		CloudID:                types.StringValue(s.CloudID),
		Description:            types.StringPointerValue(s.Description),
		CreatorID:              types.StringValue(s.CreatorID),
		CreatedAt:              types.StringValue(s.CreatedAt),
		EndedAt:                types.StringPointerValue(s.EndedAt),
		Hostname:               types.StringValue(s.Hostname),
		BaseURL:                types.StringValue(s.BaseURL),
		CurrentState:           types.StringValue(s.CurrentState),
		GoalState:              types.StringValue(s.GoalState),
		AutoRolloutEnabled:     types.BoolValue(s.AutoRolloutEnabled),
		IsMultiVersion:         types.BoolValue(s.IsMultiVersion),
		ErrorMessage:           types.StringPointerValue(s.ErrorMessage),
		ServiceStatusChecklist: serviceStatusChecklistToModel(s.ServiceStatusChecklist),
	}

	obsURLsObj, obsDiags := serviceObservabilityURLsToObject(ctx, s.ServiceObservabilityURLs)
	diags.Append(obsDiags...)
	model.ServiceObservabilityURLs = obsURLsObj

	if s.PrimaryVersion != nil {
		primaryVersion, vDiags := serviceVersionResultToModel(ctx, *s.PrimaryVersion)
		diags.Append(vDiags...)
		primaryObj, pObjDiags := types.ObjectValueFrom(ctx, serviceVersionAttrTypes, primaryVersion)
		diags.Append(pObjDiags...)
		model.PrimaryVersion = primaryObj
	} else {
		model.PrimaryVersion = types.ObjectNull(serviceVersionAttrTypes)
	}

	if s.CanaryVersion != nil {
		canaryVersion, cDiags := serviceVersionResultToModel(ctx, *s.CanaryVersion)
		diags.Append(cDiags...)
		model.CanaryVersion = &canaryVersion
	}

	return model, diags
}
