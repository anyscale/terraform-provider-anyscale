package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
	_ resource.Resource                = &UserGroupResource{}
	_ resource.ResourceWithConfigure   = &UserGroupResource{}
	_ resource.ResourceWithImportState = &UserGroupResource{}
)

// NewUserGroupResource creates a new user group resource.
func NewUserGroupResource() resource.Resource {
	return &UserGroupResource{}
}

// UserGroupResource manages an Anyscale-managed user group. Membership is a
// separate resource (anyscale_user_group_members).
type UserGroupResource struct {
	client *Client
}

// UserGroupResourceModel describes the resource data model.
type UserGroupResourceModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

func (r *UserGroupResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_group"
}

func (r *UserGroupResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: userGroupsAlphaBanner +
			"Manages an Anyscale user group in the token's organization; manage its members with `anyscale_user_group_members`. " +
			"Destroying the group first removes every member, including members added outside Terraform. " +
			"Groups synced from an identity provider (SCIM) cannot be managed or imported; read them with the `anyscale_user_group` data source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user group ID (`ug_...`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The group name, 1-255 characters with no leading or trailing whitespace. Must be unique among the organization's groups. Changing it renames the group in place.",
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 255),
					userGroupNameTrimmedValidator{},
				},
			},
		},
	}
}

func (r *UserGroupResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *UserGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan UserGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	group, err := createUserGroup(ctx, r.client, name)
	if err != nil {
		var statusErr *UnexpectedStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusConflict {
			r.addDuplicateNameError(ctx, &resp.Diagnostics, name, err)
			return
		}
		AddAPIError(&resp.Diagnostics, "create user group", err)
		return
	}

	tflog.Info(ctx, "User group created", map[string]any{"user_group_id": group.ID})
	plan.ID = types.StringValue(group.ID)
	plan.Name = types.StringValue(group.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// addDuplicateNameError reports a 409 from create. The existing group's ID is
// looked up so the import command can be copied as-is; if that lookup fails
// the command keeps a placeholder. The create is never retried.
func (r *UserGroupResource) addDuplicateNameError(ctx context.Context, diags *diag.Diagnostics, name string, createErr error) {
	importID := "<group_id>"
	if existing, err := findUserGroupByName(ctx, r.client, name); err == nil && existing != nil {
		importID = existing.ID
		if isUserGroupSCIM(existing) {
			diags.AddError("User Group Name Already Exists",
				fmt.Sprintf("%s The existing group (%s) is synced from your identity provider and cannot be imported. Choose a different name.",
					extractAPIErrorDetail(createErr), existing.ID))
			return
		}
	} else if err != nil {
		tflog.Warn(ctx, "Could not look up the existing user group for the import hint", map[string]any{"error": err.Error()})
	}
	diags.AddError("User Group Name Already Exists",
		fmt.Sprintf("%s To manage the existing group with Terraform, import it:\n\n  terraform import <resource address> %s",
			extractAPIErrorDetail(createErr), importID))
}

func (r *UserGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state UserGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.ID.ValueString()
	group, err := getUserGroup(ctx, r.client, groupID)
	if err != nil {
		if isUserGroupNotFound(err) {
			tflog.Warn(ctx, "User group not found, removing from state", map[string]any{"user_group_id": groupID})
			resp.State.RemoveResource(ctx)
			return
		}
		AddAPIError(&resp.Diagnostics, "read user group", err)
		return
	}
	if isUserGroupSCIM(group) {
		AddConfigError(&resp.Diagnostics, "User Group Is Directory-Synced", userGroupSCIMDetail(groupID))
		return
	}

	state.ID = types.StringValue(group.ID)
	state.Name = types.StringValue(group.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state UserGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.ID.ValueString()
	// A 409 here names either a duplicate name or a directory-synced group;
	// the backend's detail says which, and AddAPIError surfaces it verbatim.
	group, err := renameUserGroup(ctx, r.client, groupID, plan.Name.ValueString())
	if err != nil {
		AddAPIError(&resp.Diagnostics, "rename user group", err)
		return
	}

	plan.ID = types.StringValue(group.ID)
	plan.Name = types.StringValue(group.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state UserGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.ID.ValueString()
	if err := removeAllUserGroupMembers(ctx, r.client, groupID); err != nil {
		if isUserGroupNotFound(err) {
			return
		}
		AddAPIError(&resp.Diagnostics, "remove members before deleting user group", err)
		return
	}
	if err := deleteUserGroup(ctx, r.client, groupID); err != nil {
		if isUserGroupNotFound(err) {
			return
		}
		AddAPIError(&resp.Diagnostics, "delete user group", err)
		return
	}
	tflog.Info(ctx, "User group deleted", map[string]any{"user_group_id": groupID})
}

func (r *UserGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID := req.ID
	group, err := getUserGroup(ctx, r.client, groupID)
	if err != nil {
		if isUserGroupNotFound(err) {
			AddConfigError(&resp.Diagnostics, "User Group Not Found",
				fmt.Sprintf("No user group exists with ID %q in this organization.", groupID))
			return
		}
		AddAPIError(&resp.Diagnostics, "read user group for import", err)
		return
	}
	if isUserGroupSCIM(group) {
		AddConfigError(&resp.Diagnostics, "User Group Is Directory-Synced", userGroupSCIMDetail(groupID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), group.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), group.Name)...)
}

// userGroupNameTrimmedValidator rejects a name with leading or trailing
// whitespace. The backend strips it on create and rename, so such a name
// would be stored differently from the config and the apply would fail as an
// inconsistent result.
type userGroupNameTrimmedValidator struct{}

func (v userGroupNameTrimmedValidator) Description(_ context.Context) string {
	return "must not have leading or trailing whitespace"
}

func (v userGroupNameTrimmedValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v userGroupNameTrimmedValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if trimUserGroupName(value) != value {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid User Group Name",
			fmt.Sprintf("The name %q has leading or trailing whitespace. The Anyscale API strips it, so remove it from the configuration.", value))
	}
}
