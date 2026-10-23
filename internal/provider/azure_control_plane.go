package provider

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// azureControlPlaneHosts are the Anyscale control planes that run on Azure. Access there is
// managed with Azure role-based access control in the Azure portal, so the provider refuses
// to manage Anyscale RBAC against them. Mirrors ANYSCALE_ENDPOINTS["azure"] in the Anyscale
// CLI (frontend/cli/anyscale/shared_anyscale_utils/conf.py): production, staging, predeploy.
var azureControlPlaneHosts = map[string]bool{
	"console.azure.anyscale.com":                   true,
	"console.azure.anyscale-staging.com":           true,
	"console.gcplane.azure.predeploy.anyscale.dev": true,
}

// azureControlPlaneHost returns the normalized host of apiURL if it is an Azure control
// plane, or "". It matches the parsed host exactly - lowercased, without a trailing dot or a
// default port - never a substring of the URL, so a path or query that mentions an Azure
// host does not match, and neither does a longer host that merely starts with one. A URL
// fronting Azure through a custom or proxied host is not detected.
func azureControlPlaneHost(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		// Includes a schemeless value such as "console.azure.anyscale.com", which parses as
		// a path. Configure does not normalize schemes, so such a value already fails every
		// request; it is not classified here either.
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if port := u.Port(); port != "" && port != "443" && port != "80" {
		return ""
	}
	if azureControlPlaneHosts[host] {
		return host
	}
	return ""
}

// azureRBACBlockedSummary is the summary of every Azure RBAC block error.
const azureRBACBlockedSummary = "RBAC Is Managed in Azure"

func azureRBACBlockedDetail(host, typeName string) string {
	return fmt.Sprintf("This provider targets an Azure control plane (%s). Access on Azure is managed with "+
		"Azure role-based access control in the Azure portal, not with %s.", host, typeName)
}

// refuseRBACOnAzure adds the Azure RBAC block error and reports true when client targets an
// Azure control plane. A nil client (provider not yet configured) is not blocked here.
func refuseRBACOnAzure(client *Client, typeName string, diags *diag.Diagnostics) bool {
	if client == nil || client.azureControlPlaneHost == "" {
		return false
	}
	diags.AddError(azureRBACBlockedSummary, azureRBACBlockedDetail(client.azureControlPlaneHost, typeName))
	return true
}

// refuseRBACPlanOnAzure blocks a create or an update of an RBAC resource on an Azure control
// plane, at plan time. Destroy (null plan) is allowed so stray state can always be removed,
// and a no-op plan on existing state is allowed so an upgrade does not break a plan that
// changes nothing. Call it first in ModifyPlan; it reports true when it added an error.
func refuseRBACPlanOnAzure(client *Client, typeName string, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) bool {
	if req.Plan.Raw.IsNull() {
		return false
	}
	if !req.State.Raw.IsNull() && req.Plan.Raw.Equal(req.State.Raw) {
		return false
	}
	return refuseRBACOnAzure(client, typeName, &resp.Diagnostics)
}
