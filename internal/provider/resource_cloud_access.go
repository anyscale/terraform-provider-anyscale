package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
// NOT YET REGISTERED in provider.go. Read and ImportState are implemented; the
// three WRITE methods deliberately refuse to run. Registration happens once the
// reconcile and its guards are complete, because this is the one resource in
// the provider that can revoke real people's access to a real cloud at scale,
// and a half-wired version of it is worse than none.
//
// The read half is built and landed first on purpose, and the ordering is a
// safety property rather than convenience: a resource that can only read cannot
// revoke anyone, so it can be reviewed, tested and even exercised against real
// infrastructure with no way to cause harm. Nothing here reaches a practitioner
// until registration.
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

// cloudAccessProjectWriteRole is the project-scope spelling of the write tier;
// cloudAccessCloudWriteRole is the cloud-scope spelling of an unrelated role
// that happens to be a near-homograph of it.
//
// Only ONE direction of this confusion is rejected below - a project role of
// "writer", which the API answers with a 422. The reverse (a cloud base_role of
// "write") is deliberately NOT rejected: base_role carries no OneOf because the
// roles API is actively extended, and a validator that hard-rejects a value the
// backend might legalize later can only be unblocked by a provider release. The
// project vocabulary is a fixed three-value tier list, so it does not carry that
// risk.
const (
	cloudAccessProjectWriteRole = "write"
	cloudAccessCloudWriteRole   = "writer"
)

var (
	_ resource.Resource                   = &CloudAccessResource{}
	_ resource.ResourceWithValidateConfig = &CloudAccessResource{}
	_ resource.ResourceWithModifyPlan     = &CloudAccessResource{}
	_ resource.ResourceWithConfigure      = &CloudAccessResource{}
	_ resource.ResourceWithImportState    = &CloudAccessResource{}
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
								"genuinely differ. `writer` on a project is rejected at plan time with a did-you-mean, " +
								"rather than left to fail as an API error partway through an apply.\n\n" +
								"Keyed by project **ID** rather than name, even though `member` is keyed by email: " +
								"project names are not reliably unique across an organization's clouds, so a name would " +
								"not identify one project. This resource therefore spans three identifier namespaces - " +
								"cloud ID, member email, project ID - each the stable key for what it addresses.",
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

// cloudAccessNotWiredDetail is the refusal shared by the three write methods. It
// exists so that this resource cannot act if it is registered before its guards
// are finished - failing loudly beats a reconcile that runs without its
// empty-set guard, its auto_add_user check or its adds-before-removes ordering.
//
// Read and ImportState are deliberately NOT covered by it: neither can revoke
// anyone, so neither carries the risk this refusal is protecting against.
const cloudAccessNotWiredDetail = "anyscale_cloud_access cannot yet write. Its schema, validation, refresh and " +
	"import are in place, but its reconcile logic and revoke handling are not, so it must not be used to change " +
	"access. This is a provider bug if you are seeing it in a released version - please report it."

func (r *CloudAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Cloud Access Writes Not Implemented", cloudAccessNotWiredDetail)
}

// Read refreshes the whole member list from the API.
//
// It is the read half of a resource whose write half can revoke real people, so
// two rules govern it. First, only a genuine not-found removes the resource from
// state - a 500 or a timeout surfaces as a diagnostic, because treating any
// error as "gone" would let a transient failure evict the resource and let the
// next apply re-assert Terraform's view over whatever changed out of band.
// Second, a member the API reports is always represented, even when its roles
// cannot be: an existing member missing from state is invisible to an
// authoritative resource, which is the one failure here that loses someone's
// access rather than reporting it.
func (r *CloudAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CloudAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cloudID := state.CloudID.ValueString()
	if cloudID == "" {
		cloudID = state.ID.ValueString()
	}
	if cloudID == "" {
		resp.Diagnostics.AddError(
			"Cloud Access State Has No Cloud ID",
			"This resource's state carries neither cloud_id nor id, so there is nothing to refresh. Remove it from "+
				"state with 'terraform state rm' and import it again.",
		)
		return
	}

	remote, err := listCloudAccessMembers(ctx, r.client, cloudID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// The cloud itself is gone, which takes its member list with it. This is
			// the only branch that drops the resource from state.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Could Not Read Cloud Access",
			fmt.Sprintf("Reading the members of cloud %s failed: %s\n\nThe resource is left in state unchanged. Only a "+
				"genuine not-found removes it, so that a transient failure cannot cause the next apply to re-assert "+
				"Terraform's view over an out-of-band change.", cloudID, extractAPIErrorDetail(err)),
		)
		return
	}

	memberMap, diags := cloudAccessMembersToState(ctx, remote, state.Member)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.ID = types.StringValue(cloudID)
	state.CloudID = types.StringValue(cloudID)
	state.Member = memberMap

	// allow_empty_member_set is provider-side only, with no representation in
	// Anyscale, so a refresh has nothing to read it from and must leave whatever
	// is in state alone. The one exception is a state that has no value at all -
	// the shape a fresh import lands in - where the schema's own documented
	// contract is that it comes back as false.
	if state.AllowEmptyMemberSet.IsNull() || state.AllowEmptyMemberSet.IsUnknown() {
		state.AllowEmptyMemberSet = types.BoolValue(false)
	}

	// unmanaged_grants records what the LAST APPLY could not revoke, which is not
	// something any read can recompute - so a refresh preserves it rather than
	// clearing it. Clearing it on refresh would silence an alert about access
	// Terraform intends someone not to have, which is precisely the thing this
	// attribute exists to keep visible. Null (again, the fresh-import shape)
	// becomes an empty list, never left unknown: a Computed list-of-objects left
	// unknown has crashed this provider before.
	if state.UnmanagedGrants.IsNull() || state.UnmanagedGrants.IsUnknown() {
		state.UnmanagedGrants = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ImportState takes the cloud ID and lets Read populate the member list.
//
// The two provider-side attributes are set here rather than left null, because
// Read deliberately preserves whatever it finds in them and a null would
// survive the refresh. Both land on the value the schema documents for an
// import: allow_empty_member_set false (so an imported resource cannot empty a
// cloud until someone opts in, which is why a config that sets true shows a
// false -> true diff on its first plan), and unmanaged_grants empty (nothing has
// been attempted yet, so nothing has failed).
func (r *CloudAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cloudID := strings.TrimSpace(req.ID)
	if cloudID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			"Import anyscale_cloud_access using the cloud's ID, for example 'terraform import "+
				"anyscale_cloud_access.example cld_abc123'.",
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.StringValue(cloudID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_id"), types.StringValue(cloudID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("allow_empty_member_set"), types.BoolValue(false))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("unmanaged_grants"),
		types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{}))...)
}

