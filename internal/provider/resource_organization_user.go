package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
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

// OrganizationUserResource declares that a person SHOULD HAVE ACCESS to the
// organization, keyed by email. It adopts them if they are already a member and
// invites them if not - so a successful apply means access was REQUESTED, not
// granted.
//
// It is NOT import-only, and destroy does NOT evict: it cancels only a still
// pending invitation this resource itself sent. Removing a human from the
// organization is deliberately not something this resource can do.
//
// The API's own vocabulary (organization_collaborators) is preserved in the
// shared request/response types and helpers below, since it accurately names
// the backend concept those share with the data sources.
//
// Role management deliberately does NOT live here - it belongs to
// anyscale_organization_user_role, which owns the base role and the
// organization's deny roles. Two resources writing the same org role would race
// each other.
type OrganizationUserResource struct {
	client *Client
}

// OrganizationUserResourceModel describes the resource data model. Every field
// is read-only: this resource has no writable attribute at all, since roles
// moved to anyscale_organization_user_role.
type OrganizationUserResourceModel struct {
	// Identity
	// ID holds the EMAIL, not the identity_id. Re-keyed because identity_id and
	// user_id only exist once someone accepts; email identifies the same person
	// before, during and after that.
	ID types.String `tfsdk:"id"`

	// Email is REQUIRED - the declared key. The rest are Computed.
	Email      types.String `tfsdk:"email"`
	UserID     types.String `tfsdk:"user_id"`
	IdentityID types.String `tfsdk:"identity_id"`
	Name       types.String `tfsdk:"name"`
	CreatedAt  types.String `tfsdk:"created_at"`

	// ReinviteIfExpired is an opt-in, never automatic. See its schema
	// comment for why a lapsed invitation must not silently invite anyone again.
	ReinviteIfExpired types.Bool `tfsdk:"reinvite_if_expired"`
}

// Metadata returns the resource type name.
func (r *OrganizationUserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_user"
}

// Schema defines the schema for the resource.
func (r *OrganizationUserResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// v1: re-keyed from identity_id to email. See
		// resource_organization_user_upgrade.go for the migration and why it is
		// required rather than cosmetic.
		Version: 1,

		MarkdownDescription: "Declares that a person **should have access** to the Anyscale organization, keyed by email.\n\n" +
			"~> **A successful apply means access has been REQUESTED, not granted.** If the person is not already a " +
			"member, this sends them an invitation. Unless your organization uses SSO or SCIM, **a human must still " +
			"click the link in that email** before they actually have access. `terraform apply` reporting success " +
			"does not mean the person can log in. Read this resource's `anyscale_organization_user` data source " +
			"counterpart to see where they actually are.\n\n" +
			"If the person is **already** a member, this resource adopts them: no API call, no email, no error. That " +
			"makes it safe to declare people who joined before Terraform, and safe to re-apply.\n\n" +
			"### Destroying this resource never removes a human\n\n" +
			"Destroy cancels **only an invitation this resource sent** and which is still pending. It never evicts " +
			"someone who had already joined, and never evicts someone this resource adopted. Removing a person's " +
			"access is a deliberate act that this resource deliberately cannot do.\n\n" +
			"### Organization-wide limits you will hit before you expect to\n\n" +
			"Inviting is rate-limited by the Anyscale backend, and the limits are **per organization and shared with " +
			"the Anyscale console** - not per configuration:\n\n" +
			"- at most **20 pending invitations** at once (default)\n" +
			"- at most **20 invitations sent per 24 hours** (default)\n\n" +
			"Both are backend defaults that Anyscale can change per organization. A `for_each` onboarding 25 people " +
			"**will fail partway through the apply** once one of them is reached, leaving the earlier invitations " +
			"sent. Onboard in batches under the limit, or ask Anyscale to raise it.\n\n" +
			"### Directory-synced (SCIM) organizations\n\n" +
			"If your organization has directory sync enabled **and** has opted into the Policy API, the Anyscale " +
			"backend refuses to create invitations at all - this resource can still adopt and read existing members, " +
			"but cannot add anyone, because your identity provider owns membership.\n\n" +
			"Both conditions are required. A SCIM-enabled organization that has no Policy API bindings still uses the " +
			"per-user path and can be invited through normally.\n\n" +
			"-> **Roles are managed separately** by `anyscale_organization_user_role`, which owns `base_role` and the " +
			"organization's deny roles. The two resources are deliberately split so only one of them ever writes a " +
			"member's role.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				// Holds the EMAIL, not the identity_id. Changed in the re-key: identity_id
				// and user_id are two different backend identifiers for one human, user_id
				// is optional in the API model and genuinely absent for some identity types,
				// and neither exists before the person accepts. Email is the only value that
				// is stable across the whole lifecycle - before the invitation, while it is
				// pending, and after they join.
				MarkdownDescription: "The member's email address. Same value as `email`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"email": schema.StringAttribute{
				Required: true,
				// Was Computed before the re-key. Now the declared key: this is the one thing
				// the operator states and the only identifier that exists at every point in
				// the lifecycle. Changing it means a different person, hence RequiresReplace.
				MarkdownDescription: "Email address of the person who should have access. Matched case-insensitively " +
					"against Anyscale, which stores addresses lower-cased. Changing this replaces the resource - it " +
					"names a different person.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			"reinvite_if_expired": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				// Opt-in, and deliberately NOT automatic. An unaccepted invitation expires,
				// at which point the person is in neither the collaborator list nor the
				// pending-invitation list. Without this attribute the resource would quietly
				// re-invite whenever that happened - a plan that changes from no-op to
				// create purely because time passed, mailing people again on a cadence set
				// by the backend's expiry window and consuming the 20-per-day quota. Left
				// off, a lapsed invitation is reported as a warning on every refresh and
				// nothing is sent.
				//
				// The mechanism is refresh-side, and has to be: Create cannot see the lapse
				// marker (resource.CreateRequest has no Private field at all) and Update is
				// a no-op, so Read is the only place this attribute can be acted on. Set to
				// true, a lapsed resource is dropped from state during refresh, which makes
				// the next plan a visible create.
				MarkdownDescription: "Whether to send a new invitation when a previous one expired without being " +
					"accepted. Defaults to `false`.\n\n" +
					"When `false`, a lapsed invitation produces a **warning on every refresh** and no email is sent - " +
					"Terraform will not silently invite someone again because an invitation aged out.\n\n" +
					"Set this to `true` to have the resource issue a new invitation. Nothing is \"resent\": an expired " +
					"invitation is dead, and this mints a **new invitation with a new link** - the link in the original " +
					"email will not work, so point the recipient at the new message. It counts against the " +
					"organization's daily invitation limit.\n\n" +
					"Because the expiry is only noticed during a refresh, a lapsed resource with this set to `true` is " +
					"removed from state during `terraform plan`/`refresh` and shown as a **create** in the resulting " +
					"plan. The new invitation is sent when you apply it, not before.",
			},

			"user_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The user ID of the member, once they exist. Null while an invitation is still " +
					"pending, and null for identity types that do not have one.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"identity_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The identity ID of the member, once they exist. Null while an invitation is " +
					"still pending. This was the resource's ID before it was re-keyed to email.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name of the organization member. Null while an invitation is still pending.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"created_at": schema.StringAttribute{
				Computed: true,
				// Write-once, not "set on import": since the re-key this resource can start
				// life as a pending invitation, where no member record - and so no
				// created_at - exists yet. It is therefore populated the first time the
				// person is seen in the organization's member list (which may be a refresh
				// long after create), and never re-read after that, since the API has
				// returned different values for it across reads for the same member.
				MarkdownDescription: "Timestamp when the member was added to the organization. Null while an invitation is still pending. Write-once: populated the first time the member is seen and never re-read afterward, since the API has returned different values for it across reads.",
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

