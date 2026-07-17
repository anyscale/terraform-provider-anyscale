package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &OrganizationCollaboratorResource{}
	_ resource.ResourceWithConfigure   = &OrganizationCollaboratorResource{}
	_ resource.ResourceWithImportState = &OrganizationCollaboratorResource{}
)

// NewOrganizationCollaboratorResource creates a new organization collaborator resource.
func NewOrganizationCollaboratorResource() resource.Resource {
	return &OrganizationCollaboratorResource{}
}

// OrganizationCollaboratorResource defines the resource implementation.
type OrganizationCollaboratorResource struct {
	client *Client
}

// OrganizationCollaboratorResourceModel describes the resource data model.
type OrganizationCollaboratorResourceModel struct {
	// Identity
	ID types.String `tfsdk:"id"` // identity_id

	// Manageable field
	PermissionLevel types.String `tfsdk:"permission_level"`

	// Computed fields
	Email     types.String `tfsdk:"email"`
	UserID    types.String `tfsdk:"user_id"`
	Name      types.String `tfsdk:"name"`
	CreatedAt types.String `tfsdk:"created_at"`

	// Current role-model visibility (read-only; see schema doc). permission_level
	// above remains the only writable field - the /roles write endpoint is
	// feature-gated (501 in most orgs), so these exist for visibility and drift
	// detection only, not management.
	BaseRole        types.String `tfsdk:"base_role"`
	AdditionalRoles types.List   `tfsdk:"additional_roles"`
}

// Metadata returns the resource type name.
func (r *OrganizationCollaboratorResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_collaborator"
}

// Schema defines the schema for the resource.
func (r *OrganizationCollaboratorResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an existing Anyscale Organization Collaborator's permissions.\n\n" +
			"~> **Warning:** Destroying this resource removes the user from the organization entirely, not just from Terraform state — it is a real, immediate `DELETE` against the Anyscale API. There is no undo; the user would need to be re-invited and re-accept to regain access. This also happens on any `terraform destroy` that reaches this resource, including as part of tearing down a larger configuration. If you only want Terraform to stop managing a collaborator without removing their access, use `terraform state rm` instead of `terraform destroy`. This is a heavier operation than destroying an `anyscale_organization_invitation` - once accepted, an invitation and its resulting membership are separate objects, and only this resource's destroy actually revokes access.\n\n" +
			"**Important:** This resource cannot create new users. Users must first be added to the organization through:\n" +
			"1. An accepted `anyscale_organization_invitation`, or\n" +
			"2. SCIM provisioning\n\n" +
			"Once a user exists in the organization, import them using `terraform import` to manage their permissions.\n\n" +
			"**Example Import:**\n```\nterraform import anyscale_organization_collaborator.user <identity_id>\n```\n\n" +
			"**Directory-synced organizations:** If your organization manages permissions via directory sync (the Policy API), this resource cannot manage collaborators at all - any `terraform apply` against it fails, and the error points you to the `anyscale policy set` command instead. See the [Anyscale policy CLI documentation](https://docs.anyscale.com/reference/cli/policy#policy-cli) for that command.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identity ID of the collaborator. Used for import.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"permission_level": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The permission level for this collaborator. Must be either `owner` or `collaborator`.",
				Validators: []validator.String{
					stringvalidator.OneOf("owner", "collaborator"),
				},
			},

			"email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The email address of the collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user ID of the collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the collaborator was added to the organization. Write-once: set on import and never re-read afterward, since the API has returned different values for it across reads for the same collaborator.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"base_role": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The collaborator's base role in the organization (`owner` or `collaborator`). This is the backend's current source of role information; `permission_level` above remains the field you set to change it, and the two always agree since the backend derives `permission_level` from `base_role` on every read.",
				// Deliberately no UseStateForUnknown: base_role is derived from
				// the same underlying role permission_level writes, so it
				// legitimately changes whenever permission_level does. Freezing
				// it to the prior value (as a first pass of this schema did)
				// makes any real permission_level change hard-fail apply with
				// "Provider produced inconsistent result after apply" - caught
				// by assayer's collaborator lifecycle test.
			},

			"additional_roles": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Additional restriction (deny) roles applied on top of this collaborator's base role (for example `image_reader`, which restricts container-image creation a plain collaborator could otherwise do), if any - never an alternative permission level, and never additional capability beyond the base role. Read-only: the Anyscale API endpoint that manages these roles is feature-gated and returns HTTP 501 in most organizations, so Terraform cannot set them - this attribute exists for visibility and drift detection only. Three states: populated means the collaborator genuinely has one or more additional roles; empty means the backend was queried and reports none (including in an organization where the underlying roles-read feature is off - there, the concept is simply inactive); null means the provider could not query it at all, which only happens for a collaborator with no `user_id` (the query is `user_id`-keyed). Guard against null in your configuration before calling `length()` or iterating over this value - for example `length(coalesce(additional_roles, []))` rather than `length(additional_roles)` directly, which errors on a null list.",
				// UseStateForUnknown is safe here (unlike base_role above):
				// alter_collaborator never touches the SpiceDB-managed groups
				// additional_roles comes from, and assayer proved live (real
				// infra, toggling permission_level owner<->collaborator through
				// the real write path) that additional_roles holds steady the
				// whole time. Pinning it avoids a false "(known after apply)" on
				// this field during every permission_level update.
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *OrganizationCollaboratorResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Create returns an error directing users to use the invitation resource or import.
func (r *OrganizationCollaboratorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError(
		"Direct Creation Not Supported",
		"Organization collaborators cannot be created directly through the API.\n\n"+
			"To add a new user to your organization:\n"+
			"1. Use the 'anyscale_organization_invitation' resource to send an invitation:\n"+
			"   resource \"anyscale_organization_invitation\" \"new_user\" {\n"+
			"     email = \"user@example.com\"\n"+
			"   }\n\n"+
			"2. Wait for the user to accept the invitation\n\n"+
			"3. Find the user's identity_id using the data source:\n"+
			"   data \"anyscale_organization_user\" \"new_user\" {\n"+
			"     email = \"user@example.com\"\n"+
			"   }\n\n"+
			"4. Import the collaborator to manage their permissions:\n"+
			"   terraform import anyscale_organization_collaborator.new_user <identity_id>\n\n"+
			"Alternatively, if the user already exists in your organization (e.g., via SCIM),\n"+
			"you can import them directly using their identity_id.",
	)
}

