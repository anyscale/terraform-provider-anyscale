package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// defaultCloudAccessTimeout bounds one Create/Update/Delete reconcile,
// including every per-write retry J.13 permits (at most 3 attempts per write,
// exponential backoff capped at cloudAccessRetryMaxBackoff). 5 minutes gives
// real headroom even for a large member map with several retried transient
// failures, while still being a real bound rather than an unbounded reconcile.
const defaultCloudAccessTimeout = 5 * time.Minute

// anyscale_cloud_access - AUTHORITATIVE over one cloud's entire member list.
//
// FULL CRUD, WRITE PATH ENABLED. The read half was built and landed before the
// write half on purpose, and the ordering was a safety property rather than
// convenience: a resource that could only read could not revoke anyone, so it
// was reviewable and exercisable against real infrastructure with no way to
// cause harm - see cloudAccessWriteEnabled's own history for what enabling the
// write path required.
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
// project role compatible with it. See the three-vocabulary note below before
// assuming any spelling carries across scopes.
const (
	cloudAccessReadOnlyDenyRole    = "cloud_read_only"
	cloudAccessReadOnlyProjectRole = "readonly"
)

// cloudRolesFeatureDisabled reports whether an error is the cloud roles
// endpoint's feature gate rather than a real failure.
//
// TWO SEPARATE FEATURE FLAGS gate this API - one for reading roles and one for
// writing them - and both return byte-identical 501 detail. So a 501 says the
// feature is not enabled but does NOT say which half is missing, and a diagnostic
// that names only one of them would send someone looking for the wrong thing.
//
// An earlier version of this comment, and of the diagnostic below, claimed the
// endpoint is unavailable on Azure regardless of the flags. That was unsourced and
// is not what the backend does: all three 501 sites gate on a feature flag and
// none of them reads the cloud provider. It may still be true in practice - the
// flags may simply never be enabled for Azure deployments - but that is a
// different claim with no evidence behind it, and the version that shipped told
// Azure users support could not help them, which would talk a customer out of a
// feature they might be able to have.
//
// DO NOT DELETE THIS PARAGRAPH even though it describes an absence. This claim
// was introduced on anyscale_organization_user_role in #228, shipped, and was
// never retracted - it reached this file by being copied from that released
// code. Sweeps then found it in four code sites, a test that asserted it, an
// example comment, and the generated docs page: four surfaces, three people,
// one afternoon.
//
// Matched on the status text DoRequestRaw produces, since that helper does not
// return the status code itself. Same approach as the organization-scope
// equivalent.
func cloudRolesFeatureDisabled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unexpected status 501")
}

// cloudRolesDisabledSummary and cloudRolesDisabledDetail are the diagnostic
// this resource must return on EVERY path that can reach either flag, not only
// Read. The detail is deliberately operation-agnostic ("cannot proceed" rather
// than "cannot read") because the same 501 is reachable from a role write
// (setCloudAccessRole) as well as the member-list read (listCloudAccessMembers),
// and a read-flavored message on a write failure would send an operator looking
// at the wrong half of the two-flag gate.
const cloudRolesDisabledSummary = "Cloud Roles API Not Available"
const cloudRolesDisabledDetail = "The cloud roles API is not enabled for this organization, so this operation cannot " +
	"proceed.\n\nReading roles and writing them are gated by two separate feature flags that " +
	"return the same response, so this error does not say which of the two is missing - mention both when you ask. " +
	"Contact Anyscale support to have the cloud roles API enabled for your organization. Until then, manage cloud and " +
	"project access through the Anyscale console or API."

