package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Default build timeout
const defaultBuildTimeout = 30 * time.Minute

// Poll interval for build status
const buildPollInterval = 10 * time.Second

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                 = &ContainerImageBuildResource{}
	_ resource.ResourceWithConfigure    = &ContainerImageBuildResource{}
	_ resource.ResourceWithImportState  = &ContainerImageBuildResource{}
	_ resource.ResourceWithUpgradeState = &ContainerImageBuildResource{}
)

// NewContainerImageBuildResource creates a new container image build resource.
func NewContainerImageBuildResource() resource.Resource {
	return &ContainerImageBuildResource{}
}

// ContainerImageBuildResource defines the resource implementation.
type ContainerImageBuildResource struct {
	client *Client
}

// ContainerImageBuildResourceModel describes the resource data model.
type ContainerImageBuildResourceModel struct {
	// Identity - use cluster environment ID as the main resource ID
	ID types.String `tfsdk:"id"`

	// User-provided attributes
	Name              types.String   `tfsdk:"name"`               // Required
	Containerfile     types.String   `tfsdk:"containerfile"`      // Inline content (mutually exclusive with containerfile_path)
	ContainerfilePath types.String   `tfsdk:"containerfile_path"` // File path (mutually exclusive with containerfile)
	ProjectID         types.String   `tfsdk:"project_id"`         // Optional
	Timeouts          timeouts.Value `tfsdk:"timeouts"`

	// Computed attributes
	BuildID     types.String `tfsdk:"build_id"`
	BuildStatus types.String `tfsdk:"build_status"`
	ImageURI    types.String `tfsdk:"image_uri"`
	RayVersion  types.String `tfsdk:"ray_version"`
	Revision    types.Int64  `tfsdk:"revision"`
	Digest      types.String `tfsdk:"digest"`
	NameVersion types.String `tfsdk:"name_version"` // Formatted as "name:revision" for use with Anyscale APIs
	CreatedAt   types.String `tfsdk:"created_at"`
}

// Metadata returns the resource type name.
func (r *ContainerImageBuildResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_image_build"
}

// Schema defines the schema for the resource.
func (r *ContainerImageBuildResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: 1,
		MarkdownDescription: `Builds a container image from a Containerfile (Dockerfile). Use this resource to create custom container images for Anyscale workloads.

~> **Note:** When this resource is destroyed, it archives the underlying cluster environment. However, the Anyscale API does not currently support permanent deletion of container images. Archived images can be viewed by setting ` + "`include_archived = true`" + ` on the ` + "`anyscale_container_images`" + ` data source.`,

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the cluster environment.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// User-provided attributes
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name for the container image (cluster environment). Changing this replaces the resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"containerfile": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The content of the Containerfile (Dockerfile) to build. Mutually exclusive with `containerfile_path`. Updating this value triggers a new build revision.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(
						path.MatchRoot("containerfile"),
						path.MatchRoot("containerfile_path"),
					),
				},
			},
			"containerfile_path": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Path to the Containerfile (Dockerfile) to build. Mutually exclusive with `containerfile`. Updating this value triggers a new build revision.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The ID of the project to associate this container image with. Changing this replaces the resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// Computed attributes - these change when containerfile is updated
			"build_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the build. Changes when a new build is created.",
			},
			"build_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The current status of the build (`pending`, `in_progress`, `succeeded`, `failed`, `pending_cancellation`, `canceled`).",
			},
			"image_uri": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The URI of the built container image.",
			},
			"ray_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Ray version used in the build.",
			},
			"revision": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The revision number of the container image build. Increments with each new build.",
			},
			"digest": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The content digest of the built container image (e.g. `sha256:...`). May occasionally be briefly empty immediately after creation, or after an update that triggers a new build, if the build is still settling.",
			},
			"name_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the build was created. Changes when a new build is created.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create:            true,
				Update:            true,
				CreateDescription: "Maximum time to wait for the initial build to complete (e.g. `30m`, `1h`). Defaults to `30m`. Purely local to this provider - never sent to or read from the Anyscale API.",
				UpdateDescription: "Maximum time to wait for a new build triggered by a `containerfile`/`containerfile_path` change to complete. Same default as `create`. Not consulted when an update changes only this `timeouts` block itself (no new build is triggered in that case).",
			}),
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *ContainerImageBuildResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// populateBuildFields maps a completed BuildResult into the model's build-derived fields.
// Shared by Create and Update, which perform this identical mapping once their respective
// build finishes.
func populateBuildFields(model *ContainerImageBuildResourceModel, build *BuildResult) {
	model.BuildID = types.StringValue(build.ID)
	model.BuildStatus = types.StringValue(build.Status)
	model.CreatedAt = types.StringValue(build.CreatedAt)
	model.ImageURI = types.StringPointerValue(build.DockerImageName)
	model.RayVersion = types.StringPointerValue(build.RayVersion)
	model.Revision = types.Int64Value(int64(build.Revision))
	model.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", model.Name.ValueString(), build.Revision))
	model.Digest = types.StringPointerValue(build.Digest)
}