func (r *CloudAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Cloud Access Writes Not Implemented", cloudAccessNotWiredDetail)
}

func (r *CloudAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddError("Cloud Access Writes Not Implemented", cloudAccessNotWiredDetail)
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

		// Both remaining checks read project roles through resolvedStringMapValues
		// rather than ElementsAs, and deny roles through resolvedStringListValues,
		// because a value interpolated from something Terraform has not resolved yet
		// is unknown at validate time and converting it to a Go string is an error.
		// Reporting that as a config error would fail a legitimate config; Terraform
		// validates again once the value is known.
		projects := resolvedStringMapValues(m.Projects)
		denyRoles := resolvedStringListValues(m.DenyRoles)

		// The project-vocabulary check runs BEFORE the deny-role check below,
		// deliberately: a "writer" project role is wrong whether or not the member
		// has any deny roles, and the deny-role check does nothing for a member with
		// none - which is the common case. Ordering these the other way, with the
		// early-continue the deny-role check needs, would leave this validator silent
		// for most configs that hit the typo.
		for projectID, level := range projects {
			if level != cloudAccessCloudWriteRole {
				continue
			}
			resp.Diagnostics.AddAttributeError(
				memberPath.AtName("projects").AtMapKey(projectID),
				"Cloud Role Spelling Used For A Project Role",
				fmt.Sprintf("Member %q is granted %q on project %s, but %q is a *cloud* role and Anyscale rejects it "+
					"on a project. Did you mean %q?\n\nThe two scopes genuinely use different words: a cloud member "+
					"is a %q, while a project collaborator is a %q. They are near-homographs for unrelated grants, "+
					"not two spellings of one role.",
					email, cloudAccessCloudWriteRole, projectID, cloudAccessCloudWriteRole, cloudAccessProjectWriteRole,
					cloudAccessCloudWriteRole, cloudAccessProjectWriteRole),
			)
		}

		// A member carrying cloud_read_only can only hold readonly on this cloud's
		// projects - the backend 422s anything else. This is genuinely a cross-field
		// constraint spanning a member's cloud deny roles and their project roles,
		// which is exactly why the two live in one resource: split apart, this could
		// only ever surface as a confusing failure partway through an apply.
		//
		// Gated on cloud_read_only being definitely PRESENT, which is why an
		// unresolved deny_roles element does not need to suppress this check: an
		// unknown element cannot hide a value that is already visible, and if
		// cloud_read_only turns out to be the unknown one, the check simply does not
		// fire this pass and Terraform validates again once it resolves.
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

// cloudAccessUnmanagedGrantType is the element type of unmanaged_grants. It
// must stay in step with the schema's NestedObject; the framework hard-errors
// on a mismatch in either direction.
func cloudAccessUnmanagedGrantType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"email":      types.StringType,
		"project_id": types.StringType,
		"reason":     types.StringType,
	}}
}

