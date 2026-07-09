package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

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
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
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
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
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

	configJSON := CreateBYODApplicationTemplateConfigJSON{
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
		// Add timestamp suffix to ensure uniqueness
		baseName := sanitizeImageURIForName(plan.ImageURI.ValueString())
		timestamp := time.Now().UnixNano()
		name = fmt.Sprintf("%s-%d", baseName, timestamp)
	}

	templateReq := CreateBYODApplicationTemplateRequest{
		Name:       name,
		ConfigJSON: configJSON,
		Anonymous:  false,
	}

	templateReqBody, err := MarshalRequestBody(templateReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "container image registry request", err)
		return
	}

	tflog.Debug(ctx, "Registering container image via BYOD", map[string]any{
		"image_uri": plan.ImageURI.ValueString(),
		"name":      name,
	})

	// Call 1 of 2: create the application template. Unlike the old atomic
	// /ext/v0/cluster_environments/byod endpoint (a single DB transaction), api/v2
	// has no combined template+build BYOD endpoint - the build is created
	// separately below, which opens a partial-failure window that call 1 alone
	// never had.
	templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
		ctx,
		r.client,
		"POST",
		"/api/v2/application_templates/byod",
		templateReqBody,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "register container image", err)
		return
	}

	templateID := templateResp.Result.ID
	templateName := templateResp.Result.Name

	tflog.Info(ctx, "BYOD application template created", map[string]any{
		"cluster_environment_id": templateID,
		"name":                   templateName,
	})

	// Persist state now that the template exists remotely, before the build-create
	// call below that can still fail. Delete() acts on ClusterEnvironmentID, so that
	// (not just ID) must be recorded here. Without this, a call-2 failure would leave
	// the template orphaned in the backend with no Terraform record to archive it -
	// the 2-call split widens the window the old atomic call never had, so this early
	// write (already used below for the build wait) is now essential rather than
	// optional. id is set to the template id only for this transient window; once the
	// build succeeds below, id is overwritten with the build id to keep this
	// migration behavior-neutral on the happy path. Read() below tolerates a null
	// BuildID so a resource left in this partial state survives a refresh instead of
	// being mistaken for deleted (see GATE test: call-2 fails -> state holds the
	// template -> Delete archives it -> no orphan).
	plan.ID = types.StringValue(templateID)
	plan.ClusterEnvironmentID = types.StringValue(templateID)
	plan.BuildID = types.StringNull()
	plan.BuildStatus = types.StringNull()
	plan.CreatedAt = types.StringNull()
	plan.IsBYOD = types.BoolValue(true)
	plan.Revision = types.Int64Value(0)
	plan.NameVersion = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Call 2 of 2: create the build from the registered image.
	buildReq := CreateBYODBuildRequest{
		ApplicationTemplateID: templateID,
		ConfigJSON: CreateBYODAppConfigConfigJSON{
			DockerImage:         plan.ImageURI.ValueString(),
			RayVersion:          rayVersion,
			RegistryLoginSecret: configJSON.RegistryLoginSecret,
		},
	}

	buildReqBody, err := MarshalRequestBody(buildReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "container image registry build request", err)
		return
	}

	buildResp, err := DoRequestAndParse[BuildResponse](
		ctx,
		r.client,
		"POST",
		"/api/v2/builds/byod",
		buildReqBody,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "register container image build", err)
		return
	}

	result := buildResp.Result
	tflog.Info(ctx, "Container image registered successfully", map[string]any{
		"build_id":               result.ID,
		"cluster_environment_id": result.ApplicationTemplateID,
	})

	// Map response to model
	plan.ID = types.StringValue(result.ID)
	plan.BuildID = types.StringValue(result.ID)
	plan.ClusterEnvironmentID = types.StringValue(result.ApplicationTemplateID)
	plan.BuildStatus = types.StringValue(result.Status)
	plan.CreatedAt = types.StringValue(result.CreatedAt)
	plan.IsBYOD = types.BoolValue(result.IsBYOD)
	plan.Revision = types.Int64Value(int64(result.Revision))

	// Set name_version
	if templateName != "" {
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", templateName, result.Revision))
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

	// A Create() that failed between the two BYOD calls (see the defensive State.Set
	// there) leaves a template with no build yet. There is no build to fetch in that
	// case; confirm the template itself is still live and otherwise leave state
	// untouched, rather than treating the missing build as evidence the whole
	// resource was deleted (which would silently re-orphan the template on the very
	// next refresh).
	if state.BuildID.IsNull() || state.BuildID.ValueString() == "" {
		clusterEnvID := state.ClusterEnvironmentID.ValueString()
		tflog.Debug(ctx, "Reading container image registry with no build yet", map[string]any{"cluster_environment_id": clusterEnvID})

		_, err := DoRequestAndParse[ApplicationTemplateResponse](
			ctx,
			r.client,
			"GET",
			fmt.Sprintf("/api/v2/application_templates/%s", clusterEnvID),
			nil,
			http.StatusOK,
		)
		if err != nil {
			if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
				tflog.Warn(ctx, "Cluster environment not found, removing from state", map[string]any{"cluster_environment_id": clusterEnvID})
				resp.State.RemoveResource(ctx)
				return
			}

			AddAPIError(&resp.Diagnostics, "read container image registry", err)
			return
		}

		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	buildID := state.ID.ValueString()

	tflog.Debug(ctx, "Reading container image registry", map[string]any{"build_id": buildID})

	// Get build details
	// Note: The Anyscale API returns 201 for GET build endpoints
	buildResp, err := DoRequestAndParse[BuildResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/api/v2/builds/%s", buildID),
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
	state.ClusterEnvironmentID = types.StringValue(result.ApplicationTemplateID)
	state.BuildStatus = types.StringValue(result.Status)
	state.CreatedAt = types.StringValue(result.CreatedAt)
	state.IsBYOD = types.BoolValue(result.IsBYOD)
	state.Revision = types.Int64Value(int64(result.Revision))

	// image_uri is Required in the schema, so the user always supplied a value
	// that the API now echoes back as docker_image_name. Refresh from the API
	// to keep state in sync if the canonical URI differs from the user's input.
	if result.DockerImageName != nil {
		state.ImageURI = types.StringValue(*result.DockerImageName)
	}
	// ray_version is Optional-only; only overwrite when the user actually set it
	// in their config so we don't introduce drift on plans that omit the field.
	if result.RayVersion != nil && !state.RayVersion.IsNull() {
		state.RayVersion = types.StringValue(*result.RayVersion)
	}

	// Get cluster environment name for name_version. We deliberately do NOT
	// overwrite state.Name from the API: name is Optional-only in the schema,
	// so populating it from the API when the user left it unset would cause
	// drift on a subsequent plan.
	clusterEnvName := ""
	if !state.Name.IsNull() {
		clusterEnvName = state.Name.ValueString()
	} else {
		templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
			ctx,
			r.client,
			"GET",
			fmt.Sprintf("/api/v2/application_templates/%s", result.ApplicationTemplateID),
			nil,
			http.StatusOK,
		)
		if err == nil {
			clusterEnvName = templateResp.Result.Name
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