// Create creates the resource and sets the initial Terraform state.
func (r *ContainerImageBuildResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ContainerImageBuildResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve containerfile content
	containerfileContent, err := r.resolveContainerfile(&plan)
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Containerfile Configuration", err.Error())
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultBuildTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build create request
	createReq := CreateApplicationTemplateRequest{
		Name:          plan.Name.ValueString(),
		Containerfile: containerfileContent,
	}

	if !plan.ProjectID.IsNull() {
		projectID := plan.ProjectID.ValueString()
		createReq.ProjectID = &projectID
	}

	// Marshal request to JSON
	reqBody, err := MarshalRequestBody(createReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "container image build request", err)
		return
	}

	tflog.Debug(ctx, "Creating container image build", map[string]any{
		"name":    createReq.Name,
		"timeout": timeout.String(),
	})

	// Create the application template (which triggers a build)
	templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
		ctx,
		r.client,
		"POST",
		"/api/v2/application_templates/",
		reqBody,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "create container image build", err)
		return
	}

	result := templateResp.Result
	templateID := result.ID

	tflog.Info(ctx, "Application template created, waiting for build", map[string]any{
		"cluster_environment_id": templateID,
		"name":                   result.Name,
	})

	// Set the application template ID immediately
	plan.ID = types.StringValue(templateID)

	// Resolve the build the create just triggered. The create response is bare (no
	// latest_build), so this re-fetches the template in its decorated form.
	buildID, err := r.getLatestBuildID(ctx, templateID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "get build ID", err)
		return
	}

	tflog.Debug(ctx, "Found build ID", map[string]any{
		"build_id":               buildID,
		"cluster_environment_id": templateID,
	})

	plan.BuildID = types.StringValue(buildID)

	// Persist state now that the cluster environment exists remotely, before
	// waiting on the (potentially long-running) build. Without this, a build
	// timeout/failure below would leave the cluster environment orphaned in
	// the backend with no Terraform record to destroy it.
	for _, computed := range []*types.String{&plan.BuildStatus, &plan.ImageURI, &plan.RayVersion, &plan.NameVersion, &plan.CreatedAt, &plan.Digest} {
		if computed.IsUnknown() {
			*computed = types.StringNull()
		}
	}
	if plan.Revision.IsUnknown() {
		plan.Revision = types.Int64Value(0)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Wait for build to complete
	build, err := r.waitForBuild(ctx, buildID, timeout)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for build", err)
		return
	}

	// The backend can report a build as "succeeded" slightly before its digest is
	// populated (see waitForBuildDigest) - wait for it to settle so Create reliably
	// returns a non-null digest in the common case, rather than depending on a later
	// refresh to fill it in.
	build, digestSettled := waitForBuildDigest(ctx, r.client, build)
	if !digestSettled {
		AddDigestNotSettledWarning(&resp.Diagnostics, build.ID)
	}

	tflog.Info(ctx, "Container image build completed", map[string]any{
		"build_id":               buildID,
		"cluster_environment_id": templateID,
		"status":                 build.Status,
	})

	// Map response to model
	populateBuildFields(&plan, build)

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *ContainerImageBuildResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ContainerImageBuildResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	templateID := state.ID.ValueString()

	tflog.Debug(ctx, "Reading container image build", map[string]any{"cluster_environment_id": templateID})

	// Get application template details (decorated: carries latest_build for free)
	template, err := r.getApplicationTemplate(ctx, templateID)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			tflog.Warn(ctx, "Application template not found, removing from state", map[string]any{"cluster_environment_id": templateID})
			resp.State.RemoveResource(ctx)
			return
		}

		AddAPIError(&resp.Diagnostics, "read application template", err)
		return
	}

	// Check if archived
	if template.IsArchived() {
		tflog.Warn(ctx, "Application template is archived, removing from state", map[string]any{"cluster_environment_id": templateID})
		resp.State.RemoveResource(ctx)
		return
	}

	// Update name from application template (important for import)
	state.Name = types.StringValue(template.Name)

	// Get the build ID from state, falling back to the template's own latest_build
	// reference (already fetched above, no extra call needed).
	var buildID string
	if !state.BuildID.IsNull() {
		buildID = state.BuildID.ValueString()
	} else if template.LatestBuild != nil {
		buildID = template.LatestBuild.ID
	} else {
		tflog.Warn(ctx, "Application template has no builds", map[string]any{"cluster_environment_id": templateID})
	}

	// If we have a build ID, get build details
	if buildID != "" {
		build, err := r.getBuild(ctx, buildID)
		if err != nil {
			tflog.Warn(ctx, "Failed to get build details", map[string]any{
				"build_id": buildID,
				"error":    err.Error(),
			})
		} else {
			state.BuildID = types.StringValue(build.ID)
			state.BuildStatus = types.StringValue(build.Status)
			state.CreatedAt = types.StringValue(build.CreatedAt)

			if build.DockerImageName != nil {
				state.ImageURI = types.StringValue(*build.DockerImageName)
			}

			if build.RayVersion != nil {
				state.RayVersion = types.StringValue(*build.RayVersion)
			}

			// Set revision and name_version
			state.Revision = types.Int64Value(int64(build.Revision))
			state.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", template.Name, build.Revision))

			if build.Digest != nil {
				state.Digest = types.StringValue(*build.Digest)
			}
		}
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
// When the containerfile changes, a new build is created for the cluster environment.
func (r *ContainerImageBuildResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ContainerImageBuildResourceModel
	var state ContainerImageBuildResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Check if containerfile has changed
	containerfileChanged := !plan.Containerfile.Equal(state.Containerfile) || !plan.ContainerfilePath.Equal(state.ContainerfilePath)

	// If containerfile hasn't changed, skip triggering a new build (timeouts may have
	// changed) - containerfileChanged deliberately never compares Timeouts (same as it
	// never compared the old flat BuildTimeout), so a timeouts-only change never triggers
	// a new build.
	//
	// Still need a fresh GET to populate the computed outputs before persisting (mirrors
	// resource_service.go's H2/H5 handling of the same shape): every Computed attribute
	// here has no UseStateForUnknown plan modifier, so all of them (build_status,
	// created_at, digest, image_uri, name_version, ray_version, revision) are Unknown in
	// this plan once anything triggers Update at all - confirmed by a real acceptance run
	// failing with "provider produced invalid result" before this fix, not assumed. This
	// bug pre-dates PR2 (any build_timeout-only change would have hit it too - nothing
	// ever tested that path before), surfaced by the PR2 test that closes exactly that
	// coverage gap.
	if !containerfileChanged {
		build, err := r.getBuild(ctx, state.BuildID.ValueString())
		if err != nil {
			AddAPIError(&resp.Diagnostics, "read build", err)
			return
		}
		populateBuildFields(&plan, build)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	tflog.Info(ctx, "Containerfile changed, triggering new build", map[string]any{
		"cluster_environment_id": state.ID.ValueString(),
	})

	// Resolve containerfile content
	containerfileContent, err := r.resolveContainerfile(&plan)
	if err != nil {
		AddConfigError(&resp.Diagnostics, "Invalid Containerfile Configuration", err.Error())
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultBuildTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	templateID := state.ID.ValueString()

	// Create a new build for the existing application template
	createBuildReq := CreateBuildRequest{
		ApplicationTemplateID: templateID,
		Containerfile:         containerfileContent,
	}

	reqBody, err := MarshalRequestBody(createBuildReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "create build request", err)
		return
	}

	// POST to create new build
	buildResp, err := DoRequestAndParse[BuildResponse](
		ctx,
		r.client,
		"POST",
		"/api/v2/builds/",
		reqBody,
		http.StatusOK,
		http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "create new build", err)
		return
	}

	buildID := buildResp.Result.ID

	tflog.Info(ctx, "New build created, waiting for completion", map[string]any{
		"build_id":               buildID,
		"cluster_environment_id": templateID,
	})

	// Wait for build to complete
	build, err := r.waitForBuild(ctx, buildID, timeout)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for build", err)
		return
	}

	// See the matching comment in Create: wait for the digest to settle rather than
	// depending on a later refresh to fill it in.
	build, digestSettled := waitForBuildDigest(ctx, r.client, build)
	if !digestSettled {
		AddDigestNotSettledWarning(&resp.Diagnostics, build.ID)
	}

	tflog.Info(ctx, "Container image build completed", map[string]any{
		"build_id":               buildID,
		"cluster_environment_id": templateID,
		"status":                 build.Status,
		"revision":               build.Revision,
	})

	// Preserve the ID from state
	plan.ID = state.ID

	// Map response to model
	populateBuildFields(&plan, build)

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *ContainerImageBuildResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ContainerImageBuildResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterEnvID := state.ID.ValueString()

	archiveClusterEnvironment(ctx, r.client, clusterEnvID, &resp.Diagnostics)
}