// cloudAccessMemberType is the element type of the member map.
func cloudAccessMemberType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"base_role":  types.StringType,
		"deny_roles": types.ListType{ElemType: types.StringType},
		"projects":   types.MapType{ElemType: types.StringType},
	}}
}

// cloudAccessMembersToState converts the API's view of a cloud's members into
// the member map, using priorMember (the map already in state, which is null on
// import) to preserve shapes the wire cannot express.
//
// THE ROLE COUNT IS THE SUBTLE PART. The roles endpoint reports base_roles as a
// list, and this provider only ever writes exactly one, but two other counts are
// reachable and they are NOT the same kind of event:
//
//   - ZERO is ordinary, not an anomaly. A member added through the legacy
//     collaborator path - the Anyscale console, or the API's permission_level
//     endpoint - has a membership row and no role row. Erroring on that would
//     make this resource unusable against most real clouds, and warning on it
//     would emit noise on every refresh for a completely normal cloud.
//   - MORE THAN ONE is a genuine anomaly, only reachable through a directory-sync
//     interaction outside this resource's writes. It gets a warning naming what
//     was found.
//
// Both land base_role as NULL rather than guessing an element or dropping the
// member, and that choice does real work in both directions. A member the
// configuration declares shows a diff and the next apply writes the declared
// role - which, for the multi-role case, is also what repairs it, since the roles
// write is a SET. A member the configuration does not declare stays visible as
// an undeclared member, so authoritative mode can act on it. Dropping the member
// instead - the intuitive reading of "we cannot represent this" - would hide an
// existing member from the one resource whose job is to know the whole set.
func cloudAccessMembersToState(ctx context.Context, remote []cloudAccessRemoteMember, priorMember types.Map) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics

	// Prior entries are looked up case-INSENSITIVELY. Email identity is not
	// case-sensitive to Anyscale, so a config key of "Alice@x.com" and an API
	// response of "alice@x.com" are one person; matching them exactly would treat
	// the API's spelling as a new member and the config's as a departed one.
	prior := make(map[string]CloudAccessMemberModel)
	if !priorMember.IsNull() && !priorMember.IsUnknown() {
		raw := make(map[string]CloudAccessMemberModel, len(priorMember.Elements()))
		diags.Append(priorMember.ElementsAs(ctx, &raw, false)...)
		if diags.HasError() {
			return types.MapNull(cloudAccessMemberType()), diags
		}
		for email, m := range raw {
			prior[strings.ToLower(strings.TrimSpace(email))] = m
		}
	}

	elements := make(map[string]attr.Value, len(remote))
	for _, m := range remote {
		priorEntry, hadPrior := prior[strings.ToLower(strings.TrimSpace(m.Email))]

		baseRole := types.StringNull()
		switch {
		case len(m.BaseRoles) == 1:
			baseRole = types.StringValue(m.BaseRoles[0])
		case len(m.BaseRoles) > 1:
			diags.AddWarning(
				"Cloud Member Has More Than One Base Role",
				fmt.Sprintf("Member %s holds %d base roles on this cloud (%v), but a member is expected to hold exactly "+
					"one. This is only reachable through a change made outside this resource, which always writes a "+
					"single role.\n\nbase_role is reported as null for this member rather than guessing which of the %d "+
					"applies. If your configuration declares a role for them, the next apply writes it and replaces the "+
					"whole set, which resolves this.", m.Email, len(m.BaseRoles), m.BaseRoles, len(m.BaseRoles)),
			)
		}

		// The wire cannot distinguish "deny_roles was never declared" from
		// "deny_roles was declared as []", because both are sent as an empty list
		// and both come back as one. Reproducing a single shape unconditionally
		// would put a permanent diff against whichever the practitioner wrote, so an
		// empty result keeps prior state's shape. A non-empty result always wins:
		// that is real current server state.
		denyRoles := types.ListNull(types.StringType)
		switch {
		case len(m.DenyRoles) > 0:
			list, listDiags := types.ListValueFrom(ctx, types.StringType, m.DenyRoles)
			diags.Append(listDiags...)
			denyRoles = list
		case hadPrior && !priorEntry.DenyRoles.IsNull() && !priorEntry.DenyRoles.IsUnknown():
			list, listDiags := types.ListValueFrom(ctx, types.StringType, []string{})
			diags.Append(listDiags...)
			denyRoles = list
		}

		// PROJECT ROLES ARE NOT REFRESHED. They are carried over from prior state
		// unchanged, and land null on import.
		//
		// This is a deliberate interim contract, not an oversight, and it has a real
		// cost: a project role changed outside Terraform is not detected, so drift
		// at project scope is invisible. Refreshing it needs one call per project
		// under the cloud on every read - there is no list-collaborators-for-a-cloud
		// endpoint - and whether to pay that, gate it, or accept the blind spot is
		// an open decision. Carrying prior state forward is the only interim that
		// does not manufacture a permanent phantom diff: projects is plain Optional,
		// so a read that nulled a declared value would show a change on every plan.
		projects := types.MapNull(types.StringType)
		if hadPrior {
			projects = priorEntry.Projects
		}

		obj, objDiags := types.ObjectValue(
			map[string]attr.Type{
				"base_role":  types.StringType,
				"deny_roles": types.ListType{ElemType: types.StringType},
				"projects":   types.MapType{ElemType: types.StringType},
			},
			map[string]attr.Value{
				"base_role":  baseRole,
				"deny_roles": denyRoles,
				"projects":   projects,
			},
		)
		diags.Append(objDiags...)
		if diags.HasError() {
			return types.MapNull(cloudAccessMemberType()), diags
		}

		// Keyed by the API's own spelling of the email, not prior state's. The two
		// can differ in case, and the API's is what a fresh import would produce -
		// so using it keeps an imported state and a refreshed state identical rather
		// than making the key depend on which path got there first.
		elements[m.Email] = obj
	}

	result, resultDiags := types.MapValue(cloudAccessMemberType(), elements)
	diags.Append(resultDiags...)
	return result, diags
}

