package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// cloudDeleteMock serves the requests anyscale_cloud's Delete makes and
// records them in order as "METHOD path".
type cloudDeleteMock struct {
	mu         sync.Mutex
	calls      []string
	resources  string // JSON array for GET .../resources
	resStatus  int    // GET .../resources status; 0 = 200
	lbPending  int    // get_lb_resource answers is_terminated=false this many times
	lbNever    bool   // get_lb_resource never answers true
	delStatus  int    // DELETE status; 0 = 204
	delBody    string
	delHang    bool // DELETE never answers before the client gives up
	getStatus  int  // GET of the cloud itself; 0 = 404
	putStatus  int  // PUT state status; 0 = 204
	lbStatus   int  // get_lb_resource status; 0 = 200
	lbFailOnce int  // the first get_lb_resource call answers this status
	lbDropOnce bool // the first get_lb_resource call drops the connection

	removeStatus int    // DELETE .../remove_resource status; 0 = 204
	removeBody   string // DELETE .../remove_resource body when removeStatus is set
}

func (m *cloudDeleteMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	const cloud = "/api/v2/clouds/cld_del"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.calls = append(m.calls, r.Method+" "+r.URL.Path)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/machine_pools/":
			_, _ = fmt.Fprint(w, `{"result": {"machine_pools": []}}`)
		case r.Method == http.MethodGet && r.URL.Path == cloud+"/resources":
			if m.resStatus != 0 {
				w.WriteHeader(m.resStatus)
				_, _ = fmt.Fprint(w, `{"error": {"detail": "Cloud resources for Anyscale-hosted clouds can not be fetched."}}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"results": %s, "metadata": {"next_paging_token": null}}`, m.resources)
		case r.Method == http.MethodPut && r.URL.Path == cloud+"/state":
			if got := r.URL.Query().Get("state"); got != "DELETING" {
				t.Errorf("PUT state = %q, want DELETING", got)
			}
			if m.putStatus != 0 {
				w.WriteHeader(m.putStatus)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == cloud+"/get_lb_resource":
			if m.lbDropOnce {
				m.mu.Lock()
				m.lbDropOnce = false
				m.mu.Unlock()
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			if m.lbFailOnce != 0 {
				m.mu.Lock()
				status := m.lbFailOnce
				m.lbFailOnce = 0
				m.mu.Unlock()
				w.WriteHeader(status)
				return
			}
			if m.lbStatus != 0 {
				w.WriteHeader(m.lbStatus)
				_, _ = fmt.Fprint(w, `{"error": {"detail": "lb lookup failed"}}`)
				return
			}
			m.mu.Lock()
			done := !m.lbNever && m.lbPending <= 0
			m.lbPending--
			m.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"result": {"is_terminated": %t}}`, done)
		case r.Method == http.MethodDelete && r.URL.Path == cloud+"/remove_resource":
			if m.removeStatus != 0 {
				w.WriteHeader(m.removeStatus)
				_, _ = fmt.Fprint(w, m.removeBody)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == cloud:
			status := m.getStatus
			if status == 0 {
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"result": {"id": "cld_del"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == cloud:
			if m.delHang {
				<-r.Context().Done()
				return
			}
			if m.delStatus != 0 {
				w.WriteHeader(m.delStatus)
				_, _ = fmt.Fprint(w, m.delBody)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// teardownCalls returns the recorded calls other than the machine pool listing.
func (m *cloudDeleteMock) teardownCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.calls {
		if !strings.Contains(c, "machine_pools") {
			out = append(out, c)
		}
	}
	return out
}

func runCloudDelete(t *testing.T, m *cloudDeleteMock, deleteTimeout string) diag.Diagnostics {
	t.Helper()
	old := cloudLBPollInterval
	cloudLBPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { cloudLBPollInterval = old })

	attrs := map[string]any{"id": "cld_del"}
	if deleteTimeout != "" {
		attrs["timeouts"] = timeouts.Value{Object: types.ObjectValueMust(cloudTimeoutsAttrTypes(), map[string]attr.Value{
			"create": types.StringNull(),
			"delete": types.StringValue(deleteTimeout),
		})}
	}
	r := &CloudResource{client: NewClientWithToken(m.server(t).URL, "test-token")}
	return runForbiddenDelete(t, r, attrs)
}

const (
	awsVMResource  = `[{"cloud_resource_id": "cldrsrc_1", "name": "r", "provider": "AWS", "compute_stack": "VM", "is_default": true}]`
	gcpVMResource  = `[{"cloud_resource_id": "cldrsrc_1", "name": "r", "provider": "GCP", "compute_stack": "VM", "is_default": true}]`
	awsK8SResource = `[{"cloud_resource_id": "cldrsrc_1", "name": "r", "provider": "AWS", "compute_stack": "K8S", "is_default": true}]`
)

// TestCloudResourceDelete_TearsDownLoadBalancersFirst pins the order the CLI's
// delete_cloud uses: tear down the service load balancers (PUT state DELETING),
// wait until get_lb_resource reports them gone, then DELETE. Deleting first
// leaves the load balancer, target groups and certificate in the account.
func TestCloudResourceDelete_TearsDownLoadBalancersFirst(t *testing.T) {
	for name, res := range map[string]string{"AWS VM": awsVMResource, "GCP VM": gcpVMResource} {
		t.Run(name, func(t *testing.T) {
			m := &cloudDeleteMock{resources: res, lbPending: 2}
			if diags := runCloudDelete(t, m, ""); diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}
			want := []string{
				"GET /api/v2/clouds/cld_del/resources",
				"PUT /api/v2/clouds/cld_del/state",
				"POST /api/v2/clouds/cld_del/get_lb_resource",
				"POST /api/v2/clouds/cld_del/get_lb_resource",
				"POST /api/v2/clouds/cld_del/get_lb_resource",
				"DELETE /api/v2/clouds/cld_del",
			}
			if got := m.teardownCalls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// A cloud with no resource, or a Kubernetes cloud, has no Anyscale-managed load
// balancer: the teardown endpoint fails for the first and does nothing for the
// second, so neither gets a PUT.
func TestCloudResourceDelete_SkipsTeardownWithoutManagedLoadBalancers(t *testing.T) {
	for name, res := range map[string]string{"empty cloud": `[]`, "K8S cloud": awsK8SResource} {
		t.Run(name, func(t *testing.T) {
			m := &cloudDeleteMock{resources: res}
			if diags := runCloudDelete(t, m, ""); diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}
			want := []string{"GET /api/v2/clouds/cld_del/resources", "DELETE /api/v2/clouds/cld_del"}
			if got := m.teardownCalls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("calls = %v, want %v", got, want)
			}
		})
	}
}

// If the load balancers are not gone by the delete deadline, the cloud is not
// deleted, and the error says to raise timeouts.delete.
func TestCloudResourceDelete_LoadBalancerTeardownTimeoutSkipsDelete(t *testing.T) {
	m := &cloudDeleteMock{resources: awsVMResource, lbNever: true}
	diags := runCloudDelete(t, m, "300ms")
	if !diagsContainSummary(diags, "Cloud Load Balancer Teardown Failed") {
		t.Fatalf("want a teardown error, got: %v", diags)
	}
	d := diags[0].Detail()
	if !strings.Contains(d, "timeouts.delete = 300ms") || !strings.Contains(d, "marked DELETING") {
		t.Errorf("detail %q should name the timeout and the DELETING state", d)
	}
	// A longer timeout cannot help once the cloud's IAM role is gone, so the error must also
	// give the recovery that works then, not only "raise timeouts.delete".
	for _, want := range []string{
		"If that identity or its permissions were already removed, a longer timeout will not help.",
		"terraform state rm",
		"anyscale cloud delete --cloud-id cld_del",
		"raise timeouts.delete",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q does not contain %q", d, want)
		}
	}
	for _, c := range m.teardownCalls() {
		if strings.HasPrefix(c, "DELETE ") {
			t.Fatalf("the cloud was deleted although its load balancers were not torn down: %v", m.teardownCalls())
		}
	}
}

// A 409 from DELETE after the teardown leaves the cloud DELETING. The
// diagnostics carry the API's detail (which names the active clusters) and say
// how to finish.
func TestCloudResourceDelete_ConflictAfterTeardownExplainsDeletingState(t *testing.T) {
	m := &cloudDeleteMock{
		resources: awsVMResource,
		delStatus: http.StatusConflict,
		delBody:   `{"error": {"detail": "Cloud has active clusters: ses_abc"}}`,
	}
	diags := runCloudDelete(t, m, "")
	if !diagsContainSummary(diags, "Cloud Left In DELETING State") {
		t.Fatalf("want the DELETING-state error, got: %v", diags)
	}
	all := ""
	for _, d := range diags {
		all += d.Detail() + "\n"
	}
	for _, want := range []string{"(HTTP 409): Cloud has active clusters: ses_abc", "Terminate any clusters named above or still running in the cloud"} {
		if !strings.Contains(all, want) {
			t.Errorf("diagnostics %q missing %q", all, want)
		}
	}
}

// A prior version's timeouts block (create only) carries into the current
// shape with delete unset.
func TestUpgradeCloudTimeouts(t *testing.T) {
	prior := timeouts.Value{Object: types.ObjectValueMust(map[string]attr.Type{"create": types.StringType},
		map[string]attr.Value{"create": types.StringValue("20m")})}
	got := upgradeCloudTimeouts(prior)
	attrs := got.Attributes()
	if attrs["create"].(types.String).ValueString() != "20m" || !attrs["delete"].IsNull() {
		t.Errorf("upgraded timeouts = %v, want create=20m, delete=null", got)
	}
	if !upgradeCloudTimeouts(timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType})}).IsNull() {
		t.Error("a null prior block must stay null")
	}
}

// A DELETE that outlives timeouts.delete is re-checked with a GET: a cloud that
// is gone counts as deleted, one that still exists is an error.
func TestCloudResourceDelete_TimedOutDeleteIsReadBack(t *testing.T) {
	t.Run("gone after the timeout", func(t *testing.T) {
		m := &cloudDeleteMock{resources: `[]`, delHang: true, getStatus: http.StatusNotFound}
		if diags := runCloudDelete(t, m, "300ms"); diags.HasError() {
			t.Fatalf("want success for a cloud gone after the timeout, got: %v", diags)
		}
	})
	t.Run("still present after the timeout", func(t *testing.T) {
		m := &cloudDeleteMock{resources: `[]`, delHang: true, getStatus: http.StatusOK}
		diags := runCloudDelete(t, m, "300ms")
		if !diagsContainSummary(diags, "Cloud Delete Timed Out") {
			t.Fatalf("want a timeout error, got: %v", diags)
		}
		if d := diags[0].Detail(); !strings.Contains(d, "can still be read") {
			t.Errorf("detail %q should say the cloud still exists", d)
		}
	})
}

// A cloud that is gone by the time of the PUT (404) goes straight to DELETE,
// which treats a 404 as already deleted.
func TestCloudResourceDelete_GoneAtTeardownProceedsToDelete(t *testing.T) {
	m := &cloudDeleteMock{resources: awsVMResource, putStatus: http.StatusNotFound}
	if diags := runCloudDelete(t, m, ""); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	want := []string{"GET /api/v2/clouds/cld_del/resources", "PUT /api/v2/clouds/cld_del/state", "DELETE /api/v2/clouds/cld_del"}
	if got := m.teardownCalls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

// A get_lb_resource check that fails with a 4xx is not "torn down": the cloud
// is not deleted, since deleting it with its load balancer still up is the
// leak this prevents.
func TestCloudResourceDelete_FailedLoadBalancerCheckSkipsDelete(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		timeout string
		want    string
	}{
		"4xx fails at once":            {http.StatusBadRequest, "2s", "failed to check the load balancer teardown"},
		"persistent 5xx until timeout": {http.StatusInternalServerError, "300ms", "the last check failed: lb lookup failed"},
	} {
		t.Run(name, func(t *testing.T) {
			m := &cloudDeleteMock{resources: awsVMResource, lbStatus: tc.status}
			diags := runCloudDelete(t, m, tc.timeout)
			if !diagsContainSummary(diags, "Cloud Load Balancer Teardown Failed") {
				t.Fatalf("want a teardown error, got: %v", diags)
			}
			if d := diags[0].Detail(); !strings.Contains(d, tc.want) {
				t.Errorf("detail %q should contain %q", d, tc.want)
			}
			for _, c := range m.teardownCalls() {
				if strings.HasPrefix(c, "DELETE ") {
					t.Fatalf("the cloud was deleted although the teardown check failed: %v", m.teardownCalls())
				}
			}
		})
	}
}

// One transient failure mid-poll does not fail the destroy: the cloud is
// already DELETING, and the next check succeeds.
func TestCloudResourceDelete_TransientLoadBalancerCheckErrorIsRetried(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m := &cloudDeleteMock{resources: awsVMResource, lbPending: 1, lbFailOnce: status}
			if diags := runCloudDelete(t, m, ""); diags.HasError() {
				t.Fatalf("a single %d must not fail the destroy, got: %v", status, diags)
			}
			calls := m.teardownCalls()
			if last := calls[len(calls)-1]; last != "DELETE /api/v2/clouds/cld_del" {
				t.Errorf("last call = %q, want the DELETE; calls: %v", last, calls)
			}
		})
	}
}

// A dropped connection mid-poll is transient too.
func TestCloudResourceDelete_DroppedConnectionDuringCheckIsRetried(t *testing.T) {
	m := &cloudDeleteMock{resources: awsVMResource, lbPending: 1, lbDropOnce: true}
	if diags := runCloudDelete(t, m, ""); diags.HasError() {
		t.Fatalf("a dropped connection must not fail the destroy, got: %v", diags)
	}
}

// An Anyscale-hosted cloud's resource listing answers 400: it has no load
// balancer in the customer's account, so destroy goes straight to the DELETE.
// Any other listing failure still stops the destroy.
func TestCloudResourceDelete_HostedCloudSkipsTeardown(t *testing.T) {
	m := &cloudDeleteMock{resStatus: http.StatusBadRequest}
	if diags := runCloudDelete(t, m, ""); diags.HasError() {
		t.Fatalf("a hosted cloud must delete without a teardown, got: %v", diags)
	}
	want := []string{"GET /api/v2/clouds/cld_del/resources", "DELETE /api/v2/clouds/cld_del"}
	if got := m.teardownCalls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls = %v, want %v", got, want)
	}

	t.Run("control: another listing failure stops the destroy", func(t *testing.T) {
		m := &cloudDeleteMock{resStatus: http.StatusInternalServerError}
		diags := runCloudDelete(t, m, "")
		if !diagsContainSummary(diags, "Cloud Load Balancer Teardown Failed") {
			t.Fatalf("want a teardown error, got: %v", diags)
		}
		for _, c := range m.teardownCalls() {
			if strings.HasPrefix(c, "DELETE ") {
				t.Fatalf("deleted although the listing failed: %v", m.teardownCalls())
			}
		}
	})
}
