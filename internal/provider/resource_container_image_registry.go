package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &ContainerImageRegistryResource{}
	_ resource.ResourceWithConfigure   = &ContainerImageRegistryResource{}
	_ resource.ResourceWithImportState = &ContainerImageRegistryResource{}
)

// NewContainerImageRegistryResource creates a new container image registry resource.
func NewContainerImageRegistryResource() resource.Resource {
	return &ContainerImageRegistryResource{}
}

// ContainerImageRegistryResource defines the resource implementation.
type ContainerImageRegistryResource struct {
	client *Client
}

// ContainerImageRegistryResourceModel describes the resource data model.
type ContainerImageRegistryResourceModel struct {
	// Identity - use build ID as the main resource ID
	ID types.String `tfsdk:"id"`

	// User-provided attributes
	Name                types.String `tfsdk:"name"`                  // Optional - cluster env name
	ImageURI            types.String `tfsdk:"image_uri"`             // Required
	RayVersion          types.String `tfsdk:"ray_version"`           // Optional
	RegistryLoginSecret types.String `tfsdk:"registry_login_secret"` // Optional, sensitive

	// Computed attributes
	BuildID              types.String `tfsdk:"build_id"`
	ClusterEnvironmentID types.String `tfsdk:"cluster_environment_id"`
	BuildStatus          types.String `tfsdk:"build_status"`
	CreatedAt            types.String `tfsdk:"created_at"`
	IsBYOD               types.Bool   `tfsdk:"is_byod"`
	Revision             types.Int64  `tfsdk:"revision"`
	NameVersion          types.String `tfsdk:"name_version"` // Formatted as "name:revision" for use with Anyscale APIs
}

// Metadata returns the resource type name.
func (r *ContainerImageRegistryResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_image_registry"
}