// Create declares that the person named by email should have access.
//
// Two outcomes, neither of which ever removes or downgrades anyone:
//
//   - they are ALREADY a member: adopt them. No API write, no email, no error.
//     This is what makes it safe to declare people who joined before Terraform,
//     and safe to re-apply. The backend would reject a duplicate invite with a
//     400 ("already a member of your organization"), so adopting first is also
//     what keeps that error off the practitioner's screen.
//   - they are not: send an invitation. A successful apply means access was
//     REQUESTED - a human still has to click the link.
//
// The third branch the design called for - refusing to re-invite when a
// previous invitation lapsed unless reinvite_if_expired is set - CANNOT
// live here. resource.CreateRequest has no Private field at all in
// terraform-plugin-framework v1.19.0 (verified against the pinned source:
// CreateResponse.Private exists and is pre-populated, the REQUEST side does
// not exist), so Create structurally cannot see the lapse marker Read records.
// Nor can it re-derive one: the only list endpoint the API exposes for
// invitations returns pending-unexpired-unaccepted rows exclusively (traced to
// organization_invitations_dao.list_pending_invitations / find_invitation,
// both filtering `expires_at > now() AND accepted_at IS NULL`), so an expired
// invitation is reachable only by an ID Create does not have.
//
// The guarantee that guard existed for is preserved, just relocated: Read is
// where the lapse is detected and where reinvite_if_expired is honored,
// and Read keeps a lapsed resource IN state while the attribute is false. A
// lapsed resource therefore never reaches Create at all unless the practitioner
// opted in, so there is still no path on which an expired invitation is
// silently replaced with a fresh one.
func (r *OrganizationUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationUserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	email := plan.Email.ValueString()
	// The key. Deliberately the configured casing, not the API's: Anyscale stores
	// addresses lower-cased, and email is Required (not Computed), so echoing the
	// backend's casing back into state makes Terraform Core reject the apply
	// outright for any address containing an uppercase letter. Same failure the
	// invitation resource hit in v0.1.0; see its Create for the full history.
	plan.ID = types.StringValue(email)

	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, email)
	switch {
	case err == nil:
		// Adopt. Nothing is written to the API - this resource is declaring a fact
		// that is already true.
		applyMemberIdentity(&plan, collaborator)
		resp.Diagnostics.Append(writeMembershipRecord(ctx, resp.Private, organizationUserMembership{
			Origin: membershipOriginAdopted,
		})...)
		if resp.Diagnostics.HasError() {
			return
		}
		tflog.Info(ctx, "Adopted existing organization member", map[string]any{
			"email_domain": getEmailDomain(email),
		})
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return

	case !errors.Is(err, ErrNotFound):
		// A failed lookup is NOT "they are not a member". Inviting on a transient
		// error would mail someone who is already in the organization.
		resp.Diagnostics.AddError(
			"Could Not Check Organization Membership",
			fmt.Sprintf("Failed to look up %s in the organization before inviting them: %s\n\n"+
				"No invitation was sent.", email, err.Error()),
		)
		return
	}

	// Not a member: invite. Note the backend invalidates any pending invitation for
	// this address and issues a fresh one, so this is safe to retry, but it does
	// send another email and consume another unit of the daily quota.

	tflog.Info(ctx, "Inviting organization member", map[string]any{
		"email_domain": getEmailDomain(email),
	})

	// Shared with anyscale_organization_invitation: both resources POST to the
	// same endpoint, so the SSO guard lives in one place rather than being added
	// to whichever call site someone happens to be looking at.
	invitation, err := createOrganizationInvitation(ctx, r.client, email, &resp.Diagnostics)
	if err != nil {
		summary, detail := invitationCreateDiagnostic(email, err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	// Every Computed attribute must be resolved before Create returns - the plan
	// has them Unknown, and leaving one unknown fails the apply. Nobody has a
	// member record until they accept, so all four are genuinely null.
	clearMemberIdentity(&plan)
	plan.CreatedAt = types.StringNull()

	resp.Diagnostics.Append(writeMembershipRecord(ctx, resp.Private, organizationUserMembership{
		Origin:       membershipOriginInvited,
		InvitationID: invitation.ID,
	})...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Invited organization member", map[string]any{
		"invitation_id": invitation.ID,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read answers one question - where is this person right now? - and the ORDER
// it asks in is the design, not an optimization.
//
//  1. Members first. A hit means they are in, and nothing about invitations
//     matters any more.
//  2. Otherwise the invitation this resource recorded, fetched BY ID. The row is
//     never deleted (invalidate/expiry only move expires_at, acceptance only
//     sets accepted_at - traced to organization_invitations_dao), so a by-ID GET
//     is the only read that can tell pending from expired from accepted.
//  3. An expired read is NOT a conclusion. The backend invalidates the previous
//     invitation whenever a new one is created for the same address, so "ours
//     expired" is exactly what a third party re-inviting this person looks like.
//     Re-checking the pending list by email distinguishes the two, and a pending
//     invitation under a different id means we adopt theirs rather than
//     reporting a lapse that did not happen.
//  4. Only an expired stored invitation AND an empty pending list is a genuine
//     lapse.
//  5. No stored invitation at all still checks the pending list - that is the
//     terraform import path, and the whole point of the check.
//  6. Nothing anywhere: gone.
//
// Read also SELF-HEALS private state: ReadResponse.Private is pre-populated from
// the stored data and is writable (verified against the pinned framework -
// server_readresource.go points readReq.Private and readResp.Private at the same
// seeded ProviderData). That matters because nothing else can seed it: a state
// upgrader cannot reach private state at all, so resources predating this
// re-key, imported resources, and restored-from-backup state would otherwise
// have no marker for the rest of their lives.
func (r *OrganizationUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationUserResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	email := state.Email.ValueString()
	stored := readMembershipRecord(ctx, req.Private)

	tflog.Debug(ctx, "Reading organization user", map[string]any{
		"email_domain": getEmailDomain(email),
		"origin":       stored.Origin,
	})

	resolution, diags := r.resolveMembership(ctx, email, stored)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Persisted before branching on the outcome: every location except "absent"
	// wants the record, and the self-heal has to land even on a refresh that
	// changes nothing else about state.
	resp.Diagnostics.Append(writeMembershipRecord(ctx, resp.Private, resolution.Membership)...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch resolution.Location {
	case locationMember:
		applyMemberIdentity(&state, resolution.Collaborator)

	case locationPending, locationAccepted:
		// Not a member: nothing that exists only for a real member may survive.
		clearMemberIdentity(&state)
		state.CreatedAt = types.StringNull()

	case locationLapsed:
		clearMemberIdentity(&state)
		state.CreatedAt = types.StringNull()
		r.reportLapsedInvitation(ctx, &state, resolution, resp)
		return

	case locationAbsent:
		tflog.Warn(ctx, "Organization user is neither a member nor invited; removing from state", map[string]any{
			"email_domain": getEmailDomain(email),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	// email and id are deliberately never refreshed from the API here - see
	// applyMemberIdentity for why the operator's casing has to win.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// membershipLocation is where the person named by email actually is. Read's
// whole job is to work this out; everything after it is bookkeeping.
type membershipLocation string

const (
	// locationMember - in the organization. Nothing about invitations matters.
	locationMember membershipLocation = "member"
	// locationPending - invited, invitation still live and unaccepted.
	locationPending membershipLocation = "pending"
	// locationAccepted - the invitation was accepted but the member list has not
	// caught up yet. Transient by nature.
	locationAccepted membershipLocation = "accepted"
	// locationLapsed - the invitation expired unaccepted and nothing replaced it.
	locationLapsed membershipLocation = "lapsed"
	// locationAbsent - not a member, no invitation of any kind.
	locationAbsent membershipLocation = "absent"
)

// membershipResolution is resolveMembership's answer.
type membershipResolution struct {
	Location membershipLocation
	// Collaborator is set only for locationMember.
	Collaborator *OrganizationCollaboratorResult
	// Invitation is the invitation the location was decided from - the live one
	// for pending/accepted, the expired one for lapsed. Nil otherwise.
	Invitation *OrganizationInvitationResult
	// Membership is the record as it should now be stored, including any id
	// adopted from a replacement invitation and any lapse marker.
	Membership organizationUserMembership
}

// resolveMembership answers "where is this person right now?", and the ORDER it
// asks in is the design, not an optimization.
//
//  1. Members first. A hit means they are in, and nothing about invitations
//     matters any more.
//  2. Otherwise the invitation this resource recorded, fetched BY ID. The row is
//     never deleted (invalidation and expiry only move expires_at, acceptance
//     only sets accepted_at - traced to organization_invitations_dao), so a
//     by-ID GET is the only read that can tell pending from expired from
//     accepted.
//  3. An expired read is NOT a conclusion. The backend invalidates the previous
//     invitation whenever a new one is created for the same address, so "ours
//     expired" is exactly what a third party re-inviting this person looks like.
//     Re-checking the pending list by email separates the two, and a pending
//     invitation under a DIFFERENT id means adopting theirs rather than
//     reporting a lapse that never happened.
//  4. Only an expired stored invitation AND an empty pending list is a genuine
//     lapse.
//  5. No usable stored invitation still checks the pending list - that is the
//     terraform import path, and the whole point of the check.
//  6. Nothing anywhere: gone.
//
// Split out of Read so the ordering can be tested directly. Read owns the
// framework types (state, private state, diagnostics-with-side-effects); this
// owns the decision, and takes and returns plain values.
func (r *OrganizationUserResource) resolveMembership(
	ctx context.Context, email string, stored organizationUserMembership,
) (membershipResolution, diag.Diagnostics) {
	var diags diag.Diagnostics
	resolution := membershipResolution{Membership: stored}

	// Step 1: are they a member?
	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, email)
	switch {
	case err == nil:
		// Self-heal. An absent marker and an "adopted" marker route to the same
		// (do-nothing) Delete branch, so this changes no behavior - it makes the
		// reason a destroy did nothing legible to whoever reads private state
		// later. Nothing else can seed it: a state upgrader cannot reach private
		// state at all, so resources predating this re-key and state restored from
		// a backup would otherwise never have one. The absent case must stay safe
		// on its own regardless; see Delete.
		if resolution.Membership.Origin == "" {
			resolution.Membership.Origin = membershipOriginAdopted
		}
		// They are in. Any lapse this resource recorded is history.
		resolution.Membership.LapsedAt = ""
		resolution.Location = locationMember
		resolution.Collaborator = collaborator
		return resolution, diags

	case !errors.Is(err, ErrNotFound):
		// Only a genuine not-found may fall through to the invitation checks. A
		// timeout or a 500 must never be read as "they are not a member" - that
		// would drop a real member out of state, and the next apply would re-invite
		// somebody who already has access.
		diags.AddError(
			"Could Not Read Organization Membership",
			fmt.Sprintf("Failed to look up %s in the organization: %s", email, err.Error()),
		)
		return resolution, diags
	}

	// Step 2: the invitation this resource knows about, by ID.
	if resolution.Membership.InvitationID != "" {
		invitation, invErr := getOrganizationInvitation(ctx, r.client, resolution.Membership.InvitationID)
		switch {
		case invErr == nil:
			switch computeInvitationStatus(invitation.AcceptedAt, invitation.ExpiresAt) {
			case invitationStatusPending:
				resolution.Membership.LapsedAt = ""
				resolution.Location = locationPending
				resolution.Invitation = invitation
				return resolution, diags

			case invitationStatusAccepted:
				// Accepted, but step 1 did not find them - almost certainly the gap
				// between acceptance and the member list catching up. Reported as a
				// location rather than a failure so Read keeps the resource: dropping
				// it here would make the next apply try to re-invite somebody who just
				// joined. Logged rather than raised as a diagnostic because it clears
				// itself, and a warning every refresh for that is noise.
				tflog.Warn(ctx, "Invitation is accepted but the member list does not show them yet", map[string]any{
					"invitation_id": resolution.Membership.InvitationID,
					"email_domain":  getEmailDomain(email),
				})
				resolution.Membership.LapsedAt = ""
				resolution.Location = locationAccepted
				resolution.Invitation = invitation
				return resolution, diags

			default:
				// Step 3: expired is not yet lapsed.
				pending, pendErr := findPendingOrganizationInvitation(ctx, r.client, email)
				if pendErr != nil {
					diags.AddError(
						"Could Not Check Pending Organization Invitations",
						fmt.Sprintf("The invitation recorded for %s has expired, but the pending-invitation list could "+
							"not be checked to see whether a newer one replaced it: %s\n\nRefusing to report a lapsed "+
							"invitation without that check.", email, pendErr.Error()),
					)
					return resolution, diags
				}
				if pending != nil {
					// Somebody re-invited them, which is what invalidated ours. Take over
					// the invitation that actually exists so destroy still cancels the
					// right one and no lapse is reported for an address that has a live
					// invitation sitting in the recipient's inbox.
					tflog.Info(ctx, "Adopting a newer pending invitation that replaced the recorded one", map[string]any{
						"previous_invitation_id": resolution.Membership.InvitationID,
						"invitation_id":          pending.ID,
					})
					resolution.Membership.InvitationID = pending.ID
					resolution.Membership.LapsedAt = ""
					resolution.Location = locationPending
					resolution.Invitation = pending
					return resolution, diags
				}

				// Step 4: genuinely lapsed. The stale invitation_id is deliberately
				// kept - it is the only record of what was sent, and Delete uses it to
				// confirm there is nothing left to cancel.
				//
				// The lapse date comes from the invitation's own expires_at: that is
				// the real date, and it is stable, so the marker does not churn on
				// every refresh. Falls back to a previously recorded marker, then to
				// now, so the warning is never dateless.
				lapsedAt := invitation.ExpiresAt
				if lapsedAt == "" {
					lapsedAt = resolution.Membership.LapsedAt
				}
				if lapsedAt == "" {
					lapsedAt = time.Now().UTC().Format(time.RFC3339)
				}
				resolution.Membership.LapsedAt = lapsedAt
				resolution.Location = locationLapsed
				resolution.Invitation = invitation
				return resolution, diags
			}

		case errors.Is(invErr, ErrNotFound):
			// Should not happen - the row outlives both expiry and acceptance - but a
			// recorded id that no longer resolves must not strand the resource. Fall
			// through to the by-email check below.
			tflog.Warn(ctx, "Recorded invitation no longer resolves; falling back to a lookup by email", map[string]any{
				"invitation_id": resolution.Membership.InvitationID,
			})
			resolution.Membership.InvitationID = ""

		default:
			diags.AddError(
				"Could Not Read Organization Invitation",
				fmt.Sprintf("Failed to read invitation %s for %s: %s",
					resolution.Membership.InvitationID, email, invErr.Error()),
			)
			return resolution, diags
		}
	}

	// Step 5: no usable recorded invitation. This is the terraform import path,
	// and the recovery path for state whose private data was lost.
	pending, err := findPendingOrganizationInvitation(ctx, r.client, email)
	if err != nil {
		diags.AddError(
			"Could Not Check Pending Organization Invitations",
			fmt.Sprintf("Failed to look for a pending invitation for %s: %s", email, err.Error()),
		)
		return resolution, diags
	}
	if pending != nil {
		resolution.Membership.InvitationID = pending.ID
		if resolution.Membership.Origin == "" {
			// Deliberately NOT membershipOriginInvited. Arriving here means an
			// invitation exists that this resource has no record of sending, so
			// claiming it would have destroy revoke somebody else's invitation.
			// ImportState is different and does record "invited": importing is an
			// explicit declaration of ownership, this is a recovery guess.
			resolution.Membership.Origin = membershipOriginImported
		}
		resolution.Membership.LapsedAt = ""
		resolution.Location = locationPending
		resolution.Invitation = pending
		return resolution, diags
	}

	// Step 6: not a member, no invitation of any kind.
	resolution.Location = locationAbsent
	return resolution, diags
}

// reportLapsedInvitation handles a lapsed resolution: the recorded invitation
// expired and nothing replaced it.
//
// The resource is KEPT in state by default. Dropping it would turn the next plan
// into a create, and that create would mail the person again - a plan that
// changed from no-op to create purely because time passed, on a cadence set by
// the backend's expiry window rather than by anything the practitioner did.
//
// reinvite_if_expired is honored HERE rather than in Create because here
// is the only place it can be. Create cannot see private state at all
// (resource.CreateRequest has no Private field), and Update is a no-op, so a
// refresh is the sole point where the lapse and the practitioner's opt-in are
// both visible. False (the default) keeps the resource and warns every refresh;
// true drops it, which makes the next plan a visible, reviewable create.
func (r *OrganizationUserResource) reportLapsedInvitation(
	ctx context.Context,
	state *OrganizationUserResourceModel,
	resolution membershipResolution,
	resp *resource.ReadResponse,
) {
	email := state.Email.ValueString()
	lapsedAt := resolution.Membership.LapsedAt

	if state.ReinviteIfExpired.ValueBool() {
		resp.Diagnostics.AddAttributeWarning(
			path.Root("reinvite_if_expired"),
			"A New Organization Invitation Will Be Sent",
			fmt.Sprintf("The invitation for %s expired on %s without being accepted, and reinvite_if_expired is "+
				"true, so this resource has been dropped from state and the next plan will show it being created "+
				"again. Applying that plan sends a NEW invitation, which counts against the organization's daily "+
				"invitation limit.\n\nNothing is resent: the expired invitation is dead and the new one carries a "+
				"new link, so tell %s to use the new email - the link in the original will not work.\n\nSet "+
				"reinvite_if_expired back to false if you would rather be warned and left alone.",
				email, lapsedAt, email),
		)
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.AddWarning(
		"Organization Invitation Expired Without Being Accepted",
		fmt.Sprintf("The invitation for %s expired on %s and was never accepted, so they still do not have access. "+
			"Nothing has been sent - this resource does not silently invite people again because an invitation "+
			"aged out.\n\nSet reinvite_if_expired = true on this resource to have Terraform issue a new invitation "+
			"(it shows up as a create in the next plan, and counts against the organization's daily invitation "+
			"limit). Note that this sends a NEW invitation with a NEW link rather than resending the original, "+
			"whose link is dead. Leaving it false keeps this warning on every refresh and sends nothing.",
			email, lapsedAt),
	)

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// applyMemberIdentity copies the fields that only exist once someone is a real
// member. Email and id are deliberately NOT among them: email is Required, so
// overwriting it with the API's lower-cased echo makes Terraform Core reject the
// apply for any address containing an uppercase letter, and id mirrors email.
//
// created_at is write-once. It starts null (nobody has one while an invitation
// is pending), gets its single write the first time the person is seen in the
// member list, and is never re-read - the API has returned different created_at
// values across reads for the same member (task 4745d9fb), and re-syncing it
// caused "Provider produced inconsistent result after apply".
func applyMemberIdentity(model *OrganizationUserResourceModel, collaborator *OrganizationCollaboratorResult) {
	model.IdentityID = types.StringValue(collaborator.ID)

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

	if (model.CreatedAt.IsNull() || model.CreatedAt.IsUnknown()) && collaborator.CreatedAt != "" {
		model.CreatedAt = types.StringValue(collaborator.CreatedAt)
	}
}

// clearMemberIdentity nulls the member-only fields for someone who is not (or is
// no longer) in the organization. created_at is left to the caller: Create must
// null it to resolve the plan's unknown, Read nulls it too, but they are
// different decisions and collapsing them hides that.
func clearMemberIdentity(model *OrganizationUserResourceModel) {
	model.IdentityID = types.StringNull()
	model.UserID = types.StringNull()
	model.Name = types.StringNull()
}

// Update is a deliberate no-op that copies plan into state.
//
// Nothing here is updatable through the API. email is the key and forces
// replacement; every other attribute is Computed from the backend.
// reinvite_if_expired is writable but is pure provider-side policy - it
// changes what a future REFRESH does, not anything in Anyscale - so flipping it
// is a state write and nothing more. Role changes, the only thing Update ever
// did on this resource, moved to anyscale_organization_user_role.
//
// Deliberately not an error. An error here would make `reinvite_if_expired
// = true` unappliable, which is the one thing a practitioner staring at a lapsed
// -invitation warning is being told to do.
//
// resp.Private is left untouched on purpose: the framework seeds it from the
// planned private data (verified against the pinned source - updateResp.Private
// is set to req.PlannedPrivate.Provider), so not writing preserves the
// membership record. Overwriting it here would lose the invitation id and turn
// the next destroy into a silent no-op.
func (r *OrganizationUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan OrganizationUserResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Update is a no-op for anyscale_organization_user", map[string]any{
		"email_domain": getEmailDomain(plan.Email.ValueString()),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete cancels an invitation. It NEVER removes a human from the organization.
//
// DELETE /api/v2/organization_collaborators/{identity_id} does not appear
// anywhere in this file, and must not: that endpoint deprovisions a real person,
// there is no undo (they have to be re-invited and re-accept), and a shared test
// identity was destroyed that way during development. Removing someone's access
// is a deliberate act this resource deliberately cannot perform.
//
// What destroy does is decided entirely by what this resource RECORDED about how
// the person got here, read from private state - which DeleteRequest does carry,
// unlike CreateRequest:
//
//   - invited, still pending -> invalidate it. Terraform sent it, so Terraform
//     can take it back, and the recipient can no longer accept.
//   - invited, already accepted -> nothing, and say so. They are a member now;
//     that membership outlived the invitation and is not ours to undo.
//   - invited, already expired -> nothing to cancel.
//   - adopted -> nothing, and say so. They were a member before Terraform
//     declared it and stay one afterward.
//   - imported/absent -> nothing. This resource has no record of creating
//     anything, so it destroys nothing. That default must stay safe on its own:
//     Read seeds a marker for resources that never had one, but a destroy that
//     happens before any refresh still lands here.
func (r *OrganizationUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationUserResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Origin comes from private state, whose type is internal to the framework and
	// cannot be constructed in a unit test. Reading it here and delegating keeps
	// every branch below reachable from a test - without this seam a mutation in
	// any branch except the default one goes undetected, which is exactly what
	// happened before this was extracted.
	r.deleteMembership(ctx, state, readMembershipRecord(ctx, req.Private), resp)
}

func (r *OrganizationUserResource) deleteMembership(
	ctx context.Context, state OrganizationUserResourceModel, membership organizationUserMembership, resp *resource.DeleteResponse,
) {
	email := state.Email.ValueString()

	switch membership.Origin {
	case membershipOriginInvited:
		r.deleteInvitedMembership(ctx, email, membership, resp)

	case membershipOriginAdopted:
		resp.Diagnostics.AddWarning(
			"Organization Member Left In Place",
			fmt.Sprintf("%s was already a member of the organization when this resource adopted them, so destroying "+
				"it removed nothing - they still have access. This resource cannot remove people from the "+
				"organization; do that from the Anyscale console if that is what you intend.", email),
		)

	case membershipOriginImported:
		if membership.InvitationID != "" {
			resp.Diagnostics.AddWarning(
				"Organization Invitation Left Standing",
				fmt.Sprintf("%s has a pending invitation that this resource adopted rather than sent, so destroying "+
					"this resource did not cancel it - they can still accept it. Destroy the "+
					"anyscale_organization_invitation that owns it, or invalidate it from the Anyscale console, if "+
					"it should not stand.", email),
			)
			return
		}
		tflog.Debug(ctx, "Nothing to destroy for an adopted organization user", map[string]any{
			"email_domain": getEmailDomain(email),
		})

	default:
		// No record at all. Predates this re-key, or was written by a provider
		// version that did not record one. Doing nothing is the safe direction.
		tflog.Debug(ctx, "No membership record in private state; destroying nothing", map[string]any{
			"email_domain": getEmailDomain(email),
		})
	}
}

// deleteInvitedMembership handles the one branch that actually calls the API.
func (r *OrganizationUserResource) deleteInvitedMembership(
	ctx context.Context, email string, membership organizationUserMembership, resp *resource.DeleteResponse,
) {
	if membership.InvitationID == "" {
		resp.Diagnostics.AddWarning(
			"Organization Invitation Could Not Be Cancelled",
			fmt.Sprintf("This resource recorded that it invited %s but not which invitation, so there was nothing to "+
				"cancel. If an invitation is still outstanding, invalidate it from the Anyscale console.", email),
		)
		return
	}

	invitation, err := getOrganizationInvitation(ctx, r.client, membership.InvitationID)
	switch {
	case errors.Is(err, ErrNotFound):
		tflog.Debug(ctx, "Recorded invitation no longer exists; nothing to cancel", map[string]any{
			"invitation_id": membership.InvitationID,
		})
		return
	case err != nil:
		// Deliberately an error, not a warning. Whether to cancel depends on the
		// invitation's status, and guessing in either direction is wrong: skipping
		// leaves a live invitation the practitioner asked to revoke, cancelling
		// blind acts on a status nobody confirmed. Failing keeps the resource in
		// state so a retry can finish the job.
		resp.Diagnostics.AddError(
			"Could Not Determine Organization Invitation Status",
			fmt.Sprintf("Failed to read invitation %s for %s before cancelling it: %s\n\nNothing was cancelled. "+
				"Re-run the destroy once the API is reachable.", membership.InvitationID, email, err.Error()),
		)
		return
	}

	switch computeInvitationStatus(invitation.AcceptedAt, invitation.ExpiresAt) {
	case invitationStatusPending:
		tflog.Info(ctx, "Invalidating organization invitation", map[string]any{
			"invitation_id": membership.InvitationID,
		})
		_, err := DoRequestRaw(
			ctx, r.client, "POST",
			fmt.Sprintf("/api/v2/organization_invitations/%s/invalidate", membership.InvitationID), nil,
			http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound,
		)
		if err != nil {
			resp.Diagnostics.AddError(
				"Could Not Cancel Organization Invitation",
				invitationErrorDetail(err),
			)
		}

	case invitationStatusAccepted:
		resp.Diagnostics.AddWarning(
			"Organization Member Left In Place",
			fmt.Sprintf("%s accepted their invitation and is now a member of the organization, so destroying this "+
				"resource removed nothing - they still have access. Cancelling an invitation that has already been "+
				"used does not undo the membership it created; remove them from the Anyscale console if that is "+
				"what you intend.", email),
		)

	default:
		tflog.Debug(ctx, "Recorded invitation had already expired; nothing to cancel", map[string]any{
			"invitation_id": membership.InvitationID,
		})
	}
}

// ImportState imports by EMAIL. The import argument used to be an identity_id,
// and someone following an older runbook is exactly who lands here, so an
// identity-shaped argument gets told what changed rather than "no member found
// with email ide_...", which sends them hunting for the wrong problem.
//
// The test is the absence of "@", not a prefix: every email has one, no Anyscale
// ID can (they are a prefix plus 26 characters from a lowercase-alphanumeric
// charset), and that stays true if Anyscale adds ID prefixes. The ide_ prefix is
// only used to make the message more specific when it happens to match.
//
// The email written to state is the one SUPPLIED IN THE IMPORT ID, never the
// API's echo of it - Anyscale lower-cases every address it stores, and email is
// Required, so importing Alice@Example.com and storing alice@example.com makes
// the very next plan want to replace the resource. Create and Read follow the
// same rule; all three paths must agree, or a plan modifier ends up papering
// over the disagreement (which is exactly what happened on
// anyscale_organization_invitation, where ImportState writes the API's casing
// and only caseInsensitiveEmailPlanModifier keeps it from replacing).
//
// The private-state marker is written HERE rather than left to Read's self-heal:
// ImportStateResponse carries a pre-populated, writable Private, and importing
// is adopting by definition - nobody imports a person they just invited.
func (r *OrganizationUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	email := strings.TrimSpace(req.ID)

	if email == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			"Expected the email address of an organization member or invitee, for example:\n"+
				"  terraform import anyscale_organization_user.example user@example.com",
		)
		return
	}

	if !strings.Contains(email, "@") {
		looksLikeIdentity := ""
		if strings.HasPrefix(email, "ide_") {
			looksLikeIdentity = fmt.Sprintf("%q looks like an identity ID. ", email)
		}
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("%sThis resource's import argument is now an EMAIL ADDRESS, not an identity ID.\n\n"+
				"  terraform import anyscale_organization_user.example user@example.com\n\n"+
				"The resource was re-keyed to email because identity_id and user_id only exist once someone has "+
				"accepted an invitation, while email identifies the same person before, during and after that. "+
				"identity_id is still available as a read-only attribute.", looksLikeIdentity),
		)
		return
	}

	tflog.Info(ctx, "Importing organization user", map[string]any{
		"email_domain": getEmailDomain(email),
	})

	model := OrganizationUserResourceModel{
		ID:    types.StringValue(email),
		Email: types.StringValue(email),
		// Optional+Computed with a default: set it explicitly, or the first plan
		// after import shows an update applying the default to a null state value.
		ReinviteIfExpired: types.BoolValue(false),
	}
	clearMemberIdentity(&model)
	model.CreatedAt = types.StringNull()

	collaborator, err := findOrgCollaboratorByEmail(ctx, r.client, email)
	switch {
	case err == nil:
		applyMemberIdentity(&model, collaborator)
		resp.Diagnostics.Append(writeMembershipRecord(ctx, resp.Private, organizationUserMembership{
			Origin: membershipOriginAdopted,
		})...)

	case !errors.Is(err, ErrNotFound):
		resp.Diagnostics.AddError(
			"Could Not Import Organization User",
			fmt.Sprintf("Failed to look up %s in the organization: %s", email, err.Error()),
		)
		return

	default:
		// Not a member. They may still have an invitation outstanding, which is a
		// perfectly importable state for this resource.
		pending, pendErr := findPendingOrganizationInvitation(ctx, r.client, email)
		if pendErr != nil {
			resp.Diagnostics.AddError(
				"Could Not Import Organization User",
				fmt.Sprintf("%s is not a member of the organization, and the pending-invitation list could not be "+
					"checked: %s", email, pendErr.Error()),
			)
			return
		}
		if pending == nil {
			resp.Diagnostics.AddError(
				"Organization User Not Found",
				fmt.Sprintf("No organization member and no pending invitation found for %q.\n\nUse the "+
					"anyscale_organization_users data source to list members and their email addresses. If this "+
					"person was invited and the invitation has since expired, there is nothing to import - declare "+
					"the resource and apply to invite them again.", email),
			)
			return
		}
		// Recorded as invited, so destroy can cancel it: importing an outstanding
		// invitation is taking ownership of it, and a live invitation is a real
		// thing this resource is now expected to be able to take back. Contrast
		// Read's by-email recovery, which reaches the same API state without any
		// such declaration of intent and therefore stays at "imported".
		resp.Diagnostics.Append(writeMembershipRecord(ctx, resp.Private, organizationUserMembership{
			Origin:       membershipOriginInvited,
			InvitationID: pending.ID,
		})...)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
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

// Invitation status values, shared with computeInvitationStatus (which returns
// these literals) so the switches that branch on it cannot drift by a typo.
const (
	invitationStatusPending  = "pending"
	invitationStatusAccepted = "accepted"
	invitationStatusExpired  = "expired"
)

// getOrganizationInvitation fetches one invitation BY ID.
//
// This is the only read that can distinguish pending from expired from
// accepted. The list endpoint cannot: both of its code paths filter on
// `expires_at > now() AND accepted_at IS NULL` (traced to
// organization_invitations_dao.list_pending_invitations and find_invitation), so
// an expired or accepted invitation is invisible there. The row itself is never
// deleted - invalidation moves expires_at to now, acceptance sets accepted_at -
// so a recorded id keeps resolving for the life of the organization.
//
// StatusNotFound is deliberately NOT in the accepted list, so a 404 comes back
// as ErrNotFound rather than as an indistinguishable success.
func getOrganizationInvitation(ctx context.Context, client *Client, invitationID string) (*OrganizationInvitationResult, error) {
	invitation, err := DoRequestAndParse[OrganizationInvitationResponse](
		ctx, client, "GET", fmt.Sprintf("/api/v2/organization_invitations/%s", invitationID), nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return &invitation.Result, nil
}

// findPendingOrganizationInvitation returns the organization's outstanding
// invitation for email, or nil if there is none.
//
// "Pending" is the endpoint's own definition, not a filter applied here: with an
// email query parameter the backend runs find_invitation, which returns at most
// one row and requires `expires_at > now() AND accepted_at IS NULL`. So a nil
// return means "no LIVE invitation", never "no invitation ever" - which is
// exactly the distinction Read's step 3 needs, and exactly why an expired
// invitation cannot be found this way.
//
// Matched case-insensitively client-side as well: the backend already compares
// LOWER(email), but the server-side filter is treated as a narrowing hint
// everywhere else in this provider and this keeps that consistent.
func findPendingOrganizationInvitation(ctx context.Context, client *Client, email string) (*OrganizationInvitationResult, error) {
	invitations, err := PaginatedRequest(
		ctx, client, "/api/v2/organization_invitations",
		url.Values{"count": []string{"50"}, "email": []string{email}},
		func(body []byte) ([]OrganizationInvitationResult, *string, error) {
			var listResp OrganizationInvitationsListResponse
			if err := json.Unmarshal(body, &listResp); err != nil {
				return nil, nil, fmt.Errorf("error parsing response: %w", err)
			}
			return listResp.Results, listResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, err
	}

	for i := range invitations {
		if strings.EqualFold(invitations[i].Email, email) {
			return &invitations[i], nil
		}
	}
	return nil, nil
}

// Private state: what this resource DID, which is the only thing that decides
// what destroy is allowed to undo.
//
// State cannot answer it. State says where the person is now; it cannot say
// whether Terraform put them there. A member found by Read looks identical
// whether this resource invited them, adopted them, or found them by accident,
// and destroy must treat those three completely differently.
//
// One key holding a JSON object rather than several flat keys: the three facts
// are written together and read together, and a partial write (an invitation id
// without its origin) is a state this code should not be able to produce.
const organizationUserMembershipKey = "membership"

const (
	// membershipOriginInvited - this resource sent the invitation. The only
	// origin that permits destroy to call the API.
	membershipOriginInvited = "invited"
	// membershipOriginAdopted - already a member when this resource first saw
	// them. Destroy leaves them alone.
	membershipOriginAdopted = "adopted"
	// membershipOriginImported - an invitation somebody else sent, picked up by
	// import or by Read's by-email recovery. Destroy leaves it standing: the
	// resource did not create it, so it does not get to revoke it.
	membershipOriginImported = "imported"
)

// organizationUserMembership is the private-state payload.
type organizationUserMembership struct {
	Origin       string `json:"origin"`
	InvitationID string `json:"invitation_id,omitempty"`
	// LapsedAt is the expiry timestamp of an invitation that ran out without
	// being accepted. Sourced from the invitation's own expires_at so it does not
	// churn between refreshes.
	LapsedAt string `json:"lapsed_at,omitempty"`
}

// readMembershipRecord reads the record back, defaulting to an EMPTY record on
// anything unexpected - absent key, unreadable data, malformed JSON.
//
// That default is the safe direction, and it is the whole reason Delete's
// unknown-origin branch does nothing: a resource created before this key
// existed, or any future case where private state does not survive, under-acts
// rather than cancelling an invitation it has no record of sending.
func readMembershipRecord(ctx context.Context, private privateStateGetter) organizationUserMembership {
	if private == nil {
		return organizationUserMembership{}
	}
	raw, diags := private.GetKey(ctx, organizationUserMembershipKey)
	if diags.HasError() || len(raw) == 0 {
		return organizationUserMembership{}
	}
	var membership organizationUserMembership
	if err := json.Unmarshal(raw, &membership); err != nil {
		tflog.Warn(ctx, "Could not parse organization user private state; treating it as absent", map[string]any{
			"error": err.Error(),
		})
		return organizationUserMembership{}
	}
	return membership
}

// writeMembershipRecord persists the record for a later Read or Delete to use.
func writeMembershipRecord(ctx context.Context, private privateStateSetter, membership organizationUserMembership) diag.Diagnostics {
	var diags diag.Diagnostics

	// The framework pre-populates Private on Create, Read and Import (verified
	// against the pinned version), but a hand-built response in a unit test does
	// not. Tolerate nil rather than panicking; a nil here in production would mean
	// the framework changed.
	if private == nil || reflect.ValueOf(private).IsNil() {
		return diags
	}

	payload, err := json.Marshal(membership)
	if err != nil {
		diags.AddError(
			"Could Not Record Organization User Private State",
			fmt.Sprintf("Failed to serialize the membership record: %s", err.Error()),
		)
		return diags
	}
	return private.SetKey(ctx, organizationUserMembershipKey, payload)
}

// invitationCreateDiagnostic names the four ways POST
// /api/v2/organization_invitations fails that a practitioner can actually act
// on, rather than handing back a raw status and a JSON blob.
//
// Detection is on the backend's own detail text because the status code cannot
// separate them - three of these are the same 400. The strings are traced to
// org_invites_service.create_invitation and
// collaborators_service_utils.DEFAULT_DIRECTORY_SYNC_ERROR_MESSAGE, and the
// limits' defaults (20 and 20) are the LaunchDarkly variation defaults in that
// same function, which is why they are quoted as defaults rather than as facts.
func invitationCreateDiagnostic(email string, err error) (summary string, detail string) {
	backendDetail := extractAPIErrorDetail(err)
	lowered := strings.ToLower(backendDetail)

	switch {
	case strings.Contains(lowered, "pending invitations"):
		return "Organization Pending Invitation Limit Reached",
			fmt.Sprintf("%s\n\nThis limit is per ORGANIZATION and is shared with the Anyscale console - it counts "+
				"every invitation currently awaiting acceptance, not just the ones Terraform sent. The default is 20; "+
				"Anyscale can raise it per organization.\n\nA for_each onboarding more people than the limit allows "+
				"fails partway through the apply, leaving the earlier invitations sent. Onboard in batches, wait for "+
				"people to accept, cancel invitations that are no longer wanted, or ask Anyscale to raise the "+
				"limit.\n\nNo invitation was sent for %s.", backendDetail, email)

	case strings.Contains(lowered, "invitations can be sent in the past 24 hours"):
		return "Organization Daily Invitation Limit Reached",
			fmt.Sprintf("%s\n\nThis limit is per ORGANIZATION and is shared with the Anyscale console - it counts "+
				"every invitation sent in the last 24 hours from anywhere, including ones already accepted or "+
				"expired, not just the ones Terraform sent. The default is 20 per 24 hours; Anyscale can raise it "+
				"per organization.\n\nRe-apply after the window rolls forward, or ask Anyscale to raise the "+
				"limit.\n\nNo invitation was sent for %s.", backendDetail, email)

	case strings.Contains(lowered, "policy api"):
		return "Organization Membership Is Managed By Directory Sync",
			fmt.Sprintf("%s\n\nThis organization has directory sync (SCIM) enabled with the Policy API, so the "+
				"Anyscale backend refuses to create invitations at all - your identity provider owns who is a member. "+
				"Terraform cannot add %s here.\n\nAdd them in your IdP instead. This resource can still adopt and "+
				"read people who are already members; only inviting is blocked. See "+
				"https://docs.anyscale.com/reference/cli/policy#policy-cli for the Policy API tooling.",
				backendDetail, email)

	case strings.Contains(lowered, "already a member of your organization"):
		return "Email Already Belongs to an Organization Member",
			fmt.Sprintf("%s\n\nThis resource adopts existing members instead of inviting them, so this should not "+
				"normally surface - seeing it means %s became a member in between this resource checking the member "+
				"list and sending the invitation. Re-run the apply: the membership check will find them and adopt "+
				"them, with no invitation sent.", backendDetail, email)
	}

	return "Could Not Invite Organization Member",
		fmt.Sprintf("Failed to invite %s: %s", email, backendDetail)
}

// invitationErrorDetail surfaces the backend's own error text for an invitation
// call, in place of the "unexpected status NNN: {raw json}" wrapper.
func invitationErrorDetail(err error) string {
	return extractAPIErrorDetail(err)
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