// cloudAccessProjectWriteRole is the project-scope spelling of the write tier;
// cloudAccessCloudWriteRole is the cloud ROLES spelling of an unrelated role
// that happens to be a near-homograph of it.
//
// THERE ARE THREE VOCABULARIES HERE, NOT TWO, and two of them share a spelling.
// Conflating any pair of them is the recurring mistake on this surface:
//
//  1. Cloud ROLES - base_role on PUT .../collaborators/users/{user_id}/roles.
//     Known values owner, WRITER, collaborator, project_viewer,
//     compute_config_viewer, workload_operator. This is the vocabulary this
//     resource's base_role attribute speaks.
//  2. Cloud LEGACY permission_level - on POST .../collaborators/users, the
//     bootstrap call. Enum owner / WRITE / readonly, live-confirmed. Same scope
//     as (1), different spelling, different concept; this resource only ever
//     sends "readonly" to it, as the least-privilege bridge value.
//  3. PROJECT permission_level - owner / WRITE / readonly. Same spelling as (2)
//     and a different scope again.
//
// So "write" is correct at cloud scope on the legacy endpoint and wrong as a
// project role, which is why the check below is about (1) versus (3)
// specifically rather than about "cloud versus project" in general.
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
	ID                  types.String   `tfsdk:"id"`
	CloudID             types.String   `tfsdk:"cloud_id"`
	AllowEmptyMemberSet types.Bool     `tfsdk:"allow_empty_member_set"`
	Member              types.Map      `tfsdk:"member"`
	UnmanagedGrants     types.List     `tfsdk:"unmanaged_grants"`
	UngrantedMembers    types.List     `tfsdk:"ungranted_members"`
	Timeouts            timeouts.Value `tfsdk:"timeouts"`
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
		MarkdownDescription: "Manages the **complete** member list of one Anyscale cloud: cloud roles, deny " +
			"roles, and each member's roles on the projects your configuration names.\n\n" +
			"~> **This resource is authoritative.** Terraform owns who has access to this cloud. Any member not " +
			"declared in `member` is **revoked** - including people granted access through the Anyscale console, " +
			"and including on the **first** apply. That is the point of this resource - it is how you make " +
			"Terraform the source of truth for a cloud's membership - but it means a mistake here removes real " +
			"people's access. Read this whole page before your first apply against a production cloud.\n\n" +
			"**Declare the member list you expect, and `terraform plan`/`apply` makes it so** - adding, changing, " +
			"and revoking roles to match. Read the scope limits below before relying on a clean plan as complete " +
			"assurance, though.\n\n" +
			"A clean plan is not the same as \"nobody has access you did not intend\", and it is not proof " +
			"nothing has changed out of band. Organization admins are invisible to the endpoint this reads, your " +
			"own identity is excluded, project roles are compared only for the projects your configuration names, " +
			"and - the sharpest limit - an administrator can change a member's role through the Anyscale console " +
			"or CLI without this resource noticing at all. That legacy path writes through a different store than " +
			"the one this resource reads, so a member promoted from `writer` to `owner` through the console reads " +
			"back exactly as declared and plans clean, while actually holding a different role. The one direction " +
			"this resource CAN see is a change crossing the `cloud_read_only` restriction specifically - see " +
			"`deny_roles` below. Read a clean plan as confirmation of what this resource was able to check, not " +
			"as confirmation of the member's actual role.\n\n" +
			"That is also why `member` is Required: it is this resource's whole authority, not an optional " +
			"filter - omitting it is not meaningful on an authoritative resource.\n\n" +
			"~> **Two Computed attributes track write-path shortfalls, and they mean OPPOSITE things.** " +
			"`unmanaged_grants` is someone who still has access this configuration says they should not " +
			"have (a revoke that could not complete). `ungranted_members` is someone who lacks access this " +
			"configuration says they should have (a grant that could not complete). Neither ever influences " +
			"planning - both are diagnostics only, alertable via `length(...) > 0`, and a shortfall in either one " +
			"still lets the apply converge rather than fail, since erroring after another member's change already " +
			"succeeded would taint this resource's state and schedule a destructive retry.\n\n" +
			"### `auto_add_user` and this resource are mutually exclusive\n\n" +
			"~> A cloud with `auto_add_user` enabled cannot have its member list owned by Terraform at all. While " +
			"that setting is on, Anyscale automatically grants every organization member access to the cloud and " +
			"refuses to remove a collaborator, so authoritative management is impossible rather than merely " +
			"degraded.\n\n" +
			"~> Worth knowing before you enable that flag: turning `auto_add_user` on for a cloud that **already** " +
			"has organization members retroactively adds all of them as collaborators immediately, not only members " +
			"added afterwards. Enabling it is itself a grant to everyone in the organization.\n\n" +
			"Turning it back off does **not** remove the collaborators it already added - it only stops further " +
			"additions and unblocks removal. So a cloud that has ever had `auto_add_user` enabled starts out with your " +
			"whole organization on its member list, and the first apply that takes authority over it revokes everyone " +
			"the configuration does not declare. Review that plan carefully.\n\n" +
			"### A cloud carrying a group policy binding is a third structural refusal\n\n" +
			"~> Alongside `auto_add_user` and an empty `member` map, a cloud with a group policy binding (set via " +
			"the Anyscale Policy API, for example `anyscale policy set --resource-type cloud`) refuses `Create` and " +
			"`Update`. A separate, asynchronous system flattens group membership into this cloud's ordinary member " +
			"list - the same rows this resource reads, with nothing marking their origin - so an authoritative " +
			"apply cannot tell a group-derived member from a manually granted one and would revoke it. That system " +
			"may then silently re-grant the same member later, on its own schedule, entirely outside this " +
			"configuration: permanent non-convergence against a controller this resource cannot see or express, " +
			"not a one-time undisclosed revoke. `Delete` is the deliberate exception - it still attempts and " +
			"records, the same asymmetry `auto_add_user` has, because refusing a destroy would strand the resource " +
			"in state with no exit but `terraform state rm`.\n\n" +
			"~> Detecting a binding is best-effort, not a guarantee - the check itself may be inconclusive (this " +
			"endpoint is BETA and commonly returns 403 for a token that is not an organization admin), in which " +
			"case this resource warns and proceeds rather than refusing on suspicion alone.\n\n" +
			"### One Terraform state per cloud, and this resource cannot check\n\n" +
			"~> Two `anyscale_cloud_access` instances - in the same configuration, or in two entirely separate " +
			"ones - managing the same `cloud_id` revoke each other's members indefinitely, and BOTH report success " +
			"on every apply: one grants Alice and revokes Bob, the other grants Bob and revokes Alice, and neither " +
			"ever converges. This resource cannot detect it, because it cannot see another Terraform state. Manage " +
			"each cloud's member list from exactly one `anyscale_cloud_access` instance - this is the price of " +
			"authority, stated here because the failure otherwise gives an operator no signal at all.\n\n" +
			"### Not available in every organization\n\n" +
			"~> This resource reads the cloud roles API, which returns `501` in organizations that do not have " +
			"that feature enabled. Reading roles and writing them are gated by **two separate feature flags** which " +
			"return the same response, so a `501` does not tell you which of the two you are missing - mention both when " +
			"you ask Anyscale support to enable it. Until it is enabled, manage cloud access through the Anyscale " +
			"console or API.\n\n" +
			"### Reviewing the plan will not protect you from the first apply\n\n" +
			"~> The members about to lose access on a first apply are ones Terraform has never read, so they " +
			"appear **nowhere** in plan output. This is the one sharp edge on this resource that plan review " +
			"genuinely cannot catch. The `for_each` warning below *is* plan-reviewable; this is not.\n\n" +
			"The worst realistic case: a cloud owner who is **not** an organization admin can be revoked on the " +
			"first apply, with no protection but reviewing the plan before it runs. Organization admins are safe in " +
			"BOTH directions, and deliberately rather than by accident of plumbing: they are invisible to the " +
			"endpoint this resource reads, so it cannot revoke what it cannot see, and declaring one in `member` " +
			"is a plan-time error (see the organization-admin note on `member` below) - backed by the API itself " +
			"independently refusing to change an organization admin's role at all. Two different mechanisms " +
			"landing on the same guarantee, not a coincidence to rely on.\n\n" +
			"Your own identity is excluded entirely: the identity Terraform authenticates as is never reported as a " +
			"member and cannot be declared as one. That is scoped to whoever runs the apply, not to a list of " +
			"protected people - another operator who is not declared here is still revoked.\n\n" +
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
					"known source of split, half-granted access.\n\n" +
					"~> **Declaring an organization admin here is a plan-time error.** " +
					"The Anyscale API refuses to change an organization admin's role at all, and discovering that " +
					"mid-apply - after another member's grant already succeeded - is not recoverable the way a " +
					"revoke failure is. This check is best-effort: no field this resource can read predicts the " +
					"backend's own admin signal with certainty, so a plan-time pass is not a guarantee nothing will " +
					"fail, and a plan-time miss is not silent - it still fails safely at apply time, just without " +
					"the earlier warning.",
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
								"~> **Authority here is scoped to the projects you name.** This resource is authoritative " +
								"over these project roles and no others: a role dropped from this map is revoked, but a role " +
								"granted out of band on a project no configuration mentions is invisible to Terraform and is " +
								"left alone. That is deliberate - reading every project under the cloud would mean adopting " +
								"this resource silently took ownership of projects you never mentioned.\n\n" +
								"A consequence worth knowing after `terraform import`: an imported resource names no " +
								"projects, so none are in scope and this attribute comes back `null` even for a member who " +
								"holds project roles. Declaring them brings them into scope on the first apply.\n\n" +
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
				MarkdownDescription: "Grants this resource could not revoke, or deliberately skipped revoking " +
					"because a grant failed elsewhere in the same apply, and why. Populated in two distinct " +
					"cases, both worth alerting on: an undeclared member could not be removed - the Anyscale API " +
					"can reach a state where a role was granted but cannot be revoked, and it is not detectable " +
					"before attempting the revoke - or a grant failed elsewhere in the same apply, which suppresses " +
					"every revoke this apply would otherwise have attempted (see `ungranted_members`) and records " +
					"each one here with a reason naming the skip.\n\n" +
					"The apply still converges rather than failing, because one unrevokable member - or one unfinished " +
					"grant elsewhere - would otherwise block the whole set forever. Alert on this: " +
					"`length(anyscale_cloud_access.x.unmanaged_grants) > 0` means someone has access Terraform intends " +
					"them not to have.\n\n" +
					"~> **This lists only revokes that were attempted OR deliberately skipped because a grant failed.** " +
					"It covers every member of the cloud, but at project scope only the projects your configuration " +
					"names - a grant on a project this resource never manages is neither attempted nor skipped, and so " +
					"never appears here. An empty list means nothing in scope failed or was skipped, not that nobody " +
					"holds access outside that scope.",
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
			"ungranted_members": schema.ListNestedAttribute{
				Computed: true,
				// The grant-side twin of unmanaged_grants, and deliberately a SEPARATE
				// attribute rather than folded into it: unmanaged_grants means "someone
				// still has access this configuration says they should not have" (an
				// unfinished revoke), while this means "someone lacks access this
				// configuration says they should have" (an unfinished grant) - opposite
				// problems needing opposite responses, which `length(unmanaged_grants) >
				// 0` alone could not distinguish if they shared one attribute.
				//
				// Populated when a grant fails partway through an apply. When that
				// happens, this resource also skips every revoke it would otherwise have
				// attempted in the same apply - see unmanaged_grants for who that affects,
				// recorded there with a reason naming the skip. The apply still converges
				// rather than failing: erroring after another member's grant already
				// succeeded would leave state tainted, scheduling a destroy-then-recreate
				// on the next apply that would revoke everyone this one DID manage to
				// grant. Same discipline as unmanaged_grants, same reason it exists.
				//
				// Every branch that populates this must set it explicitly before the
				// first State.Set - a Computed list-of-objects left unknown has crashed
				// this provider before. No UseStateForUnknown: this tracks whether a
				// grant failed, which genuinely changes between applies.
				MarkdownDescription: "Members this apply could not grant the declared role, and why. Populated when a " +
					"grant fails partway through an apply - the configuration asked for this access and this resource " +
					"was not able to establish it.\n\n" +
					"The apply still converges rather than failing, because erroring after another member's grant " +
					"already succeeded would leave this resource's state tainted - the next apply would then destroy " +
					"and recreate it, revoking every member this one DID manage to grant. Alert on this: " +
					"`length(anyscale_cloud_access.x.ungranted_members) > 0` means someone is missing access this " +
					"configuration says they should have.\n\n" +
					"~> **When this is non-empty, this apply also skipped every revoke it would otherwise have " +
					"attempted** - see `unmanaged_grants` for who that affects, recorded there with a reason naming " +
					"the skip. A grant shortfall is treated as too dangerous a moment to also remove access.\n\n" +
					"~> **State for the affected member(s) still shows the values this apply planned, not what was " +
					"actually granted**, since writing anything else would itself produce an inconsistent-result error. " +
					"The next `terraform plan` after a refresh shows the true gap and retries it.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"email": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Email of the member whose grant could not be established.",
						},
						"project_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Project the grant applies to, or null when it is the cloud-level grant.",
						},
						"reason": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "What the API reported when the grant was attempted.",
						},
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
				CreateDescription: "Maximum time to wait for a create's reconcile to finish, including every per-write " +
					"retry (e.g. `2m`, `10m`). Defaults to `5m` - real headroom even for a large member map with " +
					"several retried transient failures. Purely local to this provider - never sent to or read from the " +
					"Anyscale API.",
				UpdateDescription: "Maximum time to wait for an update's reconcile to finish. Same default and rationale as `create`.",
				DeleteDescription: "Maximum time to wait for destroy's revoke pass to finish. Same default and rationale as `create`.",
			}),
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

