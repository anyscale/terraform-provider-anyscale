package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// anyscale_runtime_env_package packages a local directory, uploads it to a cloud's data bucket
// under a content-addressed name, and outputs the URI for a runtime_env's working_dir or
// py_modules. Design: docs/decisions/service-working-dir-upload/README.md.
//
// The content hash is computed in ModifyPlan, not Read: a hash refreshed in Read would change
// state without the resource itself planning anything, so the upload would never run. Delete
// only forgets the resource, because no public API deletes the object.
var (
	_ resource.Resource               = &RuntimeEnvPackageResource{}
	_ resource.ResourceWithConfigure  = &RuntimeEnvPackageResource{}
	_ resource.ResourceWithModifyPlan = &RuntimeEnvPackageResource{}
)

// NewRuntimeEnvPackageResource creates the anyscale_runtime_env_package resource.
func NewRuntimeEnvPackageResource() resource.Resource {
	return &RuntimeEnvPackageResource{}
}

// RuntimeEnvPackageResource defines the resource implementation.
type RuntimeEnvPackageResource struct {
	client *Client
}

// RuntimeEnvPackageResourceModel describes the resource data model.
type RuntimeEnvPackageResourceModel struct {
	ID               types.String `tfsdk:"id"`
	SourceDir        types.String `tfsdk:"source_dir"`
	Excludes         types.List   `tfsdk:"excludes"`
	CloudID          types.String `tfsdk:"cloud_id"`
	CloudResourceIDs types.List   `tfsdk:"cloud_resource_ids"`
	ContentHash      types.String `tfsdk:"content_hash"`
	URI              types.String `tfsdk:"uri"`
	RelativePath     types.String `tfsdk:"relative_path"`
}

// Metadata returns the resource type name.
func (r *RuntimeEnvPackageResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runtime_env_package"
}

// Schema defines the schema for the resource.
func (r *RuntimeEnvPackageResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Zips a local directory, uploads it to a cloud's storage bucket under a content-addressed name, and outputs its `uri` for a service's `runtime_env.working_dir` or `py_modules`. " +
			"A plan shows an update only when included file contents, names, or exec bits change. " +
			"Destroying it never deletes the uploaded object. Supports AWS and GCP clouds, not Azure. " +
			"See the [Runtime Environment Packages guide](../guides/runtime-env-packages.md).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Same as `relative_path`.",
			},
			"source_dir": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Local directory to package, typically under `path.module`. Read on every plan. Its location does not affect `content_hash`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"excludes": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Extra `.gitignore`-style patterns, relative to `source_dir`, to leave out. Always excluded: `.git`, `.venv`, `venv`, `__pycache__`, and anything a `.gitignore` in the tree ignores.",
				Validators:          []validator.List{listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1))},
			},
			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cloud whose storage receives the package. Use the cloud of the compute config that runs it: another cloud's cluster may not be able to read the object, which fails only when the service starts.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"cloud_resource_ids": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Cloud resources to upload to. Defaults to the cloud's primary resource. List every resource a multi-resource compute config uses, and reference `relative_path` instead of `uri`.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"content_hash": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SHA-256 of the included files' relative paths, contents, and exec bits. Modification times are ignored, so a fresh checkout of the same code plans no change.",
			},
			"uri": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Storage URI of the package (`s3://...` or `gs://...`) in the first target bucket. Use it as `working_dir`.",
			},
			"relative_path": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Object key of the package, identical in every target bucket. Use it as `relative_working_dir` when the compute config spans several cloud resources.",
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *RuntimeEnvPackageResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics,
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = client
}

