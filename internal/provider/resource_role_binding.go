package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                     = &RoleBindingResource{}
	_ resource.ResourceWithConfigure        = &RoleBindingResource{}
	_ resource.ResourceWithImportState      = &RoleBindingResource{}
	_ resource.ResourceWithConfigValidators = &RoleBindingResource{}
)

// NewRoleBindingResource creates a new role binding resource.
func NewRoleBindingResource() resource.Resource {
	return &RoleBindingResource{}
}

// RoleBindingResource grants one role to one user group on one organization,
// cloud or project. The API never edits a binding, so every argument forces
// replacement.
type RoleBindingResource struct {
	client *Client
}

// RoleBindingResourceModel describes the resource data model.
type RoleBindingResourceModel struct {
	ID             types.String `tfsdk:"id"`
	UserGroupID    types.String `tfsdk:"user_group_id"`
	RoleID         types.String `tfsdk:"role_id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	CloudID        types.String `tfsdk:"cloud_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Origin         types.String `tfsdk:"origin"`
	CreatedBy      types.String `tfsdk:"created_by"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// scope returns the resource type and ID the binding is held on: whichever of
// the three scope attributes is set. The config validator guarantees exactly
// one is.
func (m *RoleBindingResourceModel) scope() (resourceType, resourceID string) {
	switch {
	case !m.CloudID.IsNull():
		return roleBindingResourceCloud, m.CloudID.ValueString()
	case !m.ProjectID.IsNull():
		return roleBindingResourceProject, m.ProjectID.ValueString()
	default:
		return roleBindingResourceOrganization, m.OrganizationID.ValueString()
	}
}

// setFromAPI sets every attribute from a full binding, as create and import
// return it.
func (m *RoleBindingResourceModel) setFromAPI(b *roleBindingResult) {
	m.ID = types.StringValue(b.ID)
	m.UserGroupID = types.StringValue(b.PrincipalID)
	m.RoleID = types.StringValue(b.RoleID)
	m.OrganizationID = types.StringNull()
	m.CloudID = types.StringNull()
	m.ProjectID = types.StringNull()
	switch b.ResourceType {
	case roleBindingResourceCloud:
		m.CloudID = types.StringValue(b.ResourceID)
	case roleBindingResourceProject:
		m.ProjectID = types.StringValue(b.ResourceID)
	default:
		m.OrganizationID = types.StringValue(b.ResourceID)
	}
	m.Origin = types.StringPointerValue(b.Origin)
	m.CreatedBy = types.StringPointerValue(b.CreatedBy)
	m.CreatedAt = types.StringValue(b.CreatedAt)
}

func (r *RoleBindingResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_binding"
}

func (r *RoleBindingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	scopeDescription := "Specify exactly one of `organization_id`, `cloud_id` or `project_id`. Changing it replaces the binding."

	resp.Schema = schema.Schema{
		MarkdownDescription: roleBindingsAlphaBanner +
			"Grants one role to one user group on an organization, cloud or project. " +
			"Access granted this way is not visible to `anyscale_cloud_access`; see the [RBAC guide](../guides/rbac.md). " +
			"A binding cannot be edited, so changing any argument replaces it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The role binding ID (`rb_...`).",
				PlanModifiers:       keep,
			},
			"user_group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The user group that holds the role (`ug_...`). Groups synced from an identity provider are accepted.",
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"role_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The role to grant (`rol_...`); look one up by name with the `anyscale_role` data source. " +
					"Changing it replaces the binding, leaving the group without the role in between unless the resource sets `lifecycle { create_before_destroy = true }`.",
				PlanModifiers: replace,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"organization_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Grant the role on the organization: the token's own, as returned by the `anyscale_organization` data source. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Grant the role on this cloud. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Grant the role on this project. " + scopeDescription,
				PlanModifiers:       replace,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"origin": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "How the grant was made, as the API records it, for example `imperative`. Null when the API does not know.",
				PlanModifiers:       keep,
			},
			"created_by": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the user who made the grant. Null when the API does not know.",
				PlanModifiers:       keep,
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the grant was made.",
				PlanModifiers:       keep,
			},
		},
	}
}

func (r *RoleBindingResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("organization_id"),
			path.MatchRoot("cloud_id"),
			path.MatchRoot("project_id"),
		),
	}
}

func (r *RoleBindingResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *RoleBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RoleBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceType, resourceID := plan.scope()
	binding, err := createRoleBinding(ctx, r.client, createRoleBindingRequest{
		PrincipalType: roleBindingPrincipalUserGroup,
		PrincipalID:   plan.UserGroupID.ValueString(),
		RoleID:        plan.RoleID.ValueString(),
		ResourceType:  resourceType,
		ResourceID:    resourceID,
	})
	if err != nil {
		var statusErr *UnexpectedStatusError
		switch {
		case errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusConflict:
			r.addDuplicateError(ctx, &resp.Diagnostics, &plan, err)
		case isRoleBindingsDisabled(err):
			// Create also answers 404 for an unknown group or role, so ask
			// whether the feature is on before calling it off.
			r.addCreateNotFoundError(ctx, &resp.Diagnostics, err)
		default:
			AddAPIError(&resp.Diagnostics, "create role binding", err)
		}
		return
	}

	tflog.Info(ctx, "Role binding created", map[string]any{"role_binding_id": binding.ID})
	plan.setFromAPI(binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// addCreateNotFoundError reports a 404 from create: the feature is off, or
// the group or role does not exist, in which case the API's own detail says
// which.
func (r *RoleBindingResource) addCreateNotFoundError(ctx context.Context, diags *diag.Diagnostics, createErr error) {
	enabled, err := roleBindingsEnabled(ctx, r.client)
	if err != nil {
		addRoleBindingsProbeFailedError(diags, "create role binding", createErr, err)
		return
	}
	if !enabled {
		addRoleBindingsDisabledError(diags, false)
		return
	}
	AddAPIError(diags, "create role binding", createErr)
}

// addDuplicateError reports a 409 from create: the group already holds this
// role on this resource. The existing binding is looked up so the import
// command can be copied as-is; if that lookup fails the command keeps a
// placeholder. The existing binding is never adopted.
func (r *RoleBindingResource) addDuplicateError(ctx context.Context, diags *diag.Diagnostics, plan *RoleBindingResourceModel, createErr error) {
	importID := "<role_binding_id>"
	resourceType, resourceID := plan.scope()
	bindings, err := listRoleBindingsForGroup(ctx, r.client, resourceType, resourceID, plan.UserGroupID.ValueString())
	if err != nil {
		tflog.Warn(ctx, "Could not look up the existing role binding for the import hint", map[string]any{"error": err.Error()})
	}
	for _, b := range bindings {
		if b.RoleID == plan.RoleID.ValueString() {
			importID = b.ID
			break
		}
	}
	diags.AddError("Role Binding Already Exists",
		fmt.Sprintf("%s To manage the existing binding with Terraform, import it:\n\n  terraform import <resource address> %s",
			extractAPIErrorDetail(createErr), importID))
}

func (r *RoleBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RoleBindingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bindingID := state.ID.ValueString()
	resourceType, resourceID := state.scope()
	binding, err := findRoleBindingForGroup(ctx, r.client, resourceType, resourceID, state.UserGroupID.ValueString(), bindingID)
	if err != nil {
		if isRoleBindingsDisabled(err) {
			// Never RemoveResource on the 404 alone: the flag being turned
			// off would otherwise drop every binding from state.
			enabled, probeErr := roleBindingsEnabled(ctx, r.client)
			switch {
			case probeErr != nil:
				addRoleBindingsProbeFailedError(&resp.Diagnostics, "read role binding", err, probeErr)
			case !enabled:
				addRoleBindingsDisabledError(&resp.Diagnostics, true)
			default:
				// The feature is on, so the 404 names the group or the
				// resource the binding was held on, which is gone.
				tflog.Warn(ctx, "Role binding scope or group not found, removing from state",
					map[string]any{"role_binding_id": bindingID, "detail": extractAPIErrorDetail(err)})
				resp.State.RemoveResource(ctx)
			}
			return
		}
		if isForbidden(err) {
			AddAPIError(&resp.Diagnostics, "read role binding", err)
			resp.Diagnostics.AddError("Role Binding Not Readable",
				fmt.Sprintf("Reading bindings on %s %q requires managing IAM on it. "+
					"If the %s was deleted outside Terraform, remove this binding from state with `terraform state rm`.",
					resourceType, resourceID, resourceType))
			return
		}
		AddAPIError(&resp.Diagnostics, "read role binding", err)
		return
	}
	if binding == nil {
		// Revoked outside Terraform, or the group was deleted: a deleted
		// group answers with an empty listing.
		tflog.Warn(ctx, "Role binding not found, removing from state", map[string]any{"role_binding_id": bindingID})
		resp.State.RemoveResource(ctx)
		return
	}

	// The arguments are immutable, so only the computed fields the listing
	// carries are refreshed.
	state.Origin = types.StringPointerValue(binding.Origin)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RoleBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every argument forces replacement, so Terraform never plans an update.
	resp.Diagnostics.AddError("Role Binding Cannot Be Updated",
		"Every argument of anyscale_role_binding forces replacement, so an in-place update was not expected. Please report this issue to the provider developers.")
}

func (r *RoleBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RoleBindingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bindingID := state.ID.ValueString()
	err := deleteRoleBinding(ctx, r.client, bindingID)
	if err == nil {
		tflog.Info(ctx, "Role binding deleted", map[string]any{"role_binding_id": bindingID})
		return
	}
	if isRoleBindingsDisabled(err) {
		r.handleDeleteNotFound(ctx, &resp.Diagnostics, bindingID, err)
		return
	}
	if !isForbidden(err) {
		AddAPIError(&resp.Diagnostics, "delete role binding", err)
		return
	}

	// The backend checks the binding's own permission before looking it up,
	// so a binding that is already revoked answers 403. The listing tells
	// that apart from a real refusal.
	resourceType, resourceID := state.scope()
	binding, listErr := findRoleBindingForGroup(ctx, r.client, resourceType, resourceID, state.UserGroupID.ValueString(), bindingID)
	switch {
	case listErr != nil && isRoleBindingsDisabled(listErr):
		r.handleDeleteNotFound(ctx, &resp.Diagnostics, bindingID, listErr)
	case listErr != nil:
		tflog.Warn(ctx, "Could not check whether the role binding is already revoked", map[string]any{"error": listErr.Error()})
		AddAPIError(&resp.Diagnostics, "delete role binding", err)
	case binding == nil:
		tflog.Info(ctx, "Role binding already revoked", map[string]any{"role_binding_id": bindingID})
	default:
		AddAPIError(&resp.Diagnostics, "delete role binding", err)
	}
}

// handleDeleteNotFound settles a 404 met while deleting. With the feature on,
// the binding, its group or its resource is gone, so the delete has nothing
// left to do; with it off, nothing can be revoked and the binding stays in
// state.
func (r *RoleBindingResource) handleDeleteNotFound(ctx context.Context, diags *diag.Diagnostics, bindingID string, notFoundErr error) {
	enabled, probeErr := roleBindingsEnabled(ctx, r.client)
	switch {
	case probeErr != nil:
		addRoleBindingsProbeFailedError(diags, "delete role binding", notFoundErr, probeErr)
	case !enabled:
		addRoleBindingsDisabledError(diags, true)
	default:
		tflog.Info(ctx, "Role binding already gone", map[string]any{"role_binding_id": bindingID, "detail": extractAPIErrorDetail(notFoundErr)})
	}
}

func (r *RoleBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	bindingID := req.ID
	binding, err := getRoleBinding(ctx, r.client, bindingID)
	if err != nil {
		switch {
		case isRoleBindingsDisabled(err):
			enabled, probeErr := roleBindingsEnabled(ctx, r.client)
			switch {
			case probeErr != nil:
				addRoleBindingsProbeFailedError(&resp.Diagnostics, "read role binding for import", err, probeErr)
			case !enabled:
				addRoleBindingsDisabledError(&resp.Diagnostics, false)
			default:
				AddAPIError(&resp.Diagnostics, "read role binding for import", err)
			}
		case isForbidden(err):
			// A binding that does not exist answers 403 here as well.
			AddConfigError(&resp.Diagnostics, "Role Binding Not Found",
				fmt.Sprintf("No role binding %q could be read: it does not exist, was revoked, or you do not manage IAM on the resource it is held on. %s",
					bindingID, extractAPIErrorDetail(err)))
		default:
			AddAPIError(&resp.Diagnostics, "read role binding for import", err)
		}
		return
	}
	if binding.PrincipalType != roleBindingPrincipalUserGroup {
		AddConfigError(&resp.Diagnostics, "Role Binding Is Not Held by a User Group",
			fmt.Sprintf("Role binding %q is held by a %s, not a user group. anyscale_role_binding manages only user group bindings.",
				bindingID, binding.PrincipalType))
		return
	}

	var state RoleBindingResourceModel
	state.setFromAPI(binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