// Create reconciles the cloud to the configuration, revoking undeclared
// members exactly as Update does - Terraform is authoritative from the first
// apply, not the second. The revoke never appears in a plan: a member in
// neither config nor prior state is one Terraform has never read, so there
// is nothing to diff against. The resource page is the only mitigation.
//
// cloudAccessWriteEnabled gates the write path. See
// docs/decisions/rbac-surface-consolidation/README.md (J.18) for why it
// existed and what enabling it required. A var rather than a const so the
// branches below don't fold away as dead code under the linter.
var cloudAccessWriteEnabled = true

// cloudAccessWriteDisabledSummary and Detail are the refusal the three write
// methods return while the gate above is closed.
const (
	cloudAccessWriteDisabledSummary = "Cloud Access Is Read-Only In This Provider Version"
	cloudAccessWriteDisabledDetail  = "anyscale_cloud_access can refresh and import a cloud's member list, but cannot " +
		"yet change it. Remove this resource from your plan, or use it only to read - importing is supported.\n\n" +
		"Manage cloud and project access through the Anyscale console or API until the write path ships. It is withheld " +
		"deliberately rather than incomplete: this resource is authoritative over a cloud's whole member list, so its " +
		"write path revokes real people's access, and it is being validated against real clouds before it is allowed to."
)