// Schema defines the schema for the resource.
func (r *ContainerImageRegistryResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `Registers an existing Docker container image with Anyscale. Use this resource to make external container images (from ECR, Docker Hub, or other registries) available for use in Anyscale workloads.

~> **Note:** When this resource is destroyed, it archives the underlying cluster environment. However, the Anyscale API does not currently support permanent deletion of container images. Archived images can be viewed by setting ` + "`include_archived = true`" + ` on the ` + "`anyscale_container_images`" + ` data source.`,

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the build (same as build_id).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// User-provided attributes
			"name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The name for the cluster environment that will be created to hold this image. If not specified, a name will be auto-generated.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image_uri": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The full URI of the Docker image to register (e.g., `docker.io/myrepo/image:v2` or `123456789.dkr.ecr.us-west-2.amazonaws.com/my-repo:latest`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ray_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The Ray version to associate with this image (e.g., `2.9.0`). If not specified, the latest available Ray version will be used.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"registry_login_secret": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "The name or identifier of a secret containing credentials to authenticate to the Docker registry hosting the image. Required for private registries.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// Computed attributes
			"build_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the build.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_environment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the cluster environment (app config) that holds this image.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"build_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the build (typically `succeeded` for registered images).",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the build was created.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_byod": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is a BYOD (Bring Your Own Docker) image. Always true for registered images.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"revision": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The revision number of the container image.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"name_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs.",
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *ContainerImageRegistryResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics, "Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *ContainerImageRegistryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ContainerImageRegistryResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Determine the ray version - use provided value or default to "2.44.0"
	rayVersion := "2.44.0"
	if !plan.RayVersion.IsNull() {
		rayVersion = plan.RayVersion.ValueString()
	}

	// Build BYOD cluster environment create request
	configJSON := CreateBYODClusterEnvironmentConfigJSON{
		DockerImage: plan.ImageURI.ValueString(),
		RayVersion:  rayVersion,
	}

	if !plan.RegistryLoginSecret.IsNull() {
		secret := plan.RegistryLoginSecret.ValueString()
		configJSON.RegistryLoginSecret = &secret
	}

	// Determine name - use provided value or generate a valid one from image URI
	// Name must match pattern: ^[A-Za-z0-9._-]+$
	var name string
	if !plan.Name.IsNull() && plan.Name.ValueString() != "" {
		name = plan.Name.ValueString()
	} else {
		// Sanitize image URI to create a valid name
		// Replace invalid characters (/, :, @) with hyphens
		name = sanitizeImageURIForName(plan.ImageURI.ValueString())
	}

	createReq := CreateBYODClusterEnvironmentRequest{
		Name:       name,
		ConfigJSON: configJSON,
		Anonymous:  false,
	}

	// Marshal request to JSON
	reqBody, err := MarshalRequestBody(createReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "container image registry request", err)
		return
	}

	tflog.Debug(ctx, "Registering container image via BYOD", map[string]any{
		"image_uri": plan.ImageURI.ValueString(),
		"name":      name,
	})

	// Create BYOD cluster environment
	clusterEnvResp, err := DoRequestAndParse[ClusterEnvironmentResponse](
		ctx,
		r.client,
		"POST",
		"/ext/v0/cluster_environments/byod",
		reqBody,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "register container image", err)
		return
	}

	clusterEnvID := clusterEnvResp.Result.ID
	clusterEnvName := clusterEnvResp.Result.Name

	tflog.Info(ctx, "BYOD cluster environment created", map[string]any{
		"cluster_environment_id": clusterEnvID,
		"name":                   clusterEnvName,
	})

	// Get the build ID by listing builds for this cluster environment
	buildsResp, err := DoRequestAndParse[ClusterEnvironmentBuildsListResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_environment_builds/?cluster_environment_id=%s&count=1&desc=true", clusterEnvID),
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "get build ID", err)
		return
	}
	if len(buildsResp.Results) == 0 {
		AddAPIError(&resp.Diagnostics, "get build ID", fmt.Errorf("no builds found for cluster environment"))
		return
	}
	buildID := buildsResp.Results[0].ID

	// Get build details
	buildResp, err := DoRequestAndParse[ClusterEnvironmentBuildResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_environment_builds/%s", buildID),
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "get build details", err)
		return
	}

	result := buildResp.Result
	tflog.Info(ctx, "Container image registered successfully", map[string]any{
		"build_id":               result.ID,
		"cluster_environment_id": result.ClusterEnvironmentID,
	})

	// Map response to model
	plan.ID = types.StringValue(result.ID)
	plan.BuildID = types.StringValue(result.ID)
	plan.ClusterEnvironmentID = types.StringValue(result.ClusterEnvironmentID)
	plan.BuildStatus = types.StringValue(result.Status)
	plan.CreatedAt = types.StringValue(result.CreatedAt)
	plan.IsBYOD = types.BoolValue(result.IsBYOD)
	plan.Revision = types.Int64Value(int64(result.Revision))

	// Set name_version
	if clusterEnvName != "" {
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", clusterEnvName, result.Revision))
	} else {
		plan.NameVersion = types.StringNull()
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *ContainerImageRegistryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ContainerImageRegistryResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	buildID := state.ID.ValueString()

	tflog.Debug(ctx, "Reading container image registry", map[string]any{"build_id": buildID})

	// Get build details
	// Note: The Anyscale API returns 201 for GET build endpoints
	buildResp, err := DoRequestAndParse[ClusterEnvironmentBuildResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_environment_builds/%s", buildID),
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			tflog.Warn(ctx, "Build not found, removing from state", map[string]any{"build_id": buildID})
			resp.State.RemoveResource(ctx)
			return
		}

		AddAPIError(&resp.Diagnostics, "read container image registry", err)
		return
	}

	result := buildResp.Result

	// Update state
	state.BuildID = types.StringValue(result.ID)
	state.ClusterEnvironmentID = types.StringValue(result.ClusterEnvironmentID)
	state.BuildStatus = types.StringValue(result.Status)
	state.CreatedAt = types.StringValue(result.CreatedAt)
	state.IsBYOD = types.BoolValue(result.IsBYOD)
	state.Revision = types.Int64Value(int64(result.Revision))

	// Get cluster environment name for name_version
	clusterEnvName := ""
	if !state.Name.IsNull() {
		clusterEnvName = state.Name.ValueString()
	} else {
		// Fetch the cluster environment to get its name
		clusterEnvResp, err := DoRequestAndParse[ClusterEnvironmentResponse](
			ctx,
			r.client,
			"GET",
			fmt.Sprintf("/ext/v0/cluster_environments/%s", result.ClusterEnvironmentID),
			nil,
			http.StatusOK,
		)
		if err == nil {
			clusterEnvName = clusterEnvResp.Result.Name
		}
	}
	if clusterEnvName != "" {
		state.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", clusterEnvName, result.Revision))
	} else {
		state.NameVersion = types.StringNull()
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *ContainerImageRegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All attributes require replacement, so Update should not be called
	AddConfigError(&resp.Diagnostics, "Update Not Supported",
		"Container image registry resources cannot be updated in-place. All changes require replacement.")
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *ContainerImageRegistryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ContainerImageRegistryResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterEnvID := state.ClusterEnvironmentID.ValueString()

	tflog.Info(ctx, "Archiving cluster environment for container image", map[string]any{
		"cluster_environment_id": clusterEnvID,
	})

	// Archive the cluster environment
	// Note: The /ext/v0/cluster_environments/ endpoint do not have DELETE, so we use POST /api/v2/application_templates/{id}/archive
	_, err := DoRequestRaw(
		ctx,
		r.client,
		"POST",
		fmt.Sprintf("/api/v2/application_templates/%s/archive", clusterEnvID),
		nil,
		http.StatusOK,
		http.StatusNoContent,
		http.StatusNotFound,
		http.StatusBadRequest,
	)
	if err != nil {
		// Check if already archived/deleted
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			tflog.Info(ctx, "Cluster environment already archived or deleted", map[string]any{
				"cluster_environment_id": clusterEnvID,
			})
			return
		}

		// Check if this is a default cluster environment that cannot be archived
		// This happens when using Anyscale's official images (e.g., anyscale/ray:*)
		if strings.Contains(err.Error(), "Cannot archive a default cluster environment") {
			tflog.Info(ctx, "Cluster environment is a default environment and cannot be archived (this is expected for Anyscale-provided images)", map[string]any{
				"cluster_environment_id": clusterEnvID,
			})
			return
		}

		AddAPIError(&resp.Diagnostics, "archive cluster environment", err)
		return
	}

	tflog.Info(ctx, "Cluster environment archived successfully", map[string]any{
		"cluster_environment_id": clusterEnvID,
	})
}

// ImportState imports the resource into Terraform state.
func (r *ContainerImageRegistryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by build ID
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// sanitizeImageURIForName converts an image URI to a valid cluster environment name.
// Names must match pattern: ^[A-Za-z0-9._-]+$
func sanitizeImageURIForName(imageURI string) string {
	// Replace common invalid characters with hyphens
	result := strings.ReplaceAll(imageURI, "/", "-")
	result = strings.ReplaceAll(result, ":", "-")
	result = strings.ReplaceAll(result, "@", "-")

	// Remove any remaining invalid characters
	var sanitized strings.Builder
	for _, r := range result {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			sanitized.WriteRune(r)
		}
	}

	return sanitized.String()
}
