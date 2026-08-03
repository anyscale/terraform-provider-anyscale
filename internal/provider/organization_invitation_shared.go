package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
// organization has sso_mode = required, and whether that could be determined
// at all.
//
// FAILS OPEN, BUT NOT SILENTLY. On any failure it reports "not required" so a
// transient blip does not become a blocked apply - failing to read sso_mode is
// not evidence the org requires SSO, and refusing on that basis is the worse
// trade. But the caller must SAY the check did not run: otherwise a user in an
// SSO-required org gets the dead-link email this guard exists to prevent, with
// nothing indicating why the guard was absent. An unrun check reported as a
// clean pass is the failure mode this whole session kept producing.
func organizationRequiresSSO(ctx context.Context, client *Client) (required bool, determined bool) {
	info, err := DoRequestAndParse[organizationSSOInfoResponse](
		ctx, client, "GET", "/api/v2/userinfo", nil, http.StatusOK,
	)
	if err != nil || len(info.Result.Organizations) == 0 {
		return false, false
	}
	return strings.EqualFold(info.Result.Organizations[0].SSOMode, ssoModeRequired), true
}

const ssoCheckSkippedDetail = "Could not read this organization's single sign-on mode, so the check that would " +
	"normally refuse to invite in an SSO-required organization did not run. The invitation was sent anyway - a " +
	"failed lookup is not evidence that SSO is required, and blocking the invitation on it would turn a " +
	"transient error into a failed apply.\n\n" +
	"If this organization DOES require single sign-on, the emailed link will not work and the invitation " +
	"consumed one of the organization's 20 per 24 hours. In that case have the person log in through your " +
	"identity provider instead, and import them once they have."

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
func createOrganizationInvitation(
	ctx context.Context, client *Client, email string, diags *diag.Diagnostics,
) (*OrganizationInvitationResult, error) {
	requiresSSO, determined := organizationRequiresSSO(ctx, client)
	switch {
	case requiresSSO:
		return nil, fmt.Errorf("%s", ssoRequiredInviteDetail)
	case !determined && diags != nil:
		// Fail open, loudly. See organizationRequiresSSO.
		diags.AddWarning("Single Sign-On Check Did Not Run", ssoCheckSkippedDetail)
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
