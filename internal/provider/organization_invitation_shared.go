package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Shared invitation-creation path for anyscale_organization_user and
// anyscale_organization_invitation.
//
// It exists because both resources POST to the same endpoint from two separate
// call sites, so any guard added to one silently misses the other. That is the
// duplicated-leaf shape: same defect, same endpoint, two places, and a fix that
// looks complete while covering half of it.

// ssoModeRequired is the one SSOMode value that blocks invitation acceptance.
//
// Deliberately NOT keyed on the org's `sso_required` field, which is on the same
// response and reads like the obvious choice. It is DERIVED, and the derivation
// includes optional:
//
//	organizations_service.py:53  sso_required = (sso_mode in [optional, required])
//	organizations_router.py:40   sso_required = (sso_mode != off)
//
// So `sso_required` means "SSO is available", not "SSO is mandatory". Guarding
// on it would refuse in optional-mode organizations, which accept invitations
// perfectly well - breaking a working configuration to prevent a problem those
// orgs do not have. The accept-side 403 fires on `required` alone
// (users_service.py:946), and no backend site gates acceptance on optional -
// checked across every sso_mode comparison in the backend, not just the one
// file the gate lives in.
const ssoModeRequired = "required"

// organizationSSOInfoResponse is the subset of GET /api/v2/userinfo needed to
// decide whether inviting is possible. Same endpoint the organization data
// source already calls; no new request shape and nothing on ext/v0.
type organizationSSOInfoResponse struct {
	Result struct {
		Organizations []struct {
			SSOMode string `json:"sso_mode"`
		} `json:"organizations"`
	} `json:"result"`
}

// organizationRequiresSSO reports whether the authenticated token's
// organization has sso_mode = required.
//
// Returns false on any failure to determine it. That direction is deliberate:
// this guard exists to prevent a wasted email, and failing to read sso_mode is
// not evidence the org requires SSO. Refusing to invite because a preflight
// call failed would turn a transient error into a blocked apply, which is worse
// than the thing being guarded against.
func organizationRequiresSSO(ctx context.Context, client *Client) bool {
	info, err := DoRequestAndParse[organizationSSOInfoResponse](
		ctx, client, "GET", "/api/v2/userinfo", nil, http.StatusOK,
	)
	if err != nil || len(info.Result.Organizations) == 0 {
		return false
	}
	return strings.EqualFold(info.Result.Organizations[0].SSOMode, ssoModeRequired)
}

const ssoRequiredInviteDetail = "This organization requires single sign-on, so an invitation cannot be used: the " +
	"Anyscale backend accepts the invitation request and sends an email, but the link in it is rejected on use " +
	"(\"Cannot directly register a user with anyscale if single sign on is required for their organization\"). " +
	"Sending one would deliver a dead link AND consume one of the organization's 20 invitations per 24 hours.\n\n" +
	"In an SSO-required organization people gain access by logging in through your identity provider. Once they " +
	"have done so, bring them under Terraform management with `terraform import`, or declare them here - this " +
	"resource adopts an existing member without sending anything.\n\n" +
	"Organizations with SSO set to optional are unaffected and can be invited normally."

// createOrganizationInvitation sends an invitation, refusing first if the
// organization requires SSO.
//
// REFUSE RATHER THAN DEGRADE. Creation is not SSO-gated backend-side - there is
// no sso_mode check anywhere in org_invites_service.py, nor on the router - so
// the POST succeeds, an email goes out, and the link cannot work. Half-working
// is worse than refusing here because it also spends a rate-limited resource to
// achieve nothing, and reports success while doing it.
func createOrganizationInvitation(ctx context.Context, client *Client, email string) (*OrganizationInvitationResult, error) {
	if organizationRequiresSSO(ctx, client) {
		return nil, fmt.Errorf("%s", ssoRequiredInviteDetail)
	}

	reqBody, err := MarshalRequestBody(CreateOrganizationInvitationRequest{Email: email})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal invitation request: %w", err)
	}

	resp, err := DoRequestAndParse[OrganizationInvitationResponse](
		ctx, client, "POST", "/api/v2/organization_invitations", reqBody,
		http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}
