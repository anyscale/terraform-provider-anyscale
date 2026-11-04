package provider

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// In the split pattern (an empty anyscale_cloud plus a primary
// anyscale_cloud_resource), the IAM role and VPC take the cloud's ID, so
// Terraform destroys them before the cloud. The primary cloud resource depends
// on their outputs and is destroyed first, while the role still exists, so its
// Delete is where the cloud's service load balancers must be torn down. A real
// AWS run showed that leaving the teardown to the cloud's own Delete strands
// the load balancer: the role is gone by then, get_lb_resource never turns
// true, and the ALB keeps the VPC from being deleted.

// runPrimaryTeardownDelete runs anyscale_cloud_resource's Delete for resource
// "r" of cloud cld_del against the mock. deleteTimeout, when set, goes in
// timeouts.delete.
func runPrimaryTeardownDelete(t *testing.T, m *cloudDeleteMock, isDefault bool, deleteTimeout string) diag.Diagnostics {
	t.Helper()
	old := cloudLBPollInterval
	cloudLBPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { cloudLBPollInterval = old })

	r := &CloudResourceResource{client: NewClientWithToken(m.server(t).URL, "test-token")}
	state := forbiddenStateFixture(t, r, map[string]any{
		"id":         "cld_del:r",
		"cloud_id":   "cld_del",
		"name":       "r",
		"is_default": isDefault,
	})
	if deleteTimeout != "" {
		if diags := state.SetAttribute(context.Background(), path.Root("timeouts").AtName("delete"), types.StringValue(deleteTimeout)); diags.HasError() {
			t.Fatalf("anyscale_cloud_resource has no timeouts.delete: %v", diags)
		}
	}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
	return resp.Diagnostics
}

func callsMatching(calls []string, substr string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// Acceptance 1: the primary resource's Delete marks the cloud DELETING and
// waits until get_lb_resource reports the load balancers gone. It does not
// remove the resource or delete the cloud: the cloud's own Delete does that.
func TestCloudResourcePrimaryDelete_TearsDownLoadBalancers(t *testing.T) {
	m := &cloudDeleteMock{resources: awsVMResource, lbPending: 2}
	if diags := runPrimaryTeardownDelete(t, m, true, ""); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	want := []string{
		"GET /api/v2/clouds/cld_del/resources",
		"PUT /api/v2/clouds/cld_del/state",
		"POST /api/v2/clouds/cld_del/get_lb_resource",
		"POST /api/v2/clouds/cld_del/get_lb_resource",
		"POST /api/v2/clouds/cld_del/get_lb_resource",
	}
	if got := m.teardownCalls(); !slices.Equal(got, want) {
		t.Errorf("calls = %v\nwant %v", got, want)
	}
}

// Same teardown for GCP VM, which also has Anyscale-managed load balancers.
func TestCloudResourcePrimaryDelete_GCPTearsDownLoadBalancers(t *testing.T) {
	m := &cloudDeleteMock{resources: gcpVMResource}
	if diags := runPrimaryTeardownDelete(t, m, true, ""); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if got := callsMatching(m.teardownCalls(), "PUT /api/v2/clouds/cld_del/state"); got != 1 {
		t.Errorf("PUT state calls = %d, want 1; calls: %v", got, m.teardownCalls())
	}
}

// A non-primary resource is removed with remove_resource only. Marking the
// cloud DELETING here would start deleting a cloud that is staying.
func TestCloudResourceNonPrimaryDelete_OnlyRemovesResource(t *testing.T) {
	m := &cloudDeleteMock{resources: awsVMResource}
	if diags := runPrimaryTeardownDelete(t, m, false, ""); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	want := []string{"DELETE /api/v2/clouds/cld_del/remove_resource"}
	if got := m.teardownCalls(); !slices.Equal(got, want) {
		t.Errorf("calls = %v\nwant %v", got, want)
	}
}

// is_default can be stale in state (destroy -refresh=false). The backend's
// primary refusal then identifies the primary, and the teardown still runs.
func TestCloudResourceStalePrimaryDelete_TearsDownAfterPrimaryRefusal(t *testing.T) {
	m := &cloudDeleteMock{
		resources:    awsVMResource,
		removeStatus: 400,
		removeBody:   `{"error": {"detail": "Cloud resource r is the primary resource and cannot be removed."}}`,
	}
	if diags := runPrimaryTeardownDelete(t, m, false, ""); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	calls := m.teardownCalls()
	if got := callsMatching(calls, "PUT /api/v2/clouds/cld_del/state"); got != 1 {
		t.Errorf("PUT state calls = %d, want 1 after the primary refusal; calls: %v", got, calls)
	}
	if got := callsMatching(calls, "DELETE /api/v2/clouds/cld_del "); got != 0 {
		t.Errorf("the cloud itself must not be deleted here; calls: %v", calls)
	}
}

// Acceptance 2: no Anyscale-managed load balancers on K8S, and a hosted
// cloud's resources cannot be listed (400). Neither may be marked DELETING.
func TestCloudResourcePrimaryDelete_SkipsTeardownWithoutManagedLoadBalancers(t *testing.T) {
	for name, m := range map[string]*cloudDeleteMock{
		"k8s":    {resources: awsK8SResource},
		"hosted": {resStatus: 400},
	} {
		t.Run(name, func(t *testing.T) {
			if diags := runPrimaryTeardownDelete(t, m, true, ""); diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}
			if got := callsMatching(m.teardownCalls(), "PUT "); got != 0 {
				t.Errorf("want no PUT state, got calls: %v", m.teardownCalls())
			}
		})
	}
}

// Acceptance 3: a teardown that does not finish within timeouts.delete fails
// the destroy (the resource stays in state), and a re-run repeats the PUT,
// which the backend accepts on a cloud that is already DELETING.
func TestCloudResourcePrimaryDelete_TeardownTimeoutFailsAndRerunRepeatsPut(t *testing.T) {
	m := &cloudDeleteMock{resources: awsVMResource, lbNever: true}
	diags := runPrimaryTeardownDelete(t, m, true, "300ms")
	if !diags.HasError() {
		t.Fatalf("want an error when the teardown times out, got: %v", diags)
	}
	if d := diags.Errors()[0].Detail(); !strings.Contains(d, "timeouts.delete = 300ms") {
		t.Errorf("detail %q should name timeouts.delete", d)
	}

	diags = runPrimaryTeardownDelete(t, m, true, "300ms")
	if !diags.HasError() {
		t.Fatalf("want the re-run to fail the same way, got: %v", diags)
	}
	if got := callsMatching(m.teardownCalls(), "PUT /api/v2/clouds/cld_del/state"); got != 2 {
		t.Errorf("PUT state calls = %d, want 2 (one per run); calls: %v", got, m.teardownCalls())
	}
}
