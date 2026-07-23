package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// This resource manages WHICH cloud is an organization's default cloud - the
// org-level pointer (organizations.default_cloud_id) that anyscale_cloud's
// own removed is_default attribute used to mirror read-only. See the
// 2026-07-23 is_default quest design record for the full history: that
// attribute was removed for being an unsafe-to-plan mirror of this exact
// org-level fact; this resource is the manage half of the split (observe
// half is anyscale_cloud's own is_default data source attribute, which reads
// GET /clouds/{id} directly - auth-independent, unlike this resource's own
// drift-detection Read below, which deliberately uses the SAME endpoint for
// the SAME reason).
//
// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &OrganizationDefaultCloudResource{}
	_ resource.ResourceWithConfigure   = &OrganizationDefaultCloudResource{}
	_ resource.ResourceWithImportState = &OrganizationDefaultCloudResource{}
)

// NewOrganizationDefaultCloudResource creates a new organization default cloud resource.
func NewOrganizationDefaultCloudResource() resource.Resource {
	return &OrganizationDefaultCloudResource{}
}

// OrganizationDefaultCloudResource defines the resource implementation.
type OrganizationDefaultCloudResource struct {
	client *Client
}

// OrganizationDefaultCloudResourceModel describes the resource data model.
type OrganizationDefaultCloudResourceModel struct {
	ID      types.String `tfsdk:"id"` // organization_id
	CloudID types.String `tfsdk:"cloud_id"`
}

// Metadata returns the resource type name.
func (r *OrganizationDefaultCloudResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_default_cloud"
}

// Schema defines the schema for the resource.
func (r *OrganizationDefaultCloudResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages which cloud is the calling organization's default cloud - the same org-wide pointer surfaced read-only by `anyscale_cloud`'s own `is_default` attribute on each cloud. Declare **at most one** of this resource per organization: there is exactly one org default, so two instances fight over the same pointer on every apply. Requires an organization-owner-level token; a non-owner token fails at apply with a 403 from the Anyscale API. Destroying this resource stops Terraform from managing the pointer - it does **not** unset or change which cloud is currently the org default, since the underlying API has no path to clear it. If someone changes the org default outside Terraform (console or CLI), the next plan detects the drift and re-asserts `cloud_id`.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The organization's unique identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"cloud_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the cloud to set as the organization's default. Must be an existing cloud in this organization - checked before the API call, since the underlying endpoint performs no validation of its own and would otherwise silently accept a typo'd or wrong-organization ID. This is also the resource's Terraform import ID: `terraform import anyscale_organization_default_cloud.example <cloud_id>` imports the cloud that is CURRENTLY the organization default - importing any other cloud ID fails with a clear error.",
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *OrganizationDefaultCloudResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// setOrganizationDefaultCloud validates cloudID exists (the backend's own
// update_default_cloud performs no such check - confirmed against the real
// service: it writes the given cloud_id straight through with no existence
// or ownership lookup, so a bogus or another org's cloud_id would otherwise
// succeed silently) and then calls the real set-default endpoint. Mirrors
// the CLI's own "anyscale cloud set-default" behavior, which validates
// client-side for the identical reason before calling the same endpoint.
func setOrganizationDefaultCloud(ctx context.Context, client *Client, cloudID string) error {
	if _, err := DoRequestRaw(
		ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK,
	); err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("cloud %q not found", cloudID)
		}
		return fmt.Errorf("validate cloud exists: %w", err)
	}

	path := fmt.Sprintf("/api/v2/organizations/update_default_cloud?cloud_id=%s", cloudID)
	tflog.Debug(ctx, "POST "+path)
	if _, err := DoRequestRaw(ctx, client, "POST", path, nil, http.StatusNoContent); err != nil {
		return fmt.Errorf("set organization default cloud: %w", err)
	}
	return nil
}

// Create sets the organization's default cloud to plan.CloudID.
func (r *OrganizationDefaultCloudResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationDefaultCloudResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := plan.CloudID.ValueString()
	tflog.Info(ctx, "Setting organization default cloud", map[string]any{"cloud_id": cloudID})

	if err := setOrganizationDefaultCloud(ctx, r.client, cloudID); err != nil {
		AddAPIError(&resp.Diagnostics, "set organization default cloud", err)
		return
	}

	org, err := fetchCurrentOrganization(ctx, r.client)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "fetch organization info", err)
		return
	}

	plan.ID = org.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read checks whether plan.CloudID is still the organization's default cloud
