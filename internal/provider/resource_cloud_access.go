package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// anyscale_cloud_access - AUTHORITATIVE over one cloud's entire member list.
//
// NOT YET REGISTERED in provider.go. This file is the schema only; the CRUD
// methods below deliberately refuse to run. Registration happens once the
// reconcile and its guards are complete, because this is the one resource in
// the provider that can revoke real people's access to a real cloud at scale,
// and a half-wired version of it is worse than none.
//
// WHY THIS IS KEYED BY cloud_id AND NOT BY USER. Authority over a set requires
// one resource owning that whole set, so the resource's key must be the set's
// key. A per-user resource is structurally blind to a member it was never told
// about - nothing in its Read can surface an entry with no corresponding config
// block - so it could enforce "these are the clouds Alice belongs to" but never
// "these are the members of cloud X". Only the second guarantee catches someone
// added out-of-band, which is the entire point of authoritative mode.
//
// THE NESTING IS A BACKEND INVARIANT, NOT A DISPLAY CHOICE. Projects live under
// members rather than in a sibling resource because the backend enforces two
// things that only a single nesting resource can check at plan time:
//  1. A project role cannot exist without a cloud role on the same cloud. The
//     backend 403s creating a project collaborator unless the grantee already
//     passes a read-permission check on the parent cloud, and removing a cloud
//     collaborator cascades to delete their permissions on that cloud's
//     projects.
//  2. A member carrying the cloud_read_only deny role can only hold readonly on
//     that cloud's projects - anything else 422s. Split across two resources
//     that surfaces as a confusing apply-time 422; here it is a plan-time error.

// cloudAccessReadOnlyDenyRole is the cloud deny role that constrains which
// project roles a member may hold; cloudAccessReadOnlyProjectRole is the only
// project role compatible with it. Note the project vocabulary uses "readonly"
// while the cloud base-role vocabulary uses "writer" not "write" - the two
// scopes genuinely differ and are not interchangeable.
const (
	cloudAccessReadOnlyDenyRole    = "cloud_read_only"
	cloudAccessReadOnlyProjectRole = "readonly"
)

var (
	_ resource.Resource                   = &CloudAccessResource{}
	_ resource.ResourceWithValidateConfig = &CloudAccessResource{}
	_ resource.ResourceWithConfigure      = &CloudAccessResource{}
)

// NewCloudAccessResource returns a new anyscale_cloud_access resource.
func NewCloudAccessResource() resource.Resource {
	return &CloudAccessResource{}
}

// CloudAccessResource manages the complete member list of one cloud.
type CloudAccessResource struct {
	client *Client
}

// CloudAccessResourceModel maps the resource schema.
type CloudAccessResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	CloudID             types.String `tfsdk:"cloud_id"`
	AllowEmptyMemberSet types.Bool   `tfsdk:"allow_empty_member_set"`
	Member              types.Map    `tfsdk:"member"`
	UnmanagedGrants     types.List   `tfsdk:"unmanaged_grants"`
}

// CloudAccessMemberModel is one entry of the member map, keyed by email.
type CloudAccessMemberModel struct {
	BaseRole  types.String `tfsdk:"base_role"`
	DenyRoles types.List   `tfsdk:"deny_roles"`
	Projects  types.Map    `tfsdk:"projects"`
}

// CloudAccessUnmanagedGrantModel is one entry of the unmanaged_grants
// exception channel.
type CloudAccessUnmanagedGrantModel struct {
	Email     types.String `tfsdk:"email"`
	ProjectID types.String `tfsdk:"project_id"`
	Reason    types.String `tfsdk:"reason"`
}

func (r *CloudAccessResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_access"
}

