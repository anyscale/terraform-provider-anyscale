package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// anyscale_service's stopped argument. Design: docs/decisions/service-stopped/README.md.
//
// stopped = true terminates the service and keeps its record, id and hostname; false restarts it
// through PUT /apply under the same id (serviceRestartRequired). Read refreshes stopped from
// goal_state, so a service terminated by anyone plans stopped true -> false, an ordinary diff that
// restarts it.

// serviceStopRequired reports whether the plan stops a service that is not yet terminated.
func serviceStopRequired(plan, state *ServiceResourceModel) bool {
	return plan.Stopped.ValueBool() && !serviceIsTerminated(state.GoalState)
}

// serviceDeployFieldChanges lists every deploy field (serviceDeployFieldsChanged) the plan
// changes. A stopped service deploys nothing, so all of them are rejected while stopped is true.
func serviceDeployFieldChanges(plan, state *ServiceResourceModel) []string {
	var changed []string
	if !plan.RayServeConfig.Equal(state.RayServeConfig) {
		changed = append(changed, "ray_serve_config")
	}
	return append(changed, serviceInPlaceFrozenChanges(plan, state)...)
}

func addServiceCreateStoppedError(diags *diag.Diagnostics) {
	diags.AddAttributeError(path.Root("stopped"), "Service Cannot Be Created Stopped",
		"Anyscale starts a service when it is created, so stopped cannot be true on create. This includes a "+
			"replacement, such as a change to name or project_id. Create the service with stopped = false, then "+
			"set stopped = true.")
}

// addServiceStoppedFieldsError rejects a deploy-field change in a plan that stops the service or
// keeps it stopped: nothing would deploy it, and the next restart deploys whatever config is then
// set anyway.
func addServiceStoppedFieldsError(diags *diag.Diagnostics, changed []string, terminated bool) {
	action := "stops the service"
	if terminated {
		action = "keeps the service stopped"
	}
	diags.AddAttributeError(path.Root("stopped"), "Cannot Change a Stopped Service's Deployment",
		fmt.Sprintf("This plan %s, so it deploys nothing, but it changes: %s. Set stopped = false to deploy the change "+
			"(restarting the service if it is stopped), then set stopped = true; or revert the change.",
			action, strings.Join(changed, ", ")))
}

// stopService terminates the service and waits for TERMINATED, so a later restart is not refused
// with the 409 the backend returns while a service is still terminating. Deploy fields keep their
// state values (ModifyPlan rejects any change).
func (r *ServiceResource) stopService(ctx context.Context, plan *ServiceResourceModel, timeout time.Duration, resp *resource.UpdateResponse) {
	serviceID := plan.ID.ValueString()
	tflog.Info(ctx, "Stopping service", map[string]any{"service_id": serviceID})

	if _, err := DoRequestRaw(ctx, r.client, "POST", fmt.Sprintf("/api/v2/services-v2/%s/terminate", serviceID), nil,
		http.StatusAccepted); err != nil {
		AddAPIError(&resp.Diagnostics, "terminate service", err)
		return
	}

	service, err := waitForServiceState(ctx, r.client, serviceID, serviceStateTerminated, timeout)
	if service != nil {
		// The goal is TERMINATED from the terminate call on, so state records the service as
		// stopped even when the wait times out.
		resp.Diagnostics.Append(populateServiceResourceModelComputed(ctx, plan, service)...)
		refreshServiceTagsIntoModel(ctx, r.client, serviceID, &plan.Tags)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		AddAPIError(&resp.Diagnostics, "wait for service termination", err)
	}
}
