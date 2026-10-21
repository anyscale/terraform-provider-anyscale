package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.ResourceWithModifyPlan  = &ContainerImageRegistryResource{}
)

// defaultBYODRayVersion is sent to the BYOD create endpoints when the user omits
// ray_version. Both endpoints require it as a non-empty string, matched exactly against a
// fixed table of supported base images server-side - they never inspect the docker image
// itself - so some concrete, currently-supported value must always be sent.
const defaultBYODRayVersion = "2.44.0"

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
	// Identity - id is the cluster environment ID (stable for the resource's
	// lifetime; a registry's build can be superseded by a new latest build
	// without the resource itself being replaced, so the build ID cannot
	// serve as identity - see UpgradeState in
	// resource_container_image_registry_upgrade.go for the v0->v1 migration
	// off the old build-ID identity).
	ID types.String `tfsdk:"id"`

	// User-provided attributes
	Name                types.String `tfsdk:"name"`                  // Optional - cluster env name
	ImageURI            types.String `tfsdk:"image_uri"`             // Required
	RayVersion          types.String `tfsdk:"ray_version"`           // Optional
	RegistryLoginSecret types.String `tfsdk:"registry_login_secret"` // Optional, sensitive

	// Computed attributes
	BuildID     types.String `tfsdk:"build_id"`
	BuildStatus types.String `tfsdk:"build_status"`
	CreatedAt   types.String `tfsdk:"created_at"`
	IsBYOD      types.Bool   `tfsdk:"is_byod"`
	Revision    types.Int64  `tfsdk:"revision"`
	Digest      types.String `tfsdk:"digest"`
	NameVersion types.String `tfsdk:"name_version"` // Formatted as "name:revision" for use with Anyscale APIs
}

// Metadata returns the resource type name.
func (r *ContainerImageRegistryResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_image_registry"
}

// Schema defines the schema for the resource.
func (r *ContainerImageRegistryResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: 1,

		MarkdownDescription: `Registers an existing Docker container image with Anyscale. Use this resource to make external container images (from ECR, Docker Hub, or other registries) available for use in Anyscale workloads. Changing ` + "`image_uri`" + ` registers a new build in place; changing any other argument replaces the resource.

~> **Note:** When this resource is destroyed, it archives the underlying cluster environment. However, the Anyscale API does not currently support permanent deletion of container images. Archived images can be viewed by setting ` + "`include_archived = true`" + ` on the ` + "`anyscale_container_images`" + ` data source.`,

		Attributes: containerImageRegistryAttributes(),
	}
}