// refuseWriteWhileReadOnly reports true when it has refused.
func (r *CloudAccessResource) refuseWriteWhileReadOnly(diags *diag.Diagnostics) bool {
	if cloudAccessWriteEnabled {
		return false
	}
	diags.AddError(cloudAccessWriteDisabledSummary, cloudAccessWriteDisabledDetail)
	return true
}

func (r *CloudAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.refuseWriteWhileReadOnly(&resp.Diagnostics) {
		return
	}

	var plan CloudAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultCloudAccessTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// State is written before any API call, with every Computed attribute
	// populated. The framework returns whatever Create put in resp.State
	// alongside an error, so a resource that errors partway through a reconcile
	// still lands in state with its id - which is what lets the next plan see it
	// rather than trying to create it a second time.
	plan.ID = types.StringValue(plan.CloudID.ValueString())
	plan.UnmanagedGrants = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	plan.UngrantedMembers = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// No prior state on a create, so no prior project declarations and therefore no
	// project revokes. Undeclared roles on an in-scope project are not this
	// resource's to remove - that is the authority boundary, not a create-only
	// exception.
	r.applyCloudAccess(ctx, &plan, types.MapNull(cloudAccessMemberType()), &resp.State, &resp.Diagnostics)
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

	// The calling identity is INVISIBLE to this resource, and Read is one of the
	// three places that has to agree on it. Reporting the caller while the
	// reconcile refuses to revoke them has no working outcome: the post-apply
	// state either contradicts the plan, which Core rejects as "provider produced
	// inconsistent result after apply", or omits them and the next Read puts them
	// back and proposes the same removal forever. Read cannot arbitrate by looking
	// at the configuration either - ReadRequest carries State, Private,
	// ProviderMeta and ClientCapabilities, and no Config. So the caller is absent
	// from read, plan and write alike, and declaring them is a plan-time error.
	caller, err := fetchCloudAccessCallerIdentity(ctx, r.client)
	if err != nil {
		// Deliberately an error rather than "report everyone and hope". An
		// unidentified caller means this refresh cannot tell whether it is about to
		// write the one member that must not appear in state, and a refresh that
		// fails is recoverable while a state that disagrees with the plan is not.
		resp.Diagnostics.AddError(
			"Could Not Identify The Calling Identity",
			fmt.Sprintf("This resource excludes the identity Terraform is authenticated as from the member list it "+
				"manages, so it must resolve that identity before it can refresh. Reading /api/v2/userinfo failed: %s\n\n"+
				"State is left unchanged. Retry the refresh.", extractAPIErrorDetail(err)),
		)
		return
	}

	remote, err := listCloudAccessMembers(ctx, r.client, cloudID)
	if err != nil {
		if cloudRolesFeatureDisabled(err) {
			resp.Diagnostics.AddError(cloudRolesDisabledSummary, cloudRolesDisabledDetail)
			return
		}
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

	visible := make([]cloudAccessRemoteMember, 0, len(remote))
	for _, m := range remote {
		if caller.matches(m) {
			continue
		}
		visible = append(visible, m)
	}

	projectRoles, projectDiags := r.readScopedProjectRoles(ctx, state.Member)
	resp.Diagnostics.Append(projectDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	memberMap, diags := cloudAccessMembersToState(ctx, visible, state.Member, projectRoles)
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

	// unmanaged_grants records what the LAST APPLY could not revoke, and
	// ungranted_members what it could not grant - neither is something any read
	// can recompute, so a refresh preserves both rather than clearing them.
	// Clearing either on refresh would silence an alert about a shortfall
	// Terraform already knows about, which is precisely what these attributes
	// exist to keep visible. Null (again, the fresh-import shape) becomes an
	// empty list, never left unknown: a Computed list-of-objects left unknown
	// has crashed this provider before.
	if state.UnmanagedGrants.IsNull() || state.UnmanagedGrants.IsUnknown() {
		state.UnmanagedGrants = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	}
	if state.UngrantedMembers.IsNull() || state.UngrantedMembers.IsUnknown() {
		state.UngrantedMembers = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ImportState takes the cloud ID and lets Read populate the member list.
//
// The provider-side attributes are set here rather than left null, because
// Read deliberately preserves whatever it finds in them and a null would
// survive the refresh. All land on the value the schema documents for an
// import: allow_empty_member_set false (so an imported resource cannot empty a
// cloud until someone opts in, which is why a config that sets true shows a
// false -> true diff on its first plan), and unmanaged_grants / ungranted_members
// both empty (nothing has been attempted yet, so nothing has failed).
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
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ungranted_members"),
		types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{}))...)
}