// ModifyPlan hashes the source tree. When the hash differs from state it plans an Update with
// uri, relative_path and id unknown, so anything referencing uri plans its own change too.
// No network calls.
func (r *RuntimeEnvPackageResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan RuntimeEnvPackageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.SourceDir.IsUnknown() || listHasUnknown(plan.Excludes) {
		plan.ContentHash = types.StringUnknown()
		plan.URI, plan.RelativePath, plan.ID = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}

	excludes, diags := stringListValues(ctx, plan.Excludes)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	tree, err := walkPackageTree(plan.SourceDir.ValueString(), excludes)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("source_dir"), "Cannot Package source_dir", err.Error())
		return
	}
	contentHash, err := tree.contentHash()
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("source_dir"), "Cannot Package source_dir", err.Error())
		return
	}
	addPackageSizeWarnings(tree, &resp.Diagnostics)

	plan.ContentHash = types.StringValue(contentHash)

	var state *RuntimeEnvPackageResourceModel
	if !req.State.Raw.IsNull() {
		state = &RuntimeEnvPackageResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if state != nil && state.ContentHash.ValueString() == contentHash {
		// Same content: the object name, and so every output, is unchanged.
		plan.URI, plan.RelativePath, plan.ID = state.URI, state.RelativePath, state.ID
	} else {
		plan.URI, plan.RelativePath, plan.ID = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func addPackageSizeWarnings(tree *packageTree, diags *diag.Diagnostics) {
	if len(tree.largeFiles) > 0 {
		diags.AddAttributeWarning(path.Root("source_dir"), "Large Files in Package",
			fmt.Sprintf("These files are 100 MiB or larger and are read on every plan and uploaded with the package: %s. "+
				"Consider adding them to excludes.", strings.Join(tree.largeFiles, ", ")))
	}
	if tree.totalSize >= runtimeEnvPackageLargeTotal {
		diags.AddAttributeWarning(path.Root("source_dir"), "Large Package",
			fmt.Sprintf("The package holds %d MiB of files, all read on every plan. Large data belongs in storage the cluster reads directly, not in a working_dir.",
				tree.totalSize>>20))
	}
}

// Create packages and uploads source_dir.
func (r *RuntimeEnvPackageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RuntimeEnvPackageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.upload(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Update uploads the new content. Packages are content-addressed, so the previous object is
// left in place for anything still using it (an in-progress rollout, another service).
func (r *RuntimeEnvPackageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RuntimeEnvPackageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.upload(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// upload zips source_dir, checks it against the planned content_hash, and uploads it to every
// target that does not already hold the object. On success it sets the computed outputs.
func (r *RuntimeEnvPackageResource) upload(ctx context.Context, plan *RuntimeEnvPackageResourceModel, diags *diag.Diagnostics) {
	excludes, d := stringListValues(ctx, plan.Excludes)
	diags.Append(d...)
	targets, d := stringListValues(ctx, plan.CloudResourceIDs)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	if len(targets) == 0 {
		targets = []string{""} // the cloud's primary resource
	}

	tree, err := walkPackageTree(plan.SourceDir.ValueString(), excludes)
	if err != nil {
		diags.AddAttributeError(path.Root("source_dir"), "Cannot Package source_dir", err.Error())
		return
	}
	zipPath, contentHash, size, err := buildPackageZip(tree)
	if err != nil {
		diags.AddAttributeError(path.Root("source_dir"), "Cannot Package source_dir", err.Error())
		return
	}
	defer func() { _ = os.Remove(zipPath) }()

	if planned := plan.ContentHash; !planned.IsUnknown() && planned.ValueString() != contentHash {
		diags.AddAttributeError(path.Root("source_dir"), "source_dir Changed Since Plan",
			fmt.Sprintf("The files in %q changed after this plan was made (planned content_hash %s, now %s). "+
				"Nothing was uploaded. Run terraform plan again.", plan.SourceDir.ValueString(), planned.ValueString(), contentHash))
		return
	}

	f, err := os.Open(zipPath)
	if err != nil {
		diags.AddError("Cannot Read Package", err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	cloudID := plan.CloudID.ValueString()
	fileName := runtimeEnvPackageFileName(contentHash)
	var first *CloudDataBucketPresignedURL
	for _, target := range targets {
		info, err := RequestRuntimeEnvPackageURL(ctx, r.client, cloudID, target, fileName, CloudDataBucketWrite)
		if err != nil {
			AddAPIError(diags, "request an upload URL for "+describePackageTarget(cloudID, target), err)
			return
		}
		if err := checkPresignedUploadSupported(info); err != nil {
			diags.AddAttributeError(path.Root("cloud_id"), "Unsupported Cloud Storage", err.Error())
			return
		}
		if first != nil && info.BucketPath != first.BucketPath {
			diags.AddError("Inconsistent Package Paths",
				fmt.Sprintf("The object key differs between cloud resources (%q and %q), so no single relative_path works for all of them.", first.BucketPath, info.BucketPath))
			return
		}
		if info.FileExists {
			tflog.Info(ctx, "Package already uploaded; skipping", map[string]any{"file_uri": info.FileURI})
		} else {
			tflog.Info(ctx, "Uploading runtime_env package", map[string]any{"file_uri": info.FileURI, "bytes": size})
			if err := uploadToPresignedURL(ctx, r.client.HTTPClient, info.URL, f, size); err != nil {
				diags.AddError("Package Upload Failed",
					fmt.Sprintf("Uploading %s failed: %s", info.FileURI, err))
				return
			}
		}
		if first == nil {
			first = info
		}
	}

	plan.ContentHash = types.StringValue(contentHash)
	plan.URI = types.StringValue(first.FileURI)
	plan.RelativePath = types.StringValue(first.BucketPath)
	plan.ID = plan.RelativePath
}

func describePackageTarget(cloudID, cloudResourceID string) string {
	if cloudResourceID == "" {
		return fmt.Sprintf("cloud %s", cloudID)
	}
	return fmt.Sprintf("cloud %s, cloud resource %s", cloudID, cloudResourceID)
}

// Read checks that the object still exists in every target bucket and removes the resource
// from state when one is missing, so the next apply uploads it again under the same name.
func (r *RuntimeEnvPackageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RuntimeEnvPackageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ContentHash.IsNull() || state.ContentHash.ValueString() == "" {
		return
	}
	targets, diags := stringListValues(ctx, state.CloudResourceIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(targets) == 0 {
		targets = []string{""}
	}

	cloudID := state.CloudID.ValueString()
	fileName := runtimeEnvPackageFileName(state.ContentHash.ValueString())
	for _, target := range targets {
		info, err := RequestRuntimeEnvPackageURL(ctx, r.client, cloudID, target, fileName, CloudDataBucketRead)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				tflog.Warn(ctx, "Cloud or cloud resource not found; removing package from state", map[string]any{"target": describePackageTarget(cloudID, target)})
				resp.State.RemoveResource(ctx)
				return
			}
			AddAPIError(&resp.Diagnostics, "check the package in "+describePackageTarget(cloudID, target), err)
			return
		}
		if packageObjectMissing(info) {
			tflog.Warn(ctx, "Package object missing from storage; removing from state so the next apply uploads it again", map[string]any{"file_uri": info.FileURI})
			resp.State.RemoveResource(ctx)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// packageObjectMissing reports whether a READ response proves the object is gone. Only an
// http-scheme response is a live probe: smart_open responses always say file_exists=false,
// and trusting that would recreate the resource on every plan.
func packageObjectMissing(info *CloudDataBucketPresignedURL) bool {
	return info.URLScheme == presignedURLSchemeHTTP && !info.FileExists
}

// Delete only removes the resource from state. No public API deletes a package object, and
// the object may still be in use by a service version, a rollback, or another configuration
// that packages the same content.
func (r *RuntimeEnvPackageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	tflog.Info(ctx, "Removing anyscale_runtime_env_package from state; the uploaded object is left in storage")
}

// listHasUnknown reports whether a list, or any of its elements, is unknown.
func listHasUnknown(l types.List) bool {
	if l.IsUnknown() {
		return true
	}
	for _, e := range l.Elements() {
		if e.IsUnknown() {
			return true
		}
	}
	return false
}

// stringListValues converts a known list of strings to a slice. Null yields nil.
func stringListValues(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}
