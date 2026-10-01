package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &UserGroupMembersResource{}
	_ resource.ResourceWithConfigure   = &UserGroupMembersResource{}
	_ resource.ResourceWithImportState = &UserGroupMembersResource{}
)

// NewUserGroupMembersResource creates a new user group members resource.
func NewUserGroupMembersResource() resource.Resource {
	return &UserGroupMembersResource{}
}

// UserGroupMembersResource owns the complete member set of one user group:
// members present in the group but absent from the configuration are
// removed. Members are keyed by email and resolved to user IDs on every
// write.
type UserGroupMembersResource struct {
	client *Client
}

// UserGroupMembersResourceModel describes the resource data model.
type UserGroupMembersResourceModel struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	Members types.Set    `tfsdk:"members"`
}

func (r *UserGroupMembersResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_group_members"
}

func (r *UserGroupMembersResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: userGroupsAlphaBanner +
			"Manages the complete member list of an Anyscale user group. This resource is authoritative: members added outside Terraform are removed on the next apply, and destroying it empties the group without deleting it. " +
			"Use at most one per group. Directory-synced (SCIM) groups are rejected, because their identity provider owns membership.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user group ID; the same value as `group_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The ID of the user group whose members this resource manages. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"members": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Email addresses of the group's members. Each must already belong to the organization; invite new users first. " +
					"Emails match case-insensitively. An empty set removes every member. A member who leaves the organization drops out of this set on the next refresh.",
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
					caseInsensitiveUniqueEmailsValidator{},
				},
			},
		},
	}
}

