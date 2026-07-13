package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ContainerImageDataSource{}
	_ datasource.DataSourceWithConfigure = &ContainerImageDataSource{}
)

// NewContainerImageDataSource creates a new container image data source.
func NewContainerImageDataSource() datasource.DataSource {
	return &ContainerImageDataSource{}
}

// ContainerImageDataSource defines the data source implementation.
type ContainerImageDataSource struct {
	client *Client
}

// ContainerImageDataSourceModel describes the data source data model.
type ContainerImageDataSourceModel struct {
	// Input attributes (one required)
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Output attributes
	BuildID     types.String `tfsdk:"build_id"`
	ImageURI    types.String `tfsdk:"image_uri"`
	RayVersion  types.String `tfsdk:"ray_version"`
	BuildStatus types.String `tfsdk:"build_status"`
	IsBYOD      types.Bool   `tfsdk:"is_byod"`
	CreatedAt   types.String `tfsdk:"created_at"`
	CreatorID   types.String `tfsdk:"creator_id"`
	Revision    types.Int64  `tfsdk:"revision"`
	Digest      types.String `tfsdk:"digest"`
	NameVersion types.String `tfsdk:"name_version"` // Formatted as "name:revision" for use with Anyscale APIs

	// DS-IMG-4 (Phase B). BuildErrorMessage is singular-only: it comes from the
	// full per-build GET, which only this data source makes.
	BuildErrorMessage types.String `tfsdk:"build_error_message"`
	CloudID           types.String `tfsdk:"cloud_id"`
	IsDefault         types.Bool   `tfsdk:"is_default"`
	IsExperimental    types.Bool   `tfsdk:"is_experimental"`
	LastModifiedAt    types.String `tfsdk:"last_modified_at"`
}

// Metadata returns the data source type name.
func (d *ContainerImageDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_image"
}

// Schema defines the schema for the data source.
func (d *ContainerImageDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := containerImageSharedAttributes()
	attributes["id"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: "The unique identifier of the container image. Either `id` or `name` must be specified. If both are set, `id` takes precedence.",
		Validators: []validator.String{
			stringvalidator.AtLeastOneOf(
				path.MatchRoot("id"),
				path.MatchRoot("name"),
			),
		},
	}
	attributes["name"] = schema.StringAttribute{
		Optional:            true,
		MarkdownDescription: "The name of the container image. Either `id` or `name` must be specified.",
	}
	attributes["build_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The unique identifier of the latest build for this container image. Null if no build has been triggered yet.",
	}
	attributes["ray_version"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The Ray version used in the build. For BYOD images, this resolves from the build's `byod_ray_version` field when the standard field is absent. Null if the image has no build yet, if the build's details couldn't be retrieved, or if the latest build hasn't reported a Ray version yet.",
	}
	attributes["build_status"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The status of the latest build (`pending`, `in_progress`, `succeeded`, `failed`, `pending_cancellation`, `canceled`). Null if no build has been triggered yet, or if the build's details couldn't be retrieved.",
	}
	attributes["is_byod"] = schema.BoolAttribute{
		Computed:            true,
		MarkdownDescription: "Whether this is a BYOD (Bring Your Own Docker) image. Null if no build has been triggered yet, or if the build's details couldn't be retrieved.",
	}
	attributes["digest"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The content digest of the built container image (e.g. `sha256:...`). Null if the image has no build yet, if the build's details couldn't be retrieved, or if the latest build hasn't produced a digest yet.",
	}
	// DS-IMG-4 (Phase B): is_default/is_experimental/last_modified_at/cloud_id are
	// template-level fields, present on both the get-by-id and list responses -
	// shared with the plural via containerImageSharedAttributes below except
	// build_error_message, which only this data source's second per-build GET
	// can populate.
	attributes["build_error_message"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "The error message from the latest build, if it failed. Null if the build succeeded, is still in progress, or hasn't started yet. Only available on this singular lookup - the plural `anyscale_container_images` data source's lighter per-item response doesn't include build error details without an extra network call per image.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Retrieves information about an existing Anyscale container image (cluster environment). Use this data source to look up container images by ID or name.",
		Attributes:          attributes,
	}
}