func (r *CloudAccessResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the **complete** member list of one Anyscale cloud.\n\n" +
			"~> **This resource is authoritative.** Terraform owns who has access to this cloud. Any member " +
			"not declared in `member` is **revoked** on the next apply, including people granted access " +
			"through the Anyscale console. That is the point of this resource - it is how you make Terraform " +
			"the source of truth - but it means a mistake here removes real people's access.\n\n" +
			"### The most dangerous mistake, stated plainly\n\n" +
			"A typo in the `cloud_id` used as a `for_each` key is **not preventable by this resource**. " +
			"Terraform sees one resource destroyed and another created, and destroying an " +
			"`anyscale_cloud_access` correctly revokes that cloud's members - the provider cannot tell that " +
			"from a deliberate removal. Mitigate it outside the provider: review the plan, and put " +
			"`lifecycle { prevent_destroy = true }` on production clouds.\n\n" +
			"An accidentally *empty* `member` map is guarded, however - see `allow_empty_member_set`.\n\n" +
			"### Projects nest under members for a reason\n\n" +
			"A member's project roles live inside their entry because the backend requires a cloud grant " +
			"before a project grant on the same cloud, and revoking the cloud grant cascades to the " +
			"projects. Declaring a project role for someone with no cloud role is a plan-time error here " +
			"rather than a confusing failure partway through an apply.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the cloud whose access this resource manages. Same value as `cloud_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ID of the cloud whose member list this resource owns. Changing it replaces the " +
					"resource, which revokes the members of the *old* cloud - review such a plan carefully.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"allow_empty_member_set": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				// A deliberate boolean rather than the more elegant-looking
				// "distinguish member = {} from member omitted". Terraform CAN tell
				// those apart, but the threat this guards against - a typo'd or missing
				// upstream group name arriving through a for_each over an empty
				// collection - renders as EXACTLY member = {}. The accidental case and
				// the deliberate case would be byte-identical, so the opt-in signal has
				// to live outside the data path it is protecting. Same species as
				// aws_s3_bucket's force_destroy.
				MarkdownDescription: "Permit applying an **empty** `member` map to a cloud that currently has " +
					"members, which revokes all of them. Defaults to `false`, which refuses that apply with an " +
					"error naming the cloud and how many members would lose access.\n\n" +
					"This exists because an empty `member` map is usually an accident - a `for_each` over a " +
					"mistyped or missing group renders identically to a deliberate purge.\n\n" +
					"~> This attribute is **provider-side only**. It has no representation in Anyscale and is " +
					"never read back, so `terraform import` always lands it at `false`. If your configuration " +
					"sets it to `true`, the first plan after an import shows `false` -> `true`. That diff is " +
					"expected and is disclosing that Terraform is now permitted to empty this cloud's member list.",
			},
			"member": schema.MapNestedAttribute{
				Required: true,
				MarkdownDescription: "The complete set of members for this cloud, keyed by email address. " +
					"**Anyone not listed here is revoked.**\n\n" +
					"A map rather than a list so Terraform rejects duplicate keys at plan time. Note its " +
					"duplicate check is case-sensitive, so this resource additionally rejects two keys that " +
					"differ only in case - email identity is not case-sensitive and treating it as such is a " +
					"known source of split, half-granted access.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"base_role": schema.StringAttribute{
							Required: true,
							// No OneOf: the roles API is being actively extended, and a
							// hard-coded enum would reject a new backend role until a
							// provider release. Known values documented instead.
							MarkdownDescription: "The member's role on this cloud. Known values are `owner`, `writer`, " +
								"`collaborator`, `project_viewer`, `compute_config_viewer` and `workload_operator`. " +
								"Not validated against a fixed list, as the API is being extended.",
						},
						"deny_roles": schema.ListAttribute{
							ElementType: types.StringType,
							Optional:    true,
							MarkdownDescription: "Restrictions layered on top of `base_role`, never extra capability. " +
								"The only known value is `cloud_read_only`.\n\n" +
								"Unlike the organization-scope equivalent, cloud deny roles do **not** restrict " +
								"organization or project owners.\n\n" +
								"A member with `cloud_read_only` may only hold `readonly` on this cloud's projects; " +
								"anything else is rejected at plan time.",
						},
						"projects": schema.MapAttribute{
							ElementType: types.StringType,
							Optional:    true,
							MarkdownDescription: "This member's roles on projects under this cloud, keyed by project ID. " +
								"Known values are `owner`, `write` and `readonly`.\n\n" +
								"Note `write` here, not `writer` - the project vocabulary and the cloud vocabulary " +
								"genuinely differ, and sending the wrong one is rejected by the API.",
						},
					},
				},
			},
			"unmanaged_grants": schema.ListNestedAttribute{
				Computed: true,
				// A real attribute rather than a warning, so `length(...) > 0` is
				// expressible in config and alertable in CI. A warning recurs on every
				// plan and gets scrolled past.
				//
				// Every branch that populates this must set it explicitly before the
				// first State.Set - a Computed list-of-objects left unknown has crashed
				// this provider before. No UseStateForUnknown: this tracks whether a
				// revoke failed, which genuinely changes between applies.
				MarkdownDescription: "Grants this resource could not revoke, and why. Populated when an " +
					"undeclared member could not be removed - the Anyscale API can reach a state where a role " +
					"was granted but cannot be revoked, and it is not detectable before attempting the revoke.\n\n" +
					"The apply still converges rather than failing, because one unrevokable member would " +
					"otherwise block the whole set forever. Alert on this: `length(anyscale_cloud_access.x." +
					"unmanaged_grants) > 0` means someone has access Terraform intends them not to have.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"email": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Email of the member whose grant could not be revoked.",
						},
						"project_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Project the grant applies to, or null when it is the cloud-level grant.",
						},
						"reason": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "What the API reported when the revoke was attempted.",
						},
					},
				},
			},
		},
	}
}

