package provider

import (
	"fmt"
	"strings"

	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// organizationUserMoveStateSourceTypeName is the resource type name this
// resource was called before the rename into the anyscale_iam_ namespace.
// State written under that name moves here via a `moved` block:
//
//	moved {
//	  from = anyscale_organization_user.alice
//	  to   = anyscale_iam_organization_user.alice
//	}
const organizationUserMoveStateSourceTypeName = "anyscale_organization_user"

// Ensure OrganizationUserResource implements the move-state interface.
var _ resource.ResourceWithMoveState = &OrganizationUserResource{}

// moveStateSourceProviderMatches reports whether a MoveResourceState request
// came from this provider, comparing only the TYPE segment of the address so
// any host/namespace form is accepted: production sends registry URLs under
// the anyscale namespace, and the framework's test harness sends
// registry.terraform.io/hashicorp/anyscale.
//
// A different provider using the type name anyscale_organization_user with a
// different type segment is declined; Core then reports its own unsupported
// move error.
func moveStateSourceProviderMatches(sourceProviderAddress string) bool {
	segments := strings.Split(sourceProviderAddress, "/")
	return segments[len(segments)-1] == "anyscale"
}

// MoveState lets existing state written under the pre-rename type name
// anyscale_organization_user move to anyscale_iam_organization_user with a
// `moved` block instead of state rm + re-import.
//
// The movers perform no API call; the next refresh reads the object under the
// new type. Per mover:
//
//   - v1 (schema version 1): state copies through unchanged.
//   - v0 (pre-v0.25.0, `id` = identity_id): the shared v0 -> v1 re-key in
//     organizationUserV1ModelFromV0 is applied, the same transform
//     UpgradeState runs.
//   - any other schema version: the trailing mover errors naming the
//     version and the recovery.
//
// Every mover gates on SourceTypeName, the provider type segment, and the
// schema version before touching SourceState: the framework calls a mover
// even when its SourceSchema failed to decode the request (that failure is
// only DEBUG-logged), so the request fields are the only reliable signal.
// A mover that returns no state and no diagnostics is a skip; an error from
// any mover preempts the rest, so the catch-all is ordered last.
func (r *OrganizationUserResource) MoveState(ctx context.Context) []resource.StateMover {
	v1Schema := organizationUserSchemaV1()
	v0Schema := organizationUserSchemaV0()

	return []resource.StateMover{
		{
			SourceSchema: &v1Schema,
			StateMover:   r.moveStateOrganizationUserV1,
		},
		{
			SourceSchema: v0Schema,
			StateMover:   r.moveStateOrganizationUserV0,
		},
		{
			// Ordered last: schema-carrying movers above answer the supported
			// versions; anything left for this one is unsupported.
			StateMover: r.moveStateOrganizationUserUnsupportedVersion,
		},
	}
}

func (r *OrganizationUserResource) moveStateOrganizationUserV1(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != organizationUserMoveStateSourceTypeName ||
		req.SourceSchemaVersion != 1 ||
		!moveStateSourceProviderMatches(req.SourceProviderAddress) ||
		req.SourceState == nil {
		return
	}

	var model OrganizationUserResourceModel
	resp.Diagnostics.Append(req.SourceState.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.TargetState.Set(ctx, &model)...)
	resp.TargetPrivate = req.SourcePrivate
}

func (r *OrganizationUserResource) moveStateOrganizationUserV0(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != organizationUserMoveStateSourceTypeName ||
		req.SourceSchemaVersion != 0 ||
		!moveStateSourceProviderMatches(req.SourceProviderAddress) ||
		req.SourceState == nil {
		return
	}

	var v0 organizationUserResourceModelV0
	resp.Diagnostics.Append(req.SourceState.Get(ctx, &v0)...)
	if resp.Diagnostics.HasError() {
		return
	}
	upgraded := organizationUserV1ModelFromV0(v0)
	resp.Diagnostics.Append(resp.TargetState.Set(ctx, &upgraded)...)
	resp.TargetPrivate = req.SourcePrivate
}

func (r *OrganizationUserResource) moveStateOrganizationUserUnsupportedVersion(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != organizationUserMoveStateSourceTypeName ||
		!moveStateSourceProviderMatches(req.SourceProviderAddress) ||
		req.SourceSchemaVersion == 0 || req.SourceSchemaVersion == 1 {
		return
	}

	resp.Diagnostics.AddError(
		fmt.Sprintf("Cannot move anyscale_organization_user state at schema version %d", req.SourceSchemaVersion),
		fmt.Sprintf(
			"This resource only understands anyscale_organization_user state at schema version 0 or 1. "+
				"Remove the resource from state and re-import it under the new name with the email as the import ID:\n\n"+
				"terraform state rm <old address>\n"+
				"terraform import anyscale_iam_organization_user.example <email>",
		),
	)
}