// Read reads the current state of an organization collaborator.
func (r *OrganizationCollaboratorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationCollaboratorResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Reading organization collaborator", map[string]interface{}{
		"identity_id": identityID,
	})

	// Fetch collaborator from API
	collaborator, err := r.findCollaboratorByID(ctx, identityID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			// Collaborator was removed outside of Terraform
			tflog.Warn(ctx, "Collaborator not found, removing from state", map[string]interface{}{
				"identity_id": identityID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading collaborator",
			fmt.Sprintf("Could not read collaborator %s: %s", identityID, err.Error()),
		)
		return
	}

	// Update state with API data. created_at is intentionally NOT refreshed
	// here - see the schema doc string; the API has returned different values
	// for it across reads, and it's treated as write-once (set on import only).
	resp.Diagnostics.Append(applyCollaboratorIdentityFields(ctx, &state, collaborator)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.PermissionLevel = types.StringValue(collaborator.PermissionLevel)

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// applyCollaboratorIdentityFields copies the identity and role fields that are
// safe to refresh from the API (email, user_id, name, base_role,
// additional_roles) into model. created_at is deliberately excluded and must be
// set separately only where appropriate (import) - see the schema doc string
// and task 4745d9fb: the API has returned different created_at values across
// reads for the same collaborator, so re-syncing it from Read/Update causes
// "Provider produced inconsistent result after apply".
func applyCollaboratorIdentityFields(ctx context.Context, model *OrganizationCollaboratorResourceModel, collaborator *OrganizationCollaboratorResult) diag.Diagnostics {
	model.Email = types.StringValue(collaborator.Email)

	if collaborator.UserID != nil && *collaborator.UserID != "" {
		model.UserID = types.StringValue(*collaborator.UserID)
	} else {
		model.UserID = types.StringNull()
	}

	if collaborator.Name != nil && *collaborator.Name != "" {
		model.Name = types.StringValue(*collaborator.Name)
	} else {
		model.Name = types.StringNull()
	}

	model.BaseRole = types.StringValue(collaborator.BaseRole)

	// additional_roles is tri-state: nil from hydrateCollaboratorRoles means
	// undetermined (render null), a non-nil (possibly empty) slice means the
	// backend was actually queried (render [], never null, when genuinely
	// none) - see hydrateCollaboratorRoles and the schema doc.
	if collaborator.AdditionalRoles == nil {
		model.AdditionalRoles = types.ListNull(types.StringType)
		return nil
	}

	additionalRolesList, diags := types.ListValueFrom(ctx, types.StringType, collaborator.AdditionalRoles)
	model.AdditionalRoles = additionalRolesList
	return diags
}

// Update updates an organization collaborator's permission level.
func (r *OrganizationCollaboratorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OrganizationCollaboratorResourceModel

	// Read plan and state
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Updating organization collaborator", map[string]interface{}{
		"identity_id":          identityID,
		"old_permission_level": state.PermissionLevel.ValueString(),
		"new_permission_level": plan.PermissionLevel.ValueString(),
	})

	// Create update request
	updateReq := UpdateOrganizationCollaboratorRequest{
		PermissionLevel: plan.PermissionLevel.ValueString(),
	}

	jsonData, err := json.Marshal(updateReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error marshaling request",
			fmt.Sprintf("Could not marshal collaborator update request: %s", err.Error()),
		)
		return
	}

	// Send update request. The response body isn't parsed into anything - the resource re-reads
	// current state via findCollaboratorByID right after, so only the status code matters here.
	if _, err := DoRequestRaw(
		ctx, r.client, "PUT", fmt.Sprintf("/api/v2/organization_collaborators/%s", identityID), strings.NewReader(string(jsonData)),
		http.StatusOK, http.StatusNoContent,
	); err != nil {
		resp.Diagnostics.AddError("Could Not Update Collaborator's Permission Level", collaboratorErrorDiagnosticDetail(err))
		return
	}

	tflog.Info(ctx, "Updated organization collaborator", map[string]interface{}{
		"identity_id":      identityID,
		"permission_level": plan.PermissionLevel.ValueString(),
	})

	// Read back to get current state
	collaborator, err := r.findCollaboratorByID(ctx, identityID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading updated collaborator",
			fmt.Sprintf("Could not read collaborator after update: %s", err.Error()),
		)
		return
	}

	// Update plan with latest data. created_at is intentionally left as
	// req.Plan.Get already resolved it (UseStateForUnknown -> the prior state
	// value) rather than overwritten here - the API has returned different
	// created_at values across reads for the same collaborator, and
	// overwriting it caused "Provider produced inconsistent result after
	// apply" on a bare permission_level change (task 4745d9fb).
	resp.Diagnostics.Append(applyCollaboratorIdentityFields(ctx, &plan, collaborator)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a collaborator from the organization.
func (r *OrganizationCollaboratorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationCollaboratorResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Removing organization collaborator", map[string]interface{}{
		"identity_id":  identityID,
		"email_domain": getEmailDomain(state.Email.ValueString()),
	})

	// Delete the collaborator - treat 404 as success (already removed)
	_, err := DoRequestRaw(ctx, r.client, "DELETE", fmt.Sprintf("/api/v2/organization_collaborators/%s", identityID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		resp.Diagnostics.AddError("Could Not Remove Collaborator", collaboratorErrorDiagnosticDetail(err))
		return
	}

	tflog.Info(ctx, "Removed organization collaborator", map[string]interface{}{
		"identity_id": identityID,
	})
}