// resolvedStringMapValues returns the entries of a string map whose values
// Terraform has actually resolved, skipping null and unknown ones. A null or
// unknown map yields an empty result.
//
// This exists instead of ElementsAs because ElementsAs into a
// map[string]string errors on an unknown element, and at validate time an
// unknown element is the ordinary shape of a value interpolated from another
// resource - not a config mistake. Surfacing it as an error would fail a
// legitimate config; skipping it lets every value the practitioner did type get
// checked, and Terraform re-validates once the rest resolve.
func resolvedStringMapValues(m types.Map) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := make(map[string]string, len(m.Elements()))
	for k, v := range m.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		out[k] = s.ValueString()
	}
	return out
}

// resolvedStringListValues is resolvedStringMapValues for a list. Callers must
// treat the result as possibly incomplete: it can prove a value is PRESENT but
// never that one is absent, since a skipped unknown element could be it.
func resolvedStringListValues(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	for _, v := range l.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		out = append(out, s.ValueString())
	}
	return out
}

// ModifyPlan implements the empty-member-set guard.
//
// This CANNOT live in ValidateConfig. The question is "would this apply empty a
// cloud that currently has members?", which is inherently a comparison against
// prior state, and ValidateConfigRequest carries only Config. ModifyPlan
// receives Config, State and Plan, so it is the only hook that can ask it.
//
// The guard exists because an empty member map is usually an accident: a
// for_each over a mistyped or missing upstream group renders byte-identically
// to a deliberate purge. It is deliberately NOT satisfied by distinguishing an
// omitted map from an empty one, since the accident produces exactly the empty
// one - the opt-in has to live outside the data path it protects.
func (r *CloudAccessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// No prior state means a create: there are no members to revoke, so an empty
	// map is harmless. No plan means a destroy, which is governed by the
	// prevent_destroy guidance on the resource page rather than by this attribute.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan CloudAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown member map is computed from something not yet resolved; there is
	// nothing to judge and Terraform will re-plan once it is known.
	if plan.Member.IsUnknown() {
		return
	}
	if len(plan.Member.Elements()) > 0 {
		return
	}

	var state CloudAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	currentCount := len(state.Member.Elements())
	if currentCount == 0 {
		// Already empty - emptying it again revokes nobody.
		return
	}

	// Unknown means the value is computed and Terraform cannot confirm the opt-in
	// at plan time. Treat that as NOT opted in: the whole point is that this
	// decision is reviewable in the plan, and a guard that passes because its own
	// signal is unresolved is not a guard.
	if plan.AllowEmptyMemberSet.ValueBool() && !plan.AllowEmptyMemberSet.IsUnknown() {
		return
	}

	cloudID := plan.CloudID.ValueString()
	if cloudID == "" {
		cloudID = state.CloudID.ValueString()
	}

	resp.Diagnostics.AddAttributeError(
		path.Root("member"),
		"Refusing To Revoke Every Member Of This Cloud",
		fmt.Sprintf("This apply would remove all %d member(s) from cloud %s, because the member map is empty and "+
			"this resource is authoritative over that cloud's members.\n\n"+
			"This is usually an accident - a for_each over a mistyped or missing group produces an empty map that "+
			"looks identical to a deliberate purge. Check the expression feeding `member` first.\n\n"+
			"If revoking all %d member(s) is genuinely what you want, set `allow_empty_member_set = true` on this "+
			"resource.", currentCount, cloudID, currentCount),
	)
}