func (r *CloudAccessResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// cloudAccessNotWiredDetail is the placeholder refusal. It exists so that this
// resource cannot act if it is registered before its guards are finished -
// failing loudly beats a reconcile that runs without its empty-set guard or its
// adds-before-removes ordering.
const cloudAccessNotWiredDetail = "anyscale_cloud_access is not finished and must not be used yet. " +
	"Its schema is in place but its reconcile logic, plan-time guards and revoke handling are not. " +
	"This is a provider bug if you are seeing it in a released version - please report it."

func (r *CloudAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Resource Not Implemented", cloudAccessNotWiredDetail)
}

func (r *CloudAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.AddError("Resource Not Implemented", cloudAccessNotWiredDetail)
}

func (r *CloudAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Resource Not Implemented", cloudAccessNotWiredDetail)
}

func (r *CloudAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddError("Resource Not Implemented", cloudAccessNotWiredDetail)
}

// ValidateConfig enforces the invariants that can be checked from configuration
// alone. The empty-member-set guard is NOT here and cannot be: it has to compare
// config against prior state ("is the cloud currently non-empty?"), and
// ValidateConfigRequest carries only Config. That guard lives in ModifyPlan,
// whose request carries Config, State and Plan.
func (r *CloudAccessResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config CloudAccessResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown at validate time means a value computed from another resource; there
	// is nothing to check yet and Terraform will validate again once it is known.
	if config.Member.IsNull() || config.Member.IsUnknown() {
		return
	}

	members := make(map[string]CloudAccessMemberModel, len(config.Member.Elements()))
	resp.Diagnostics.Append(config.Member.ElementsAs(ctx, &members, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Terraform already rejects duplicate map keys, but its comparison is a
	// case-SENSITIVE string match, so "Alice@x.com" and "alice@x.com" are two
	// valid keys to it and one identity to Anyscale. Left alone, the second grant
	// silently overwrites the first and the config looks like it declared two
	// different people. Catch it here, naming BOTH spellings so the user can see
	// which line to fix.
	seen := make(map[string]string, len(members))
	for email := range members {
		folded := strings.ToLower(strings.TrimSpace(email))
		if first, dup := seen[folded]; dup {
			resp.Diagnostics.AddAttributeError(
				path.Root("member"),
				"Duplicate Member Email",
				fmt.Sprintf("%q and %q differ only in capitalization or surrounding whitespace, but Anyscale treats "+
					"them as the same person. Terraform's own duplicate-key check is case-sensitive and will not "+
					"catch this. Remove one of them.", first, email),
			)
			continue
		}
		seen[folded] = email
	}

	for email, m := range members {
		memberPath := path.Root("member").AtMapKey(email)

		// base_role is Required, so it cannot be null - but Required does not stop an
		// empty string, which would reach the API as a role that does not exist.
		if !m.BaseRole.IsUnknown() && strings.TrimSpace(m.BaseRole.ValueString()) == "" {
			resp.Diagnostics.AddAttributeError(
				memberPath.AtName("base_role"),
				"Empty Cloud Role",
				fmt.Sprintf("Member %q has an empty base_role. Every member of a cloud must have a role; if they "+
					"should not have access, remove them from the member map instead.", email),
			)
		}

		if m.Projects.IsNull() || m.Projects.IsUnknown() || m.DenyRoles.IsNull() || m.DenyRoles.IsUnknown() {
			continue
		}

		var denyRoles []string
		resp.Diagnostics.Append(m.DenyRoles.ElementsAs(ctx, &denyRoles, false)...)
		var projects map[string]string
		resp.Diagnostics.Append(m.Projects.ElementsAs(ctx, &projects, false)...)
		if resp.Diagnostics.HasError() {
			return
		}

		// A member carrying cloud_read_only can only hold readonly on this cloud's
		// projects - the backend 422s anything else. This is genuinely a cross-field
		// constraint spanning a member's cloud role and their project roles, which is
		// exactly why the two live in one resource: split apart, this could only ever
		// surface as a confusing failure partway through an apply.
		if !slices.Contains(denyRoles, cloudAccessReadOnlyDenyRole) {
			continue
		}
		for projectID, level := range projects {
			if level == cloudAccessReadOnlyProjectRole {
				continue
			}
			resp.Diagnostics.AddAttributeError(
				memberPath.AtName("projects").AtMapKey(projectID),
				"Project Role Conflicts With cloud_read_only",
				fmt.Sprintf("Member %q has the %q deny role on this cloud, so they can only be granted %q on its "+
					"projects, but project %s grants %q. Anyscale rejects this combination. Either remove %q from "+
					"their deny_roles or lower this project role to %q.",
					email, cloudAccessReadOnlyDenyRole, cloudAccessReadOnlyProjectRole, projectID, level,
					cloudAccessReadOnlyDenyRole, cloudAccessReadOnlyProjectRole),
			)
		}
	}
}