// Update is the same reconcile as Create. There is no difference in behavior
// between the two, only in when Terraform calls them.
func (r *CloudAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.refuseWriteWhileReadOnly(&resp.Diagnostics) {
		return
	}

	var plan CloudAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultCloudAccessTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	plan.ID = types.StringValue(plan.CloudID.ValueString())
	plan.UnmanagedGrants = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	plan.UngrantedMembers = types.ListValueMust(cloudAccessUnmanagedGrantType(), []attr.Value{})
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var priorState CloudAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.applyCloudAccess(ctx, &plan, priorState.Member, &resp.State, &resp.Diagnostics)
}

// applyCloudAccess is the shared body of Create and Update.
//
// Never re-reads or writes back `member` or `projects` after applying, on any
// path. Both are non-Computed, so Core requires the returned state to equal
// the plan exactly - a post-apply write can only match the plan (a no-op) or
// diverge from it ("provider produced inconsistent result after apply"). A
// post-apply write may only touch Computed attributes (unmanaged_grants,
// ungranted_members here), since only their planned value is Unknown. Drift
// in `member`/`projects` surfaces on the next Read/refresh instead.
func (r *CloudAccessResource) applyCloudAccess(
	ctx context.Context,
	plan *CloudAccessResourceModel,
	priorMember types.Map,
	state *tfsdk.State,
	diags *diag.Diagnostics,
) {
	cloudID := plan.CloudID.ValueString()

	desired, desiredDiags := cloudAccessDesiredMembers(ctx, plan.Member)
	diags.Append(desiredDiags...)
	if diags.HasError() {
		return
	}

	priorProjects, priorDiags := cloudAccessPriorProjectDeclarations(ctx, priorMember)
	diags.Append(priorDiags...)
	if diags.HasError() {
		return
	}

	result, reconcileDiags := reconcileCloudAccess(ctx, r.client, cloudID, desired, cloudAccessReconcileOptions{
		RevokeUndeclared:             true,
		RefuseWhenAutoAddUserEnabled: true,
		RefuseWhenGroupPolicyBound:   true,
		PriorProjects:                priorProjects,
	})
	diags.Append(reconcileDiags...)

	// unmanaged_grants and ungranted_members are written even when the
	// reconcile errored: a revoke or grant that failed before the error is
	// still worth recording, and dropping it because a later step failed would
	// hide it.
	unmanaged, listDiags := result.unmanagedGrantsList()
	diags.Append(listDiags...)
	if !listDiags.HasError() {
		plan.UnmanagedGrants = unmanaged
	}
	ungranted, ungrantedListDiags := result.ungrantedMembersList()
	diags.Append(ungrantedListDiags...)
	if !ungrantedListDiags.HasError() {
		plan.UngrantedMembers = ungranted
	}
	diags.Append(state.Set(ctx, plan)...)
}