func (r *UserGroupMembersResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *UserGroupMembersResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan UserGroupMembersResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.applyMembers(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = plan.GroupID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserGroupMembersResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state UserGroupMembersResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.GroupID.ValueString()
	group, err := getUserGroup(ctx, r.client, groupID)
	if err != nil {
		if isUserGroupNotFound(err) {
			tflog.Warn(ctx, "User group not found, removing its members resource from state", map[string]any{"user_group_id": groupID})
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

	members, _, err := getUserGroupMembers(ctx, r.client, groupID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read user group members", err)
		return
	}

	var prior []string
	resp.Diagnostics.Append(state.Members.ElementsAs(ctx, &prior, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	emails := memberEmailsPreferringSpelling(prior, members)
	set, diags := types.SetValueFrom(ctx, types.StringType, emails)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Members = set
	state.ID = state.GroupID
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserGroupMembersResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan UserGroupMembersResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deltas are computed against a fresh read, not prior state, so an
	// out-of-band change made since the last refresh is still reconciled. A
	// case-only edit of an email resolves to the same user and writes nothing.
	r.applyMembers(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = plan.GroupID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserGroupMembersResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state UserGroupMembersResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.GroupID.ValueString()
	if err := removeAllUserGroupMembers(ctx, r.client, groupID); err != nil {
		if isUserGroupNotFound(err) {
			return
		}
		AddAPIError(&resp.Diagnostics, "remove user group members", err)
	}
}

func (r *UserGroupMembersResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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

	members, _, err := getUserGroupMembers(ctx, r.client, groupID)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "read user group members for import", err)
		return
	}
	set, diags := types.SetValueFrom(ctx, types.StringType, memberEmailsPreferringSpelling(nil, members))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &UserGroupMembersResourceModel{
		ID:      types.StringValue(group.ID),
		GroupID: types.StringValue(group.ID),
		Members: set,
	})...)
}

// applyMembers makes the group's membership equal plan.Members: it checks the
// group is Anyscale-managed, resolves every email to a user ID, and sends the
// add and remove deltas against current membership. Both member routes are
// idempotent, so re-applying after a partial failure converges.
func (r *UserGroupMembersResource) applyMembers(ctx context.Context, plan *UserGroupMembersResourceModel, diags *diag.Diagnostics) {
	groupID := plan.GroupID.ValueString()

	group, err := getUserGroup(ctx, r.client, groupID)
	if err != nil {
		if isUserGroupNotFound(err) {
			AddConfigError(diags, "User Group Not Found",
				fmt.Sprintf("No user group exists with ID %q in this organization.", groupID))
			return
		}
		AddAPIError(diags, "read user group", err)
		return
	}
	if isUserGroupSCIM(group) {
		AddConfigError(diags, "User Group Is Directory-Synced", userGroupSCIMDetail(groupID))
		return
	}

	var emails []string
	diags.Append(plan.Members.ElementsAs(ctx, &emails, false)...)
	if diags.HasError() {
		return
	}

	desired := map[string]struct{}{}
	resolvedEmailByID := map[string]string{}
	if len(emails) > 0 {
		resolved, err := resolveOrgUserIDsByEmail(ctx, r.client, emails)
		if err != nil {
			AddConfigError(diags, "Cannot Resolve User Group Members",
				fmt.Sprintf("Some members of user group %s could not be resolved: %s.", groupID, err))
			return
		}
		for email, userID := range resolved {
			desired[userID] = struct{}{}
			resolvedEmailByID[userID] = email
		}
	}

	current, _, err := getUserGroupMembers(ctx, r.client, groupID)
	if err != nil {
		AddAPIError(diags, "read user group members", err)
		return
	}
	currentIDs := make(map[string]struct{}, len(current))
	for _, m := range current {
		currentIDs[m.UserID] = struct{}{}
	}

	var toAdd, toRemove []string
	for id := range desired {
		if _, ok := currentIDs[id]; !ok {
			toAdd = append(toAdd, id)
		}
	}
	for id := range currentIDs {
		if _, ok := desired[id]; !ok {
			toRemove = append(toRemove, id)
		}
	}
	sort.Strings(toAdd)
	sort.Strings(toRemove)

	tflog.Debug(ctx, "Reconciling user group members", map[string]any{
		"user_group_id": groupID,
		"add":           len(toAdd),
		"remove":        len(toRemove),
	})

	if err := addUserGroupMembers(ctx, r.client, groupID, toAdd); err != nil {
		// The organization member listing used to resolve emails includes
		// Anyscale support users, but the member routes do not treat them as
		// organization members and reject them with 404 "User IDs not found".
		// Name the emails rather than the user IDs the backend reports.
		if errors.Is(err, ErrNotFound) && !isUserGroupNotFound(err) {
			AddConfigError(diags, "Cannot Add User Group Members",
				fmt.Sprintf("The Anyscale API rejected some members of user group %s as not belonging to the organization: %s. "+
					"This happens for Anyscale support users and for users removed from the organization since this apply started. Remove them from members.",
					groupID, emailsForUserIDsInError(extractAPIErrorDetail(err), resolvedEmailByID)))
			return
		}
		AddAPIError(diags, "add user group members", err)
		return
	}
	if err := removeUserGroupMembers(ctx, r.client, groupID, toRemove); err != nil {
		AddAPIError(diags, "remove user group members", err)
		return
	}
}

// emailsForUserIDsInError lists the emails of the resolved user IDs named in
// the backend's "User IDs not found: id1, id2" detail. If none can be mapped,
// the detail itself is returned so the cause is never hidden.
func emailsForUserIDsInError(detail string, emailByID map[string]string) string {
	var named []string
	for id, email := range emailByID {
		if strings.Contains(detail, id) {
			named = append(named, email)
		}
	}
	if len(named) == 0 {
		return detail
	}
	sort.Strings(named)
	return strings.Join(named, ", ")
}

// memberEmailsPreferringSpelling returns the API members' emails, using the
// spelling from prior wherever it names the same address case-insensitively.
// The backend stores emails lowercased; keeping the configured spelling stops
// a mixed-case config from planning a change on every refresh.
func memberEmailsPreferringSpelling(prior []string, members []userGroupMemberAPI) []string {
	spelling := make(map[string]string, len(prior))
	for _, p := range prior {
		spelling[strings.ToLower(p)] = p
	}
	emails := make([]string, 0, len(members))
	for _, m := range members {
		if p, ok := spelling[strings.ToLower(m.UserEmail)]; ok {
			emails = append(emails, p)
			continue
		}
		emails = append(emails, m.UserEmail)
	}
	return emails
}

// caseInsensitiveUniqueEmailsValidator rejects a set holding the same email
// twice in different case. Both entries resolve to one member, so one of them
// could never appear in state and the plan would never converge.
type caseInsensitiveUniqueEmailsValidator struct{}

func (v caseInsensitiveUniqueEmailsValidator) Description(_ context.Context) string {
	return "emails must be unique, ignoring case"
}

func (v caseInsensitiveUniqueEmailsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v caseInsensitiveUniqueEmailsValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	seen := map[string]string{}
	for _, elem := range req.ConfigValue.Elements() {
		s, ok := elem.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		email := s.ValueString()
		folded := strings.ToLower(email)
		if first, dup := seen[folded]; dup {
			resp.Diagnostics.AddAttributeError(req.Path, "Duplicate Member Email",
				fmt.Sprintf("%q and %q are the same address; list it once.", first, email))
			continue
		}
		seen[folded] = email
	}
}