// by reading THAT SPECIFIC cloud (GET /clouds/{cloud_id}, the same
// auth-independent, unconditional DB-comparison endpoint anyscale_cloud's
// own is_default attribute reads) rather than listing every cloud in the
// organization. Two deliberate choices behind that:
//
//   - It only needs to answer "is the cloud I manage still the default,"
//     never "which OTHER cloud took over" - a targeted single-cloud read
//     answers that directly.
//   - The plural list endpoint (GET /clouds) computes is_default via a
//     different, caller-scoped fallback chain (org default, else the
//     caller's own last-used cloud, else first-accessible) that can label a
//     DIFFERENT cloud is_default:true if the caller's auth context can't see
//     whichever cloud the org's real default actually is - confirmed by
//     tracing search_clouds/_fetch_clouds_collection in the backend. Reading
//     the specific cloud by ID never goes through that fallback at all.
//
// If the cloud no longer exists, or is no longer the default (drift - the
// org default moved elsewhere, out of band), this removes the resource from
// state. For "no longer the default" specifically, that is deliberate: since
// cloud_id is Required (not Computed), there is no attribute to silently
// refresh to a new value the way a Computed field would. Removing from state
// makes the next plan a fresh Create, which re-asserts cloud_id as the org
// default - matching this resource's whole purpose of actively enforcing the
// pointer, not just reporting it.
func (r *OrganizationDefaultCloudResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationDefaultCloudResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.CloudID.ValueString()

	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read cloud", err)
		return
	}
	defer CloseBody(ctx, httpResp.Body)

	if httpResp.StatusCode == http.StatusNotFound {
		tflog.Warn(ctx, "Managed cloud no longer exists, removing from state", map[string]any{"cloud_id": cloudID})
		resp.State.RemoveResource(ctx)
		return
	}

	var cloudResp CloudResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&cloudResp); err != nil {
		AddJSONError(&resp.Diagnostics, "unmarshal", "cloud response", err)
		return
	}

	if !cloudResp.Result.IsDefault {
		tflog.Warn(ctx, "Cloud is no longer the organization default; the org default moved out of band", map[string]any{"cloud_id": cloudID})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update re-points the organization's default cloud when cloud_id changes.
// cloud_id has no plan modifier and is Required, so Update only ever runs
// for that one change - id (organization_id) never changes independently.
func (r *OrganizationDefaultCloudResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan OrganizationDefaultCloudResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := plan.CloudID.ValueString()
	tflog.Info(ctx, "Re-pointing organization default cloud", map[string]any{"cloud_id": cloudID})

	if err := setOrganizationDefaultCloud(ctx, r.client, cloudID); err != nil {
		AddAPIError(&resp.Diagnostics, "set organization default cloud", err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete is deliberately a no-op against the API: update_default_cloud has
// no unset/null path (confirmed against the real service - cloud_id is a
// required, non-nullable parameter both in the request and in the DB write
// it performs), so there is nothing to "undo" on the backend even if we
// wanted to. Destroying this resource only stops Terraform from managing
// the pointer; the organization's actual default cloud is left exactly as
// it was. This is a deliberate design choice, not a missing feature: a
// destructive clear-to-null would leave an organization with NO default
// cloud as a side effect of a routine `terraform destroy`, which this
// resource's design explicitly avoids.
func (r *OrganizationDefaultCloudResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	tflog.Info(ctx, "Removing anyscale_organization_default_cloud from Terraform state (org default cloud is left unchanged)")
}

// ImportState imports by cloud_id - the cloud the user asserts is currently
// the organization's default - NOT by organization id. Validated via the
// same singular GET /clouds/{cloud_id} Read/drift already uses: auth-
// independent, no userinfo involved in the validation itself. This
// corrects an earlier draft that imported by organization id via userinfo
// instead (caught independently by scribe and by re-reading architect's
// authoritative spec) - that version silently accepted any import ID,
// never validated it against what the caller actually typed, and
// reintroduced the unverified-user null blind spot this whole resource
// exists to avoid.
//
// The schema's own "id" attribute is organization id, not cloud_id, so one
// userinfo call is still needed here to populate it - that is populating a
// separate Computed field, not validating the import target, so it does
// not conflict with the spec's "auth-independent, no userinfo needed" for
// the validation step itself.
func (r *OrganizationDefaultCloudResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cloudID := req.ID

	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read cloud", err)
		return
	}
	defer CloseBody(ctx, httpResp.Body)

	if httpResp.StatusCode == http.StatusNotFound {
		AddConfigError(&resp.Diagnostics, "Cloud Not Found",
			fmt.Sprintf("Cloud %q was not found.", cloudID))
		return
	}

	var cloudResp CloudResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&cloudResp); err != nil {
		AddJSONError(&resp.Diagnostics, "unmarshal", "cloud response", err)
		return
	}

	if !cloudResp.Result.IsDefault {
		AddConfigError(&resp.Diagnostics, "Not The Organization Default",
			fmt.Sprintf("Cloud %q is not the current organization default; import the cloud that is.", cloudID))
		return
	}

	org, err := fetchCurrentOrganization(ctx, r.client)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "fetch organization info", err)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), org.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_id"), types.StringValue(cloudID))...)
}
