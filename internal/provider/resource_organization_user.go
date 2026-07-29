package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &OrganizationUserResource{}
	_ resource.ResourceWithConfigure   = &OrganizationUserResource{}
	_ resource.ResourceWithImportState = &OrganizationUserResource{}
)

// NewOrganizationUserResource creates a new organization user resource.
func NewOrganizationUserResource() resource.Resource {
	return &OrganizationUserResource{}
}

// OrganizationUserResource defines the resource implementation. It manages an
// existing organization member's MEMBERSHIP only - see the schema
// MarkdownDescription for why this resource is import-only and named
// symmetrically with the anyscale_organization_user/anyscale_organization_users
// data sources rather than "collaborator": the API's own vocabulary
// (organization_collaborators) is preserved in the shared request/response
// types and helpers below, since it accurately names the backend concept
// those are shared with the data sources against.
//
// Role management deliberately does NOT live here - it belongs to
// anyscale_organization_user_role, which owns the base role and the
// organization's deny roles. Two resources writing the same org role would race
// each other, so this one owns membership (import + destroy-evicts) and that
// one owns the role.
type OrganizationUserResource struct {
	client *Client
}

// OrganizationUserResourceModel describes the resource data model. Every field
// is read-only: this resource has no writable attribute at all, since roles
// moved to anyscale_organization_user_role.
type OrganizationUserResourceModel struct {
	// Identity
	ID types.String `tfsdk:"id"` // identity_id

	// Computed fields
	Email     types.String `tfsdk:"email"`
	UserID    types.String `tfsdk:"user_id"`
	Name      types.String `tfsdk:"name"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// Metadata returns the resource type name.
func (r *OrganizationUserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_user"
}

// Schema defines the schema for the resource.
func (r *OrganizationUserResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an existing Anyscale Organization member's **membership** - that they are in the organization at all, not what they can do in it.\n\n" +
			"-> **Roles are managed elsewhere.** This resource has no writable attributes. To set a member's role, use the `anyscale_organization_user_role` resource, which owns `base_role` and the organization's deny roles. The two resources are deliberately split so only one of them ever writes a member's role. To *read* a member's current role without managing it, use the `anyscale_organization_user` or `anyscale_organization_users` data source, both of which still expose `base_role` and `additional_roles`.\n\n" +
			"~> **Warning:** Destroying this resource removes the user from the organization entirely, not just from Terraform state — it is a real, immediate `DELETE` against the Anyscale API, and it happens on any `terraform destroy` that reaches this resource, including as part of tearing down a larger configuration or a failed apply elsewhere in the same run. There is no undo; the user would need to be re-invited and re-accept to regain access. If you only want Terraform to stop managing a member without removing their access, use `terraform state rm` instead of `terraform destroy` - do this before running destroy on any configuration that contains this resource if that is not the outcome you want. This is a heavier operation than destroying an `anyscale_organization_invitation` - once accepted, an invitation and its resulting membership are separate objects, and only this resource's destroy actually revokes access.\n\n" +
			"**Important:** This resource cannot create new users. Users must first be added to the organization through:\n" +
			"1. An accepted `anyscale_organization_invitation`, or\n" +
			"2. SCIM provisioning\n\n" +
			"Once a user exists in the organization, import them using `terraform import` to bring their membership under Terraform management.\n\n" +
			"**Example Import:**\n```\nterraform import anyscale_organization_user.user <identity_id>\n```\n\n" +
			"**Directory-synced organizations:** If your organization manages membership via directory sync (the Policy API), this resource cannot manage members at all - any `terraform destroy` against it fails, and the error points you to the `anyscale policy set` command instead. See the [Anyscale policy CLI documentation](https://docs.anyscale.com/reference/cli/policy#policy-cli) for that command.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identity ID of the organization member. Used for import.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The email address of the organization member.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user ID of the organization member.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the organization member.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the member was added to the organization. Write-once: set on import and never re-read afterward, since the API has returned different values for it across reads.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *OrganizationUserResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *OrganizationUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError(
		"Direct Creation Not Supported",
		"Organization members cannot be created directly through the API.\n\n"+
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
			"4. Import the member to manage their permissions:\n"+
			"   terraform import anyscale_organization_user.new_user <identity_id>\n\n"+
			"Alternatively, if the user already exists in your organization (e.g., via SCIM),\n"+
			"you can import them directly using their identity_id.",
	)
}

// Read reads the current state of an organization member.
func (r *OrganizationUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationUserResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Reading organization user", map[string]interface{}{
		"identity_id": identityID,
	})

	// Fetch collaborator from API
	collaborator, err := r.findUserByID(ctx, identityID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			// Member was removed outside of Terraform
			tflog.Warn(ctx, "Organization user not found, removing from state", map[string]interface{}{
				"identity_id": identityID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading organization user",
			fmt.Sprintf("Could not read organization user %s: %s", identityID, err.Error()),
		)
		return
	}

	// Update state with API data. created_at is intentionally NOT refreshed
	// here - see the schema doc string; the API has returned different values
	// for it across reads, and it's treated as write-once (set on import only).
	applyUserIdentityFields(&state, collaborator)

	// Save updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// applyUserIdentityFields copies the identity fields that are safe to refresh
// from the API (email, user_id, name) into model. created_at is deliberately
// excluded and must be set separately only where appropriate (import) - see the
// schema doc string and task 4745d9fb: the API has returned different created_at
// values across reads for the same member, so re-syncing it from Read causes
// "Provider produced inconsistent result after apply".
//
// The role fields this used to copy (base_role, additional_roles) are gone from
// this resource entirely - roles are owned by anyscale_organization_user_role,
// and the organization_user(s) data sources still expose them read-only.
func applyUserIdentityFields(model *OrganizationUserResourceModel, collaborator *OrganizationCollaboratorResult) {
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
}

// Update is a deliberate no-op that copies plan into state.
//
// This resource has no writable attribute left: every attribute is Computed,
// so there is nothing a practitioner can change that would produce an update
// plan, and nothing for this method to write to the API. Role changes - the
// only thing Update ever did here, via
// PUT /api/v2/organization_collaborators/{identity_id} - now live on the
// anyscale_organization_user_role resource, which owns base_role and the
// organization's deny roles. Two resources writing the same org role would race
// each other, hence the split.
//
// The method is kept because resource.Resource requires it; the framework will
// not compile a resource without one. Copying plan -> state (rather than
// leaving resp.State untouched) is what keeps Terraform from reporting a
// null/inconsistent result in the event Core ever does call it.
func (r *OrganizationUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan OrganizationUserResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Update is a no-op for anyscale_organization_user; roles are managed by anyscale_organization_user_role", map[string]interface{}{
		"identity_id": plan.ID.ValueString(),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a member from the organization.
func (r *OrganizationUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationUserResourceModel

	// Read current state
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identityID := state.ID.ValueString()

	tflog.Info(ctx, "Removing organization user", map[string]interface{}{
		"identity_id":  identityID,
		"email_domain": getEmailDomain(state.Email.ValueString()),
	})

	// Delete the member - treat 404 as success (already removed)
	_, err := DoRequestRaw(ctx, r.client, "DELETE", fmt.Sprintf("/api/v2/organization_collaborators/%s", identityID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		resp.Diagnostics.AddError("Could Not Remove Organization User", collaboratorErrorDiagnosticDetail(err))
		return
	}

	tflog.Info(ctx, "Removed organization user", map[string]interface{}{
		"identity_id": identityID,
	})
}

// ImportState imports an existing organization member by identity_id.
func (r *OrganizationUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by identity_id
	identityID := req.ID

	tflog.Info(ctx, "Importing organization user", map[string]interface{}{
		"identity_id": identityID,
	})

	// Fetch member to validate it exists
	collaborator, err := r.findUserByID(ctx, identityID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error importing organization user",
			fmt.Sprintf("Could not find organization member with identity_id %s: %s\n\n"+
				"Tip: Use the anyscale_organization_user data source to find the identity_id:\n"+
				"  data \"anyscale_organization_user\" \"example\" {\n"+
				"    email = \"user@example.com\"\n"+
				"  }\n\n"+
				"Then import using: terraform import anyscale_organization_user.example <identity_id>",
				identityID, err.Error()),
		)
		return
	}

	// Set all attributes. Role fields (base_role/additional_roles) are
	// deliberately not among them - they are owned by
	// anyscale_organization_user_role, not this membership-only resource.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), identityID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), collaborator.Email)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("created_at"), collaborator.CreatedAt)...)

	if collaborator.UserID != nil && *collaborator.UserID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), *collaborator.UserID)...)
	}

	if collaborator.Name != nil && *collaborator.Name != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), *collaborator.Name)...)
	}

	tflog.Info(ctx, "Imported organization user", map[string]interface{}{
		"identity_id": identityID,
		"email":       collaborator.Email,
	})
}

// Helper functions

// listAllOrganizationCollaborators pages through the full /api/v2/organization_collaborators
// list via PaginatedRequest, across every page rather than stopping at the
// first, so a collaborator past page 1 is never mistaken for missing or
// silently dropped from a list. extraParams, if non-nil, is merged in
// alongside the page-size param (e.g. a server-side email or name filter);
// pass nil for an unfiltered listing. Shared with the organization_user(s)
// data sources - kept named after the API's own organization_collaborators
// vocabulary rather than this resource's organization_user name, since both
// consumers are calling the same backend concept.
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

// findUserByID fetches an organization member by identity_id. The API has no
// direct GET-by-identity_id endpoint for a single member, so this lists and
// filters for identity lookup, then hydrates the real role fields separately
// (see hydrateCollaboratorRoles) - identity lookup and role hydration are
// different concerns per architect ruling 1, and only the second one needs to
// move off the list endpoint.
func (r *OrganizationUserResource) findUserByID(ctx context.Context, identityID string) (*OrganizationCollaboratorResult, error) {
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

	return nil, fmt.Errorf("organization user not found")
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
// additional_roles is tri-state: populated = real roles, empty = queried and genuinely
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

// additionalRolesToList converts the tri-state AdditionalRoles field populated by
// hydrateCollaboratorRoles into its types.List representation: nil (undetermined) renders null,
// a non-nil (possibly empty) slice renders as a real list, never null, when genuinely
// queried-and-none. Shared by every collaborator/user model conversion so this tri-state handling
// can't drift between them.
func additionalRolesToList(ctx context.Context, additionalRoles []string) (types.List, diag.Diagnostics) {
	if additionalRoles == nil {
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, additionalRoles)
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
// this resource cannot work at all for that member, so a hint pointing at the
// actual tool is appended.
func collaboratorErrorDiagnosticDetail(err error) string {
	detail := extractAPIErrorDetail(err)

	if strings.Contains(detail, "Policy API") {
		return fmt.Sprintf("%s\n\nThis organization's member permissions are managed outside Terraform. Use 'anyscale policy set' to manage this member instead of this resource. See https://docs.anyscale.com/reference/cli/policy#policy-cli for details.", detail)
	}

	return detail
}
