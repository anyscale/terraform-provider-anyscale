package provider

import (
	"fmt"

	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// organizationInvitationMoveStateSourceTypeName is the resource type name
// this resource was called before the rename into the anyscale_iam_
// namespace. State written under that name moves here via a `moved` block:
//
//	moved {
//	  from = anyscale_organization_invitation.pending
//	  to   = anyscale_iam_organization_invitation.pending
//	}
const organizationInvitationMoveStateSourceTypeName = "anyscale_organization_invitation"

// Ensure OrganizationInvitationResource implements the move-state interface.
var _ resource.ResourceWithMoveState = &OrganizationInvitationResource{}

// MoveState lets existing state written under the pre-rename type name
// anyscale_organization_invitation move to
// anyscale_iam_organization_invitation with a `moved` block instead of state
// rm + re-import.
//
// The invitation resource has only ever been schema version 0, so a single
// schema-carrying mover copies state through unchanged and a trailing mover
// errors on any other version. As with anyscale_iam_organization_user, every
// mover gates on the request fields before touching SourceState (the
// framework calls movers even when their SourceSchema failed to decode), the
// move performs no API call, and private state is copied because the source
// data is not copied automatically.
func (r *OrganizationInvitationResource) MoveState(ctx context.Context) []resource.StateMover {
	v0Schema := organizationInvitationSchema()

	return []resource.StateMover{
		{
			SourceSchema: &v0Schema,
			StateMover:   r.moveStateOrganizationInvitationV0,
		},
		{
			// Ordered last: the schema-carrying mover above answers the only
			// supported version; anything left is unsupported.
			StateMover: r.moveStateOrganizationInvitationUnsupportedVersion,
		},
	}
}

func (r *OrganizationInvitationResource) moveStateOrganizationInvitationV0(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != organizationInvitationMoveStateSourceTypeName ||
		req.SourceSchemaVersion != 0 ||
		!moveStateSourceProviderMatches(req.SourceProviderAddress) {
		return
	}

	if req.SourceState == nil {
		// See organizationUserMoveStateDecodeFailed for why this errors
		// instead of skipping to Core's generic message.
		resp.Diagnostics.AddError(
			fmt.Sprintf("Cannot read anyscale_organization_invitation state at schema version %d", req.SourceSchemaVersion),
			"The source state for a move into anyscale_iam_organization_invitation did not "+
				"match the schema. Remove the resource from state and re-import it under the "+
				"new name with the invitation ID as the import ID:\n\n"+
				"terraform state rm <old address>\n"+
				"terraform import anyscale_iam_organization_invitation.example <invitation ID>",
		)
		return
	}

	var model OrganizationInvitationResourceModel
	resp.Diagnostics.Append(req.SourceState.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.TargetState.Set(ctx, &model)...)
	resp.TargetPrivate = req.SourcePrivate
}

func (r *OrganizationInvitationResource) moveStateOrganizationInvitationUnsupportedVersion(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != organizationInvitationMoveStateSourceTypeName ||
		!moveStateSourceProviderMatches(req.SourceProviderAddress) ||
		req.SourceSchemaVersion == 0 {
		return
	}

	resp.Diagnostics.AddError(
		fmt.Sprintf("Cannot move anyscale_organization_invitation state at schema version %d", req.SourceSchemaVersion),
		"This resource only understands anyscale_organization_invitation state at schema version 0. "+
			"Remove the resource from state and re-import it under the new name with the invitation ID as the import ID:\n\n"+
			"terraform state rm <old address>\n"+
			"terraform import anyscale_iam_organization_invitation.example <invitation ID>",
	)
}