// ImportState imports an existing collaborator by identity_id.
func (r *OrganizationCollaboratorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by identity_id
	identityID := req.ID

	tflog.Info(ctx, "Importing organization collaborator", map[string]interface{}{
		"identity_id": identityID,
	})

	// Fetch collaborator to validate it exists
	collaborator, err := r.findCollaboratorByID(ctx, identityID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error importing collaborator",
			fmt.Sprintf("Could not find collaborator with identity_id %s: %s\n\n"+
				"Tip: Use the anyscale_organization_user data source to find the identity_id:\n"+
				"  data \"anyscale_organization_user\" \"example\" {\n"+
				"    email = \"user@example.com\"\n"+
				"  }\n\n"+
				"Then import using: terraform import anyscale_organization_collaborator.example <identity_id>",
				identityID, err.Error()),
		)
		return
	}

	// Set all attributes
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), identityID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), collaborator.Email)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("permission_level"), collaborator.PermissionLevel)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("created_at"), collaborator.CreatedAt)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("base_role"), collaborator.BaseRole)...)

	// additional_roles tri-state: nil (undetermined) vs a real, possibly empty
	// slice (queried) - see hydrateCollaboratorRoles and the schema doc.
	if collaborator.AdditionalRoles == nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("additional_roles"), types.ListNull(types.StringType))...)
	} else {
		additionalRolesList, diags := types.ListValueFrom(ctx, types.StringType, collaborator.AdditionalRoles)
		resp.Diagnostics.Append(diags...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("additional_roles"), additionalRolesList)...)
	}

	if collaborator.UserID != nil && *collaborator.UserID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), *collaborator.UserID)...)
	}

	if collaborator.Name != nil && *collaborator.Name != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), *collaborator.Name)...)
	}

	tflog.Info(ctx, "Imported organization collaborator", map[string]interface{}{
		"identity_id":      identityID,
		"email":            collaborator.Email,
		"permission_level": collaborator.PermissionLevel,
	})
}