// Configure adds the provider configured client to the data source.
func (d *ContainerImageDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics, "Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *ContainerImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ContainerImageDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var template *ApplicationTemplateResult
	var err error

	// Look up by ID or name
	if !config.ID.IsNull() && config.ID.ValueString() != "" {
		template, err = d.getApplicationTemplateByID(ctx, config.ID.ValueString())
	} else if !config.Name.IsNull() && config.Name.ValueString() != "" {
		template, err = d.getApplicationTemplateByName(ctx, config.Name.ValueString())
	} else {
		AddConfigError(&resp.Diagnostics, "Missing Required Attribute",
			"Either 'id' or 'name' must be specified.")
		return
	}

	if err != nil {
		AddAPIError(&resp.Diagnostics, "read container image", err)
		return
	}

	// Map application template to model
	config.ID = types.StringValue(template.ID)
	config.Name = types.StringValue(template.Name)
	config.CreatedAt = types.StringValue(template.CreatedAt)
	config.CreatorID = stringOrNull(template.CreatorID)

	// DS-IMG-4 (Phase B): template-level fields, always available regardless
	// of whether a build exists.
	config.CloudID = types.StringPointerValue(template.CloudID)
	config.IsDefault = types.BoolValue(template.IsDefault)
	config.IsExperimental = types.BoolValue(template.IsExperimental)
	config.LastModifiedAt = stringOrNull(template.LastModifiedAt)

	// Resolve the latest build contract-based, via the template's own latest_build
	// reference. DS-IMG-2: image_uri now reads straight off the embedded
	// latest_build.docker_image_name - it no longer depends on the second
	// per-build GET succeeding, unlike build_status/is_byod/revision/digest/
	// name_version/build_error_message below, which still need that full
	// build record.
	if template.LatestBuild != nil {
		config.BuildID = types.StringValue(template.LatestBuild.ID)
		config.ImageURI = types.StringPointerValue(template.LatestBuild.DockerImageName)

		// Get full build details
		build, err := d.getBuild(ctx, template.LatestBuild.ID)
		if err != nil {
			tflog.Warn(ctx, "Failed to get build details", map[string]any{
				"build_id": template.LatestBuild.ID,
				"error":    err.Error(),
			})
			config.BuildStatus = types.StringNull()
			config.RayVersion = types.StringNull()
			config.IsBYOD = types.BoolNull()
			config.Revision = types.Int64Null()
			config.Digest = types.StringNull()
			config.NameVersion = types.StringNull()
			config.BuildErrorMessage = types.StringNull()
		} else {
			config.BuildStatus = types.StringValue(build.Status)
			config.IsBYOD = types.BoolValue(build.IsBYOD)
			config.Revision = types.Int64Value(int64(build.Revision))
			config.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", template.Name, build.Revision))
			// DS-IMG-1: resolves to byod_ray_version when the plain ray_version
			// field is absent (the common case for BYOD images), instead of
			// reporting null for a version the backend actually knows.
			config.RayVersion = types.StringPointerValue(build.ResolvedRayVersion())
			config.Digest = types.StringPointerValue(build.Digest)
			config.BuildErrorMessage = types.StringPointerValue(build.ErrorMessage)
		}
	} else {
		config.BuildID = types.StringNull()
		config.BuildStatus = types.StringNull()
		config.ImageURI = types.StringNull()
		config.RayVersion = types.StringNull()
		config.IsBYOD = types.BoolNull()
		config.Revision = types.Int64Null()
		config.Digest = types.StringNull()
		config.NameVersion = types.StringNull()
		config.BuildErrorMessage = types.StringNull()
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// Helper functions

// getApplicationTemplateByID fetches the decorated application template by ID.
func (d *ContainerImageDataSource) getApplicationTemplateByID(ctx context.Context, id string) (*ApplicationTemplateResult, error) {
	tflog.Debug(ctx, "Fetching application template by ID", map[string]any{"id": id})

	templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/api/v2/application_templates/%s", id),
		nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster environment %s: %w", id, err)
	}

	return &templateResp.Result, nil
}

// getApplicationTemplateByName fetches an application template by exact name match.
// GET /api/v2/application_templates/ only supports a name_contains substring filter, so this
// pages through the full result set (via PaginatedRequest, following next_paging_token) and
// filters client-side for an exact, non-archived match across ALL pages - not just the first.
func (d *ContainerImageDataSource) getApplicationTemplateByName(ctx context.Context, name string) (*ApplicationTemplateResult, error) {
	tflog.Debug(ctx, "Fetching application template by name", map[string]any{"name": name})

	params := url.Values{}
	params.Set("name_contains", name)
	params.Set("include_archived", "false")

	results, err := PaginatedRequest(ctx, d.client, "/api/v2/application_templates/", params,
		func(body []byte) ([]ApplicationTemplateResult, *string, error) {
			var listResp ApplicationTemplatesListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, err
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search cluster environments: %w", err)
	}

	// Find exact match across every page - name_contains is a substring filter, so an exact
	// match may sit on any page, not just the first.
	matches := filterExactApplicationTemplateMatches(results, name)

	if len(matches) == 0 {
		return nil, fmt.Errorf("no cluster environment found with name '%s'", name)
	}

	if len(matches) > 1 {
		WarnIfMultipleMatches(ctx, "cluster environment", name, len(matches), matches[0].ID)
	}

	// Return the first match (or most recent if multiple)
	return &matches[0], nil
}

// filterExactApplicationTemplateMatches narrows a name_contains substring search down to
// non-archived results whose name is an exact match.
func filterExactApplicationTemplateMatches(results []ApplicationTemplateResult, name string) []ApplicationTemplateResult {
	var matches []ApplicationTemplateResult
	for _, tmpl := range results {
		if tmpl.Name == name && !tmpl.IsArchived() {
			matches = append(matches, tmpl)
		}
	}
	return matches
}

// getBuild fetches the current build details.
func (d *ContainerImageDataSource) getBuild(ctx context.Context, buildID string) (*BuildResult, error) {
	// Note: The Anyscale API returns 201 for GET build endpoints
	buildResp, err := DoRequestAndParse[BuildResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/api/v2/builds/%s", buildID),
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get build %s: %w", buildID, err)
	}

	return &buildResp.Result, nil
}
