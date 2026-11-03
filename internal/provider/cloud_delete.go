package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// defaultCloudDeleteTimeout bounds anyscale_cloud's Delete when timeouts.delete
// is unset: load balancer teardown plus the DELETE, which the CLI warns can
// take several minutes.
const defaultCloudDeleteTimeout = 30 * time.Minute

// cloudLBPollInterval is how often Delete asks whether the cloud's service
// load balancers are gone. A variable so tests can shorten it.
var cloudLBPollInterval = 10 * time.Second

// cloudLBResourceResponse is POST /api/v2/clouds/{id}/get_lb_resource.
type cloudLBResourceResponse struct {
	Result struct {
		IsTerminated bool `json:"is_terminated"`
	} `json:"result"`
}

// errCloudLBTeardownTimeout reports that the cloud's load balancers were still
// present when the delete deadline passed.
var errCloudLBTeardownTimeout = errors.New("the cloud's service load balancers were not torn down in time")

// cloudNeedsLBTeardown reports whether Delete must tear down the cloud's
// service load balancers first, as the CLI's delete_cloud does. Anyscale
// manages those load balancers only for an AWS or GCP VM cloud. A cloud with
// no resource has none, and the teardown endpoint fails for it.
func cloudNeedsLBTeardown(resources []CloudDeploymentResult) bool {
	if len(resources) == 0 {
		return false
	}
	primary := findDefaultInCloudResources(resources)
	if primary == nil {
		primary = &resources[0]
	}
	switch strings.ToUpper(primary.ComputeStack) {
	case "K8S", "KUBERAY":
		return false
	}
	switch strings.ToUpper(primary.Provider) {
	case "AWS", "GCP":
		return true
	}
	return false
}

// tearDownCloudLoadBalancers removes the cloud's service load balancers before
// the cloud is deleted. PUT /clouds/{id}/state?state=DELETING runs the
// backend's load balancer teardown, with the cloud's own role, and marks the
// cloud DELETING; POST get_lb_resource then reports when they are gone.
// Deleting the cloud without this leaves the load balancer, its target groups
// and its certificate behind in the customer's account.
//
// DELETING cannot be undone: if the DELETE that follows fails, the cloud stays
// DELETING. ran reports whether the state change was made, for that message.
func tearDownCloudLoadBalancers(ctx context.Context, client *Client, cloudID string) (ran bool, err error) {
	resources, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		if isGoneOrForbidden(err) {
			// Already gone, or no longer readable: the DELETE decides.
			return false, nil
		}
		return false, err
	}
	if !cloudNeedsLBTeardown(resources) {
		return false, nil
	}

	tflog.Info(ctx, "Tearing down the cloud's service load balancers", map[string]any{"cloud_id": cloudID})
	if _, err := DoRequestRaw(ctx, client, "PUT",
		fmt.Sprintf("/api/v2/clouds/%s/state?state=DELETING", cloudID), nil,
		http.StatusNoContent, http.StatusOK); err != nil {
		if isGoneOrForbidden(err) {
			// The cloud went away since the resource listing: the DELETE decides.
			return false, nil
		}
		return false, fmt.Errorf("failed to start the load balancer teardown: %w", err)
	}

	path := fmt.Sprintf("/api/v2/clouds/%s/get_lb_resource", cloudID)
	for {
		lb, err := DoRequestAndParse[cloudLBResourceResponse](ctx, client, "POST", path, nil, http.StatusOK)
		if err != nil {
			if ctx.Err() != nil {
				return true, errCloudLBTeardownTimeout
			}
			return true, fmt.Errorf("failed to check the load balancer teardown: %w", err)
		}
		if lb.Result.IsTerminated {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, errCloudLBTeardownTimeout
		case <-time.After(cloudLBPollInterval):
		}
	}
}

// addCloudDeleteError reports a failed cloud DELETE. After the load balancer
// teardown, the cloud is DELETING for good, so the message says what that
// leaves and how to finish.
func addCloudDeleteError(diags *diag.Diagnostics, cloudID string, tornDown bool, err error) {
	AddAPIError(diags, "delete cloud "+cloudID, err)
	if !tornDown {
		return
	}
	extra := "The cloud's service load balancers were already removed and the cloud is marked DELETING, which cannot be undone."
	if isHTTPStatus(err, http.StatusConflict) {
		extra += " Terminate the clusters named above, then run terraform destroy again."
	} else {
		extra += " Run terraform destroy again to finish deleting it."
	}
	diags.AddError("Cloud Left In DELETING State", extra)
}