// Helper functions

// listAllOrganizationCollaborators pages through the full /api/v2/organization_collaborators
// list via PaginatedRequest, across every page rather than stopping at the
// first, so a collaborator past page 1 is never mistaken for missing or
// silently dropped from a list. extraParams, if non-nil, is merged in
// alongside the page-size param (e.g. a server-side email or name filter);
// pass nil for an unfiltered listing.
func listAllOrganizationCollaborators(ctx context.Context, client *Client, extraParams url.Values) ([]OrganizationCollaboratorResult, error) {
	params := url.Values{"count": []string{"50"}}
	for k, v := range extraParams {
		params[k] = v
	}

	return PaginatedRequest(
		ctx, client, "/api/v2/organization_collaborators", params,
		func(body []byte) ([]OrganizationCollaboratorResult, *string, error) {
			var listResp OrganizationCollaboratorsListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("error parsing response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
}

// findCollaboratorByID fetches a collaborator by identity_id. The API has no
// direct GET-by-identity_id endpoint for a single collaborator, so this lists
// and filters for identity lookup, then hydrates the real role fields
// separately (see hydrateCollaboratorRoles) - identity lookup and role
// hydration are different concerns per architect ruling 1, and only the second
// one needs to move off the list endpoint.
func (r *OrganizationCollaboratorResource) findCollaboratorByID(ctx context.Context, identityID string) (*OrganizationCollaboratorResult, error) {
	collaborators, err := listAllOrganizationCollaborators(ctx, r.client, nil)
	if err != nil {
		return nil, err
	}

	for _, collab := range collaborators {
		if collab.ID == identityID {
			hydrated := hydrateCollaboratorRoles(ctx, r.client, collab)
			return &hydrated, nil
		}
	}

	return nil, fmt.Errorf("collaborator not found")
}

// hydrateCollaboratorRoles enriches a list-derived OrganizationCollaboratorResult
// with a real additional_roles value by calling the singular per-user GET, the
// only read path that can return one. GET-list's formatter hardcodes
// additional_roles to an empty slice unconditionally, regardless of a user's
// real roles or any flag state (traced against organizations_formatter.py;
// architect ruling 1) - the same call-site-migration shape as the compute_config
// ext/v0 pagination lesson: new information needs a real endpoint change, not
// just new struct fields.
//
// additional_roles is tri-state (mirrors the existing user_group_ids pattern in
// data_source_user.go): populated = real roles, empty = queried and genuinely
// none (a flag-off org returns this cleanly, confirmed empirically - assayer -
// not an error), nil = could not be determined at all. This function returns
// nil specifically (never a list) whenever it cannot query the singular GET,
// so callers must treat a nil AdditionalRoles as "undetermined" and render it
// as null, not empty - never coalesce nil to [] here or upstream.
//
// base_role is deliberately NEVER overwritten from the singular GET/search
// result, even on success - fromList's list-derived base_role is kept as-is
// throughout. This is load-bearing, not an oversight: alter_collaborator (the
// only real permission_level write path) writes Postgres only and never
// touches SpiceDB, while the singular GET/search read base_role from SpiceDB
// managed groups when the read flag is on. assayer proved live (real infra,
// toggling permission_level owner<->collaborator through the real write path)
// that the SpiceDB-sourced base_role never moves at all in response to a real
// write, while the list-derived (Postgres-formatted) base_role tracks it
// perfectly every time - because list and the write path share the same
// Postgres formatter/data. Overwriting base_role here previously made it go
// permanently stale after a single collaborator's first real permission_level
// change, in any org with the read flag on - a structural disconnect, not a
// transient race, and not caught by mocks since it required real backend
// behavior no mock had reason to model. additional_roles has no such
// alternative source, so it is the only field this call is for.
//
//   - fromList.UserID is nil/empty - some user types have no user_id, and the
//     singular GET is keyed by user_id, so it cannot be reached at all: nil.
//   - the call fails for any other reason (transport, status, parse - flag-off
//     itself returns a clean 200, so this is a genuine failure, not a normal
//     case): nil. Never surfaced as a Read/Import error either way.
func hydrateCollaboratorRoles(ctx context.Context, client *Client, fromList OrganizationCollaboratorResult) OrganizationCollaboratorResult {
	if fromList.UserID == nil || *fromList.UserID == "" {
		tflog.Debug(ctx, "Collaborator has no user_id; additional_roles is undetermined (not empty)", map[string]any{
			"identity_id": fromList.ID,
		})
		fromList.AdditionalRoles = nil
		return fromList
	}

	singular, err := DoRequestAndParse[OrganizationCollaboratorSingularResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/organization_collaborators/%s", *fromList.UserID), nil,
		http.StatusOK,
	)
	if err != nil {
		fromList.AdditionalRoles = nil
		tflog.Warn(ctx, "Singular collaborator GET failed; additional_roles is undetermined (not empty)", map[string]any{
			"identity_id": fromList.ID,
			"user_id":     *fromList.UserID,
			"error":       err.Error(),
		})
		return fromList
	}

	fromList.AdditionalRoles = singular.Result.AdditionalRoles
	return fromList
}

// collaboratorErrorDiagnosticDetail builds a clean diagnostic body for a
// failed Update/Delete against /api/v2/organization_collaborators. Rather than
// brittle client-side string-matching on each of the distinct 403s the backend
// can return (service-account-modify, support-user-modify,
// self-modify-permission-level, self-removal, and directory-sync/Policy-API -
// traced against alter_collaborator/_validate_user_for_role_change and
// remove_collaborator), this surfaces the backend's own error detail verbatim,
// which covers all of them - and any future one - uniformly and legibly,
// instead of the raw "unexpected status 403: {raw json}" wrapper.
//
// The one case that gets more than clean presentation: a directory-synced
// organization manages permissions via the Policy API instead, and the error
// text says so - that's the highest-value case to get right, since it means
// this resource cannot work at all for that collaborator, so a hint pointing
// at the actual tool is appended.
func collaboratorErrorDiagnosticDetail(err error) string {
	detail := extractAPIErrorDetail(err)

	if strings.Contains(detail, "Policy API") {
		return fmt.Sprintf("%s\n\nThis organization's collaborator permissions are managed outside Terraform. Use 'anyscale policy set' to manage this collaborator instead of this resource. See https://docs.anyscale.com/reference/cli/policy#policy-cli for details.", detail)
	}

	return detail
}