// ImportState imports the resource into Terraform state.
func (r *ContainerImageBuildResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by cluster environment ID
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// Helper functions

// resolveContainerfile resolves the containerfile content from either inline or file path.
func (r *ContainerImageBuildResource) resolveContainerfile(plan *ContainerImageBuildResourceModel) (string, error) {
	// Exactly one must be set (enforced by schema validator)
	if !plan.Containerfile.IsNull() && plan.Containerfile.ValueString() != "" {
		return plan.Containerfile.ValueString(), nil
	}

	if !plan.ContainerfilePath.IsNull() && plan.ContainerfilePath.ValueString() != "" {
		filePath := plan.ContainerfilePath.ValueString()
		content, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to read containerfile from %s: %w", filePath, err)
		}
		return string(content), nil
	}

	return "", fmt.Errorf("either containerfile or containerfile_path must be specified")
}

// getLatestBuildID resolves the latest build ID for an application template
// contract-based, via the template's own latest_build reference. A bare create
// response never carries latest_build, so this always re-fetches the decorated
// template rather than trusting a builds-list call's ordering.
func (r *ContainerImageBuildResource) getLatestBuildID(ctx context.Context, templateID string) (string, error) {
	template, err := r.getApplicationTemplate(ctx, templateID)
	if err != nil {
		return "", err
	}

	if template.LatestBuild == nil {
		return "", fmt.Errorf("no builds found for application template %s", templateID)
	}

	return template.LatestBuild.ID, nil
}