// containerImageRegistryAttributes returns the v1 (current) attribute map. It must NOT be
// reused as the v0 PriorSchema in UpgradeState: v1 added the digest attribute and
// removed cluster_environment_id, so v0's real on-disk state has digest
// absent and cluster_environment_id present - the opposite of this function - and id's
// MarkdownDescription here describes the current (v1) meaning, not what it meant under v0.
// See containerImageRegistrySchemaV0 in resource_container_image_registry_upgrade.go for
// the frozen v0 snapshot used to decode prior state.
func containerImageRegistryAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The unique identifier of the cluster environment holding this image. Earlier provider versions used the build ID here instead; existing state is migrated automatically, but any tooling that stored the old build-id value out of band (e.g. a `terraform output`) must use `id` going forward.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},

		// User-provided attributes
		"name": schema.StringAttribute{
			Optional:            true,
			Computed:            true,
			MarkdownDescription: "The name for the cluster environment that will be created to hold this image. If not specified, a name will be auto-generated and recorded here after create/import - the generated value is not derivable from config (it embeds a timestamp), so it lives only in state.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				stringplanmodifier.RequiresReplace(),
			},
		},
		"image_uri": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "The full URI of the Docker image to register (e.g., `docker.io/myrepo/image:v2` or `123456789.dkr.ecr.us-west-2.amazonaws.com/my-repo:latest`). Changing it registers a new build on the same cluster environment in place, incrementing `revision`.",
		},
		"ray_version": schema.StringAttribute{
			Optional:            true,
			Computed:            true,
			MarkdownDescription: "The Ray version to associate with this image (e.g., `2.9.0`). Must be a Ray version Anyscale has a build image for; the API rejects unsupported values at creation time. If not specified, a supported default is used automatically.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				stringplanmodifier.RequiresReplace(),
			},
		},
		"registry_login_secret": schema.StringAttribute{
			Optional:            true,
			Sensitive:           true,
			MarkdownDescription: "The name or identifier of a secret containing credentials to authenticate to the Docker registry hosting the image. Required for private registries. Changing it replaces the image, except the first value set after `terraform import`, which updates in place (see [Importing an existing image](../guides/container-images.md#importing-an-existing-container-image)).",
			PlanModifiers: []planmodifier.String{
				RequiresReplaceUnlessUnrecoverable("registry_login_secret", "container image"),
			},
		},

		// Computed attributes
		"build_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The unique identifier of the latest build for this image.",
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
		"digest": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The content digest of this image's current latest successful build (e.g. `sha256:...`). May occasionally be briefly empty immediately after creation if the build is still settling. Like `build_id`, `revision`, and `name_version`, it reflects whichever build is currently latest for the underlying cluster environment, and can change on a later refresh if a new build supersedes this one outside this resource's own apply.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"name_version": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
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

	// ray_version is a required field on the BYOD create call below - the backend
	// looks it up against a fixed table of supported base images and returns HTTP 500
	// if nothing matches (it never inspects the docker image itself), so some concrete,
	// currently-supported value must always be sent even when the user omits the
	// attribute. defaultBYODRayVersion is that fallback; it is not necessarily what ends
	// up in state, since state is populated from what the API echoes back (see the
	// ResolvedRayVersion call in applyRegistryBuild).
	rayVersion := defaultBYODRayVersion
	if !plan.RayVersion.IsNull() && !plan.RayVersion.IsUnknown() && plan.RayVersion.ValueString() != "" {
		rayVersion = plan.RayVersion.ValueString()
	}

	configJSON := CreateBYODApplicationTemplateConfigJSON{
		DockerImage: plan.ImageURI.ValueString(),
		RayVersion:  rayVersion,
	}

	configJSON.RegistryLoginSecret = plan.RegistryLoginSecret.ValueStringPointer()

	// Determine name - use provided value or generate a valid one from image URI
	// Name must match pattern: ^[A-Za-z0-9._-]+$
	// name is now Optional+Computed: omitted in config plans Unknown, not Null (there is no
	// prior state for UseStateForUnknown to carry forward on a fresh Create). The explicit
	// IsUnknown() check is defensive/self-documenting rather than load-bearing on its own here -
	// ValueString() on Unknown returns "" (confirmed, not assumed), which the pre-existing
	// != "" check already routes to the generate-name branch below - but relying on that
	// coincidence without saying so is exactly what reads as a hidden bug to the next person who
	// has to re-derive it. Matches ray_version's identical guard just below in this file.
	var name string
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() && plan.Name.ValueString() != "" {
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

	// Create the application template. The backend creates the cluster environment
	// and its first build from config_json in one transaction, so no separate build
	// request follows; the build is read back below.
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

	// name is Optional+Computed and RequiresReplace (immutable): only fill it from the API's
	// response when the practitioner omitted it from config (plan.Name is Unknown here, not Null,
	// same as ray_version's guard below). The template response finalizes the name, so this can
	// fill immediately rather than waiting for the build.
	if plan.Name.IsUnknown() {
		plan.Name = types.StringValue(templateName)
	}

	// Persist state now that the template exists remotely, before reading back its
	// build. Delete() acts on ID (the cluster environment id), so it must be recorded
	// here: if anything below fails, the resource stays in state and a later Delete
	// archives the template - no orphan. id is the cluster environment id permanently
	// (see the identity comment on ContainerImageRegistryResourceModel.ID). The build
	// attributes are null until the build is read; Read() tolerates that and fills
	// them on the next refresh. ray_version and digest are null here too: an unknown
	// value may not be saved after apply.
	plan.ID = types.StringValue(templateID)
	partial := plan
	partial.BuildID = types.StringNull()
	partial.BuildStatus = types.StringNull()
	partial.CreatedAt = types.StringNull()
	partial.IsBYOD = types.BoolValue(true)
	partial.Revision = types.Int64Null()
	partial.NameVersion = types.StringNull()
	partial.Digest = types.StringNull()
	if partial.RayVersion.IsUnknown() {
		partial.RayVersion = types.StringNull()
	}
	MarkCreatedByTerraform(ctx, resp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read back the build the template call created. Every failure from here on is a
	// warning, not an error: an error would taint the resource, and the replace the next
	// apply plans would archive the environment and then fail to recreate it under the
	// same name (archived names stay taken). The partial state above is kept instead.
	build, err := r.getTemplateLatestBuild(ctx, templateID)
	if err != nil || build == nil {
		addBuildNotFoundYetWarning(&resp.Diagnostics, err)
		return
	}
	settledBuild, digestSettled := waitForBuildDigest(ctx, r.client, build)
	if !digestSettled {
		AddDigestNotSettledWarning(&resp.Diagnostics, settledBuild.ID)
	}
	applyRegistryBuild(&plan, settledBuild, templateName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// getTemplateLatestBuild returns the latest build of cluster environment templateID:
// GET the template, then GET its latest_build. It returns (nil, nil) when the template
// has no build. Read resolves the build the same way, through getBuild.
func (r *ContainerImageRegistryResource) getTemplateLatestBuild(ctx context.Context, templateID string) (*BuildResult, error) {
	templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/api/v2/application_templates/%s", templateID),
		nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	if templateResp.Result.LatestBuild == nil {
		return nil, nil
	}
	return r.getBuild(ctx, templateResp.Result.LatestBuild.ID)
}

// getBuild fetches one build. The Anyscale API returns 201 for GET build endpoints.
func (r *ContainerImageRegistryResource) getBuild(ctx context.Context, buildID string) (*BuildResult, error) {
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
		return nil, err
	}
	return &buildResp.Result, nil
}

// addBuildNotFoundYetWarning reports a registered image whose build Create could not
// read back. err is nil when the template has no build yet.
func addBuildNotFoundYetWarning(diags *diag.Diagnostics, err error) {
	detail := "Container image registered, but its build was not found yet. Its build attributes " +
		"will be filled on the next refresh; run terraform apply -refresh-only, or a later plan, " +
		"to update them."
	if err != nil {
		detail += " Last error: " + err.Error()
	}
	diags.AddWarning("Container Image Build Not Found Yet", detail)
}

// createBuild registers plan.ImageURI as a new build of the cluster environment plan.ID
// and maps the build onto plan. Update uses it to register a changed image_uri.
func (r *ContainerImageRegistryResource) createBuild(ctx context.Context, plan *ContainerImageRegistryResourceModel, templateName, rayVersion string, diags *diag.Diagnostics) bool {
	buildReq := CreateBYODBuildRequest{
		ApplicationTemplateID: plan.ID.ValueString(),
		ConfigJSON: CreateBYODAppConfigConfigJSON{
			DockerImage:         plan.ImageURI.ValueString(),
			RayVersion:          rayVersion,
			RegistryLoginSecret: plan.RegistryLoginSecret.ValueStringPointer(),
		},
	}

	buildReqBody, err := MarshalRequestBody(buildReq)
	if err != nil {
		AddJSONError(diags, "marshal", "container image registry build request", err)
		return false
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
		AddAPIError(diags, "register container image build", err)
		return false
	}

	result := buildResp.Result
	tflog.Info(ctx, "Container image registered successfully", map[string]any{
		"build_id":               result.ID,
		"cluster_environment_id": result.ApplicationTemplateID,
	})

	// Unlike the build resource, this Create has no pre-existing poll loop to extend -
	// the single synchronous response above is mapped straight into state today, which
	// leaves this resource more exposed to the backend's succeeded-before-digest-settles
	// race (see waitForBuildDigest) than the build resource's Create. Wait here so this
	// resource reliably gets a non-null digest too.
	settledBuild, digestSettled := waitForBuildDigest(ctx, r.client, &result)
	if !digestSettled {
		AddDigestNotSettledWarning(diags, settledBuild.ID)
	}
	applyRegistryBuild(plan, settledBuild, templateName)
	return true
}

// applyRegistryBuild maps a build onto plan, for Create and Update. id is not touched
// here - it stays the cluster environment id for the resource's entire lifetime (see
// the identity comment on ContainerImageRegistryResourceModel.ID).
func applyRegistryBuild(plan *ContainerImageRegistryResourceModel, result *BuildResult, templateName string) {
	plan.BuildID = types.StringValue(result.ID)
	plan.BuildStatus = types.StringValue(result.Status)
	plan.CreatedAt = types.StringValue(result.CreatedAt)
	plan.IsBYOD = types.BoolValue(result.IsBYOD)
	plan.Revision = types.Int64Value(int64(result.Revision))

	plan.Digest = types.StringPointerValue(result.Digest)

	// ray_version is Optional+Computed and RequiresReplace (immutable): only fill it
	// from the API's resolved value when the user omitted it from config (plan.RayVersion
	// is Unknown here, not Null, since a Computed attribute plans Unknown absent a prior
	// state value). A user-set value must be preserved verbatim rather than reconciled
	// with what the API echoes back: byod_ray_version is parsed from the docker image
	// tag itself, not derived from this field, so it can resolve to something entirely
	// unrelated to what the user typed (see ResolvedRayVersion's doc comment) -
	// overwriting here would leave config permanently mismatched against state, and
	// RequiresReplace turns every such mismatch into a replace loop.
	if plan.RayVersion.IsUnknown() {
		if resolvedRayVersion := result.ResolvedRayVersion(); resolvedRayVersion != nil {
			plan.RayVersion = types.StringValue(*resolvedRayVersion)
		} else {
			plan.RayVersion = types.StringNull()
		}
	}

	// Set name_version
	if templateName != "" {
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", templateName, result.Revision))
	} else {
		plan.NameVersion = types.StringNull()
	}
}

// ModifyPlan marks the build-derived attributes unknown when image_uri changes, since
// Update then registers a new build.
func (r *ContainerImageRegistryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state ContainerImageRegistryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.ImageURI.Equal(state.ImageURI) {
		return
	}

	plan.BuildID = types.StringUnknown()
	plan.BuildStatus = types.StringUnknown()
	plan.CreatedAt = types.StringUnknown()
	plan.Revision = types.Int64Unknown()
	plan.Digest = types.StringUnknown()
	plan.NameVersion = types.StringUnknown()
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *ContainerImageRegistryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ContainerImageRegistryResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterEnvID := state.ID.ValueString()

	tflog.Debug(ctx, "Reading container image registry", map[string]any{"cluster_environment_id": clusterEnvID})

	// Get application template details (decorated: carries latest_build for free)
	templateResp, err := DoRequestAndParse[ApplicationTemplateResponse](
		ctx,
		r.client,
		"GET",
		fmt.Sprintf("/api/v2/application_templates/%s", clusterEnvID),
		nil,
		http.StatusOK,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			tflog.Warn(ctx, "Cluster environment not found, removing from state", map[string]any{"cluster_environment_id": clusterEnvID})
			resp.State.RemoveResource(ctx)
			return
		}

		AddAPIError(&resp.Diagnostics, "read container image registry", err)
		return
	}
	template := templateResp.Result

	if template.IsArchived() {
		tflog.Warn(ctx, "Cluster environment is archived, removing from state", map[string]any{"cluster_environment_id": clusterEnvID})
		resp.State.RemoveResource(ctx)
		return
	}

	// A Create() that could not read back its build (see the partial State.Set there),
	// or a resource from a provider version that created the build separately and
	// failed in between, can leave a template with no build - template.LatestBuild is
	// nil in that case. Leave the build-derived attributes as they already are (null, from
	// that same defensive write) rather than treating the missing build as evidence
	// the whole resource was deleted, which would silently re-orphan the template on
	// the very next refresh.
	if template.LatestBuild == nil {
		tflog.Warn(ctx, "Cluster environment has no builds yet", map[string]any{"cluster_environment_id": clusterEnvID})
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// Get build details
	result, err := r.getBuild(ctx, template.LatestBuild.ID)
	if err != nil {
		// The template itself is confirmed live above; a failure fetching its latest
		// build is soft-warned rather than treated as resource deletion, matching
		// resource_container_image_build.go's Read().
		tflog.Warn(ctx, "Failed to get build details", map[string]any{
			"build_id": template.LatestBuild.ID,
			"error":    err.Error(),
		})
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	state.BuildID = types.StringValue(result.ID)
	state.BuildStatus = types.StringValue(result.Status)
	state.CreatedAt = types.StringValue(result.CreatedAt)
	state.IsBYOD = types.BoolValue(result.IsBYOD)
	state.Revision = types.Int64Value(int64(result.Revision))

	state.Digest = types.StringPointerValue(result.Digest)

	// image_uri is Required in the schema, so the user always supplied a value
	// that the API now echoes back as docker_image_name. Refresh from the API
	// to keep state in sync if the canonical URI differs from the user's input.
	if result.DockerImageName != nil {
		state.ImageURI = types.StringValue(*result.DockerImageName)
	}

	// ray_version is Optional+Computed and RequiresReplace (immutable): only fill it
	// when state does not already carry a value (unset at Create time, e.g. a
	// resource from a provider version that left ray_version null, or - defensively - a still-null value somehow left over
	// from Create). A previously-set value, whether user-typed or filled on an earlier
	// refresh, is preserved untouched; see the matching comment in Create for why the
	// API's echoed value cannot safely overwrite it.
	if state.RayVersion.IsNull() {
		if resolvedRayVersion := result.ResolvedRayVersion(); resolvedRayVersion != nil {
			state.RayVersion = types.StringValue(*resolvedRayVersion)
		} else {
			state.RayVersion = types.StringNull()
		}
	}

	// name is Optional+Computed and RequiresReplace (immutable): only fill it when state does
	// not already carry a value (unset at Create time, e.g. an upgraded pre-fix resource or a
	// cold import, or - defensively - a still-null value somehow left over from Create). A
	// previously-set value, whether user-typed or filled on an earlier refresh, is preserved
	// untouched. Mirrors ray_version's fill-on-null guard immediately above.
	if state.Name.IsNull() {
		state.Name = types.StringValue(template.Name)
	}

	// name_version deliberately still prefers state.Name over template.Name here (unchanged from
	// before name became Computed) - the backend never renames on submit (verified: an invalid
	// character 422s, a name collision 409s, neither sanitizes nor uniquifies), so the two cannot
	// actually disagree, and changing this to match would be a no-op decorated as a fix. See
	// docs/decisions/container-image-registry-name for the full ruling and its reopen condition.
	clusterEnvName := template.Name
	if !state.Name.IsNull() {
		clusterEnvName = state.Name.ValueString()
	}
	state.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", clusterEnvName, result.Revision))

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update handles two in-place changes. A changed image_uri registers a new build on the
// same cluster environment, as `anyscale image register` does for an existing name. A
// registry_login_secret set after import (see RequiresReplaceUnlessUnrecoverable) is
// recorded in state and only sent if image_uri also changed.
func (r *ContainerImageRegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ContainerImageRegistryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Name.Equal(state.Name) || !plan.RayVersion.Equal(state.RayVersion) {
		AddConfigError(&resp.Diagnostics, "Update Not Supported",
			"Changing name or ray_version requires replacing the container image.")
		return
	}

	if plan.ImageURI.Equal(state.ImageURI) {
		state.RegistryLoginSecret = plan.RegistryLoginSecret
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	rayVersion := defaultBYODRayVersion
	if plan.RayVersion.ValueString() != "" {
		rayVersion = plan.RayVersion.ValueString()
	}
	if !r.createBuild(ctx, &plan, plan.Name.ValueString(), rayVersion, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *ContainerImageRegistryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ContainerImageRegistryResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterEnvID := state.ID.ValueString()

	archiveClusterEnvironment(ctx, r.client, clusterEnvID, &resp.Diagnostics)
}

// ImportState imports the resource into Terraform state.
func (r *ContainerImageRegistryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by cluster environment ID (same as resource_container_image_build.go).
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