// Delete revokes the members this resource manages.
//
// This is the destroy in this provider with the largest blast radius, and it is
// correct: the resource's authority IS the cloud's member list, so an absent
// state for it means nobody has access through it. The sharp edge is that
// Terraform cannot distinguish a deliberate removal from a for_each key typo -
// mitigation for that lives outside the provider, in prevent_destroy on
// production clouds, and is stated on the resource page.
//
// It revokes what STATE holds, not what the API currently reports. Someone
// granted access out of band after the last apply is not this resource's to
// remove on the way out: destroy removes what the resource had authority over,
// and it never had authority over a member it never saw.
func (r *CloudAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.refuseWriteWhileReadOnly(&resp.Diagnostics) {
		return
	}

	var state CloudAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, timeoutDiags := state.Timeouts.Delete(ctx, defaultCloudAccessTimeout)
	resp.Diagnostics.Append(timeoutDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cloudID := state.CloudID.ValueString()
	if cloudID == "" {
		cloudID = state.ID.ValueString()
	}

	managed, diags := cloudAccessDesiredMembers(ctx, state.Member)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(managed) == 0 {
		return
	}

	// An empty desired set with revokes enabled means "revoke everyone this
	// resource knows about", which is exactly destroy's meaning here.
	result, reconcileDiags := reconcileCloudAccess(ctx, r.client, cloudID, map[string]cloudAccessDesiredMember{},
		cloudAccessReconcileOptions{
			RevokeUndeclared: true,
			// Destroy does NOT refuse on an auto_add_user cloud, or on a group policy
			// binding. Both attempt every revoke, record each failure and name the
			// remedy, because refusing would strand the resource in state with no exit
			// but 'terraform state rm'.
			RefuseWhenAutoAddUserEnabled: false,
			RefuseWhenGroupPolicyBound:   false,
			// No PriorProjects on a destroy: removing each cloud collaborator cascades
			// to their permissions on that cloud's projects, so per-project revokes
			// would only produce 404s to misreport as failures.
		})
	resp.Diagnostics.Append(reconcileDiags...)
	if reconcileDiags.HasError() {
		return
	}

	if len(result.Unmanaged) > 0 {
		// Destroy cannot record this in unmanaged_grants - the resource is leaving
		// state, so there is nowhere for the attribute to survive. A warning is the
		// only channel left, and it must name the people involved: after this
		// destroy completes there is no Terraform resource left to alert on.
		var names []string
		for _, g := range result.Unmanaged {
			names = append(names, fmt.Sprintf("%s (%s)", g.Email.ValueString(), g.Reason.ValueString()))
		}
		resp.Diagnostics.AddWarning(
			"Some Members Still Have Access After Destroy",
			fmt.Sprintf("Destroying this resource could not revoke %d member(s) of cloud %s, and they still have "+
				"access:\n\n%s\n\nUnlike an apply, a destroy cannot record this in unmanaged_grants - the resource is "+
				"leaving state, so there will be no Terraform resource left to alert on. Remove them manually.",
				len(result.Unmanaged), cloudID, strings.Join(names, "\n")),
		)
	}
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
			// Folded and trimmed, matching how base_role is checked two blocks above
			// and how email identity is compared throughout this resource. Without it
			// " writer" and "Writer" slip past a validator whose whole purpose is
			// catching a typo, and 422 at apply time instead.
			if !strings.EqualFold(strings.TrimSpace(level), cloudAccessCloudWriteRole) {
				continue
			}
			resp.Diagnostics.AddAttributeError(
				memberPath.AtName("projects").AtMapKey(projectID),
				"Cloud Role Spelling Used For A Project Role",
				fmt.Sprintf("Member %q is granted %q on project %s, but a project only accepts `owner`, `write` or `readonly`, "+
					"and Anyscale rejects %q on one. Did you mean %q?\n\nThe cloud role vocabulary this resource's "+
					"base_role speaks uses %q; a project collaborator is a %q. They are near-homographs for unrelated "+
					"grants, not two spellings of one role.",
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
func cloudAccessMembersToState(
	ctx context.Context,
	remote []cloudAccessRemoteMember,
	priorMember types.Map,
	projectRoles map[string]map[string]string,
) (types.Map, diag.Diagnostics) {
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

		// PROJECT ROLES ARE REFRESHED, BUT ONLY WITHIN SCOPE. This resource is
		// authoritative over the projects the configuration NAMES, not over every
		// project under the cloud - so the read covers exactly the projects already
		// present in state for this member, and nothing else.
		//
		// The alternative, enumerating every project under the cloud, was rejected on
		// authority rather than cost: adopting this resource would then silently take
		// ownership of projects the practitioner never mentioned, and strip roles on
		// them. A disclosed blind spot beats an ownership escalation discovered
		// during an apply.
		//
		// What the scope costs, stated so it is not mistaken for a bug: a role granted
		// out of band on a project no configuration names is invisible here. What it
		// does NOT cost is drift detection on declared projects - a role dropped from
		// one of those is read as absent and revoked, which is the whole point.
		//
		// A cold import has no prior state, so nothing is in scope and projects lands
		// null. The first apply against a configuration that declares project roles
		// then writes them and brings them into scope.
		projects := types.MapNull(types.StringType)
		if hadPrior && !priorEntry.Projects.IsNull() && !priorEntry.Projects.IsUnknown() {
			inScope := priorEntry.Projects.Elements()
			current := make(map[string]attr.Value, len(inScope))
			for projectID := range inScope {
				roles, read := projectRoles[projectID]
				if !read {
					// Not read this pass. Carrying the prior value is the only safe
					// choice: reporting the role as absent would have an authoritative
					// apply revoke access on the strength of a project it never looked at.
					current[projectID] = inScope[projectID]
					continue
				}
				level, held := roles[m.UserID]
				if !held {
					// Genuinely gone. Omitted rather than carried, so the drift shows.
					continue
				}
				current[projectID] = types.StringValue(level)
			}
			projectMap, projectDiags := types.MapValue(types.StringType, current)
			diags.Append(projectDiags...)
			if diags.HasError() {
				return types.MapNull(cloudAccessMemberType()), diags
			}
			projects = projectMap
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
	// The caller check runs before the create/destroy short-circuit below, because
	// declaring the caller is wrong on a create too - the empty-member-set guard is
	// the thing that only makes sense with prior state, not this.
	if !req.Plan.Raw.IsNull() {
		r.refuseDeclaringTheCaller(ctx, req.Plan, &resp.Diagnostics)
		r.refuseDeclaringOrgAdmins(ctx, req.Plan, &resp.Diagnostics)
	}

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

// refuseDeclaringTheCaller reports an error if the plan declares the identity
// Terraform is authenticated as.
//
// This resource manages OTHER people's access to a cloud; the operator's own
// access is out of scope by construction, which is what makes the caller
// consistently absent from read, plan and write. Declaring them would otherwise
// propose an addition on every plan forever, since Read never reports them.
//
// Two deliberate weaknesses, both preferable to their alternatives:
//
//   - It lives in ModifyPlan rather than ValidateConfig because it needs an API
//     call, and ValidateConfig can run before the provider is configured, so the
//     client may be nil there. Same reason the empty-member-set guard is here.
//   - If the caller cannot be resolved, the check is SKIPPED rather than failed.
//     What actually prevents an operator locking themselves out is the reconcile's
//     own exclusion, which fails closed; all that is lost here is a friendly
//     plan-time message. A hard failure would turn a transient userinfo blip into
//     an unplannable configuration.
//
// The excluded identity is TOKEN-dependent, and that is worth being clear about
// because it is easy to misread as a protected-persons list. Whoever runs the
// apply is safe during that apply. A colleague who ran it last week and is not
// declared in the configuration is still revoked, correctly - that is the
// resource's contract.
func (r *CloudAccessResource) refuseDeclaringTheCaller(ctx context.Context, plan tfsdk.Plan, diags *diag.Diagnostics) {
	if r.client == nil {
		return
	}

	var member types.Map
	if getDiags := plan.GetAttribute(ctx, path.Root("member"), &member); getDiags.HasError() {
		return
	}
	if member.IsNull() || member.IsUnknown() || len(member.Elements()) == 0 {
		return
	}

	caller, err := fetchCloudAccessCallerIdentity(ctx, r.client)
	if err != nil || caller.Email == "" {
		return
	}
	callerEmail := cloudAccessFoldEmail(caller.Email)

	for email := range member.Elements() {
		if cloudAccessFoldEmail(email) != callerEmail {
			continue
		}
		diags.AddAttributeError(
			path.Root("member").AtMapKey(email),
			"Cannot Manage Your Own Cloud Access Here",
			fmt.Sprintf("%q is the identity Terraform is authenticated as, and this resource cannot manage it. Remove "+
				"that entry from member.\n\nThe resource deliberately excludes the calling identity from the member list "+
				"it owns, so that an authoritative apply can never revoke your own access to the cloud you are managing. "+
				"That exclusion applies to reading as well as writing, so a declared entry for yourself would be proposed "+
				"as an addition on every plan and never converge.\n\nNote this is scoped to whoever runs the apply, not "+
				"to a list of protected people: another operator who is not declared here is still revoked.", email),
		)
		return
	}
}

// refuseDeclaringOrgAdmins reports an error if the plan declares an
// organization admin as a member (J.22) - the cloud roles write refuses
// outright to change an org admin's role, and catching that mid-reconcile
// (after some other member's grant already succeeded) leaves no acceptable
// outcome: erroring taints Create, and unmanaged_grants isn't the right place
// to record a grant that was never made.
//
// Best-effort, not a guarantee: the backend's real check is a live SpiceDB
// ORG_OWNER lookup with no provider-facing equivalent, so nothing read here
// can predict it with certainty. The reconcile's own write-side handling is
// what actually protects a practitioner if this warning misses.
//
// Proxy is base_role from the singular organization-collaborator GET, one
// call per declared member - deliberately not permission_level from the list
// endpoint, which is relationally-served and can diverge from ORG_OWNER after
// a legacy-path change; the singular GET reads from the same store ORG_OWNER
// does, so the two go stale together.
//
// Same placement and skip-if-unresolvable discipline as
// refuseDeclaringTheCaller (needs an API call, so can't live in
// ValidateConfig; an unresolvable member's check is skipped, not failed) but
// unlike the caller, every offending member is reported, not just the first.
// No mirror guard needed on Read: org admins are filtered out of
// member-search entirely, so they're already invisible and never revoked.
func (r *CloudAccessResource) refuseDeclaringOrgAdmins(ctx context.Context, plan tfsdk.Plan, diags *diag.Diagnostics) {
	if r.client == nil {
		return
	}

	var member types.Map
	if getDiags := plan.GetAttribute(ctx, path.Root("member"), &member); getDiags.HasError() {
		return
	}
	if member.IsNull() || member.IsUnknown() || len(member.Elements()) == 0 {
		return
	}

	for email := range member.Elements() {
		isAdmin, err := cloudAccessLikelyOrgAdmin(ctx, r.client, email)
		if err != nil {
			// Unresolvable for THIS member only - every other declared member is
			// still checked. Most commonly this is simply "not an org member yet",
			// which is not this check's business to report; the grant call surfaces
			// that error on its own terms.
			continue
		}
		if !isAdmin {
			continue
		}
		diags.AddAttributeError(
			path.Root("member").AtMapKey(email),
			"This Member Appears To Be An Organization Admin",
			fmt.Sprintf("%q appears to be an organization admin, based on the best available signal this resource can "+
				"read - not a guarantee. If they are, the cloud roles API refuses to change an organization admin's role, "+
				"and this resource has no way to recover from that failure mid-apply. Remove that entry from member.\n\n"+
				"Organization admins are outside this resource's scope in both directions: the member-search endpoint "+
				"this resource reads never reports them, so a real admin is already invisible on refresh and never "+
				"revoked. Declaring one here would only ever fail the write - and discovering that after another "+
				"member's grant already succeeded is exactly the situation this early check exists to catch, even "+
				"though it cannot catch every case.", email),
		)
	}
}

// readScopedProjectRoles reads the collaborators of every project already named
// in state, across all members, and returns them keyed by project then user_id.
//
// The union across members rather than per-member, because the same project
// commonly appears under several of them and it is one request either way.
//
// Returns a nil map when nothing is in scope, which is the ordinary case for a
// freshly imported resource and for any configuration that declares no project
// roles. A nil map is not an error and is not the same as "read and found
// nothing": cloudAccessMembersToState distinguishes the two, because a project
// that was never read must keep its prior value rather than be reported absent.
func (r *CloudAccessResource) readScopedProjectRoles(ctx context.Context, member types.Map) (map[string]map[string]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if member.IsNull() || member.IsUnknown() {
		return nil, diags
	}

	entries := make(map[string]CloudAccessMemberModel, len(member.Elements()))
	diags.Append(member.ElementsAs(ctx, &entries, false)...)
	if diags.HasError() {
		return nil, diags
	}

	seen := make(map[string]bool)
	var projectIDs []string
	for _, m := range entries {
		if m.Projects.IsNull() || m.Projects.IsUnknown() {
			continue
		}
		for projectID := range m.Projects.Elements() {
			if seen[projectID] {
				continue
			}
			seen[projectID] = true
			projectIDs = append(projectIDs, projectID)
		}
	}
	if len(projectIDs) == 0 {
		return nil, diags
	}
	sort.Strings(projectIDs)

	roles, err := cloudAccessProjectRoles(ctx, r.client, projectIDs)
	if err != nil {
		// An unreadable project is an ERROR, not a skipped entry. Silently omitting
		// it would report its roles as absent, and for an authoritative resource
		// "absent" is an instruction to revoke - so a transient read failure would
		// become a real revoke on the next apply.
		diags.AddError(
			"Could Not Read Project Roles",
			fmt.Sprintf("This resource refreshes the project roles it manages, and one of those projects could not be "+
				"read: %s\n\nState is left unchanged. Reporting the roles as absent instead would make the next apply "+
				"revoke them.", extractAPIErrorDetail(err)),
		)
		return nil, diags
	}
	return roles, diags
}