// getApplicationTemplate fetches the decorated application template by ID.
func (r *ContainerImageBuildResource) getApplicationTemplate(ctx context.Context, templateID string) (*ApplicationTemplateResult, error) {
	templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/api/v2/application_templates/%s", templateID),
		nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get application template %s: %w", templateID, err)
	}

	return &templateResp.Result, nil
}

// waitForBuild polls the build status until it reaches a terminal state.
func (r *ContainerImageBuildResource) waitForBuild(ctx context.Context, buildID string, timeout time.Duration) (*BuildResult, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		build, err := r.getBuild(ctx, buildID)
		if err != nil {
			return nil, err
		}

		tflog.Debug(ctx, "Build status check", map[string]any{
			"build_id": buildID,
			"status":   build.Status,
		})

		done, statusErr := evaluateBuildStatus(build)
		if statusErr != nil {
			return nil, statusErr
		}
		if done {
			return build, nil
		}
		time.Sleep(buildPollInterval)
	}

	return nil, fmt.Errorf("build timed out after %v", timeout)
}

// evaluateBuildStatus classifies a build's current status into a terminal outcome or an
// in-progress state that should keep polling. done is true once no further polling is useful;
// err is set for a terminal failure/cancellation, nil for terminal success or while in progress.
//
// The backend's real wire value for a cancelled build is "canceled" (one L, per the
// BuildStatus/ClusterEnvironmentBuildStatus enums). "cancelled" (two L) is also accepted here
// defensively so an unexpected respelling never falls through to the unknown-status error.
func evaluateBuildStatus(build *BuildResult) (done bool, err error) {
	switch build.Status {
	case "succeeded":
		return true, nil
	case "failed":
		if build.ErrorMessage != nil && *build.ErrorMessage != "" {
			return true, fmt.Errorf("build failed: %s", *build.ErrorMessage)
		}
		return true, fmt.Errorf("build failed")
	case "canceled", "cancelled":
		return true, fmt.Errorf("build was cancelled")
	case "pending", "in_progress", "pending_cancellation":
		return false, nil
	default:
		return true, fmt.Errorf("unknown build status: %s", build.Status)
	}
}

// getBuild fetches the current build details.
func (r *ContainerImageBuildResource) getBuild(ctx context.Context, buildID string) (*BuildResult, error) {
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
		return nil, fmt.Errorf("failed to get build %s: %w", buildID, err)
	}

	return &buildResp.Result, nil
}
