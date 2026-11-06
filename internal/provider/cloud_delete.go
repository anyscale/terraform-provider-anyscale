package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
// service load balancers first. Anyscale
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
// cloud DELETING; POST get_lb_resource then reports when they are gone. The
// CLI's delete_cloud does the same before its DELETE.
//
// DELETING cannot be undone. markedDeleting reports whether the state change
// was made, including when the wait for the teardown then fails, so a caller
// can say the cloud is left DELETING.
func tearDownCloudLoadBalancers(ctx context.Context, client *Client, cloudID string) (markedDeleting bool, err error) {
	resources, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		if isGoneOrForbidden(err) {
			// Already gone, or no longer readable: the DELETE decides.
			return false, nil
		}
		if isHostedCloudResourcesError(err) {
			// An Anyscale-hosted cloud has no resources in the customer's
			// account, so there is no load balancer of theirs to tear down.
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

	// get_lb_resource is a POST, so the client does not retry it. By now the
	// cloud is DELETING, so a single 5xx or dropped connection must not fail
	// the destroy: those keep polling until the deadline. Any other error
	// fails at once.
	path := fmt.Sprintf("/api/v2/clouds/%s/get_lb_resource", cloudID)
	var lastErr error
	for {
		lb, err := DoRequestAndParse[cloudLBResourceResponse](ctx, client, "POST", path, nil, http.StatusOK)
		switch {
		case err == nil && lb.Result.IsTerminated:
			return true, nil
		case err != nil && ctx.Err() != nil:
			return true, lbTeardownTimeout(lastErr)
		case err != nil && !transientLBCheckError(err):
			return true, fmt.Errorf("failed to check the load balancer teardown: %w", err)
		case err != nil:
			tflog.Warn(ctx, "Load balancer teardown check failed; retrying", map[string]any{"cloud_id": cloudID, "error": err.Error()})
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return true, lbTeardownTimeout(lastErr)
		case <-time.After(cloudLBPollInterval):
		}
	}
}

// transientLBCheckError reports a get_lb_resource failure worth polling
// through: a 5xx, a 429, or no response at all.
func transientLBCheckError(err error) bool {
	var statusErr *UnexpectedStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode >= 500 || statusErr.StatusCode == http.StatusTooManyRequests
	}
	// No response at all: the HTTP client's transport error.
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// lbTeardownTimeout is errCloudLBTeardownTimeout, with the last failed check
// when there was one.
func lbTeardownTimeout(lastErr error) error {
	if lastErr == nil {
		return errCloudLBTeardownTimeout
	}
	return fmt.Errorf("%w; the last check failed: %s", errCloudLBTeardownTimeout, extractAPIErrorDetail(lastErr))
}

// addCloudLBTeardownError reports a failed tearDownCloudLoadBalancers. prefix says what was not
// destroyed; stateRmTarget names what to remove from state in the role-gone recovery.
func addCloudLBTeardownError(diags *diag.Diagnostics, prefix, stateRmTarget, cloudID, cloudProvider string, deleteTimeout time.Duration, markedDeleting bool, err error) {
	detail := err.Error()
	if errors.Is(err, errCloudLBTeardownTimeout) {
		// The provider cannot tell a slow teardown from one that can never finish, so the
		// message covers both. Anyscale tears the load balancers down with the cloud's
		// control-plane identity; once it or its permissions are removed, get_lb_resource
		// never reports them gone, even after they are removed by hand.
		identity, leftovers := "IAM role or service account", "load balancer resources in your cloud account"
		switch cloudProvider {
		case "AWS":
			identity = "IAM role"
			leftovers = "load balancer (its name contains the cloud ID), target groups and ACM certificate in AWS"
		case "GCP":
			identity = "service account"
			leftovers = "forwarding rule, target HTTPS proxy, URL map, backend service, health check, and Certificate Manager certificate and map in your GCP project"
		}
		detail = fmt.Sprintf("%s (timeouts.delete = %s). Anyscale removes them with the cloud's control-plane %s. "+
			"If that identity or its permissions were already removed, a longer timeout will not help. "+
			"In that case, first delete the cloud's %s. "+
			"Then run terraform state rm on %s. Then delete the cloud with anyscale cloud delete --cloud-id %s, which runs the same check "+
			"for up to 15 minutes before offering to delete the cloud anyway. Otherwise, raise timeouts.delete and run terraform destroy again; "+
			"if the teardown still does not finish, follow the steps above and contact Anyscale support with the cloud ID. "+
			"See \"Deleting a cloud\" in the provider's Cloud Resources guide.", detail, deleteTimeout, identity, leftovers, stateRmTarget, cloudID)
	} else {
		detail += ". Run terraform destroy again once the error above is resolved."
	}
	if markedDeleting {
		detail += " The cloud is already marked DELETING, which cannot be undone."
	}
	diags.AddError("Cloud Load Balancer Teardown Failed", fmt.Sprintf("%s: %s", prefix, detail))
}

// addCloudDeleteError reports a DELETE that failed after a completed load
// balancer teardown (markedDeleting) or with none needed. After the teardown
// the cloud is DELETING for good, so the message says what that leaves and how
// to finish.
func addCloudDeleteError(diags *diag.Diagnostics, cloudID string, markedDeleting bool, err error) {
	AddAPIError(diags, "delete cloud "+cloudID, err)
	if !markedDeleting {
		return
	}
	extra := "The cloud's service load balancers were already removed and the cloud is marked DELETING, which cannot be undone."
	if isHTTPStatus(err, http.StatusConflict) {
		extra += " Terminate any clusters named above or still running in the cloud, then run terraform destroy again."
	} else {
		extra += " Run terraform destroy again to finish deleting it."
	}
	diags.AddError("Cloud Left In DELETING State", extra)
}
