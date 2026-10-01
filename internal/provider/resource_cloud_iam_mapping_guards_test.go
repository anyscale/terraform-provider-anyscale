package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// iamGuardBackend serves one cloud with the given resources. The deployment
// config route answers GET with configStatus (200 returns a spec) and counts
// PUTs. The backend answers 500, not 404, for a deployment that was deleted
// while its cloud still exists: its "does not exist" error is raised inside a
// handler that turns every non-RPC failure into a 500.
type iamGuardBackend struct {
	server       *httptest.Server
	puts         atomic.Int32
	configStatus atomic.Int32
}

func newIAMGuardBackend(t *testing.T, cloudID string, resourceIDs ...string) *iamGuardBackend {
	t.Helper()
	b := &iamGuardBackend{}
	b.configStatus.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		items := make([]string, 0, len(resourceIDs))
		for i, id := range resourceIDs {
			items = append(items, fmt.Sprintf(`{"name": "res-%d", "is_default": %t, "cloud_resource_id": %q}`, i, i == 0, id))
		}
		_, _ = fmt.Fprintf(w, `{"results": [%s], "metadata": {"total": %d, "next_paging_token": null}}`, strings.Join(items, ","), len(items))
	})
	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/deployment/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			status := int(b.configStatus.Load())
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = fmt.Fprint(w, `{"result": {"spec": {"cloud_provider": "AWS", "compute_stack": "VM", "dataplane_iam_mapping": {"mode": "CUSTOMER_MANAGED", "rules": [{"selector": "workload-type=job", "value": "role-a"}], "fallback_rule": "CLOUD_DEFAULT"}}}}`)
			} else {
				_, _ = fmt.Fprint(w, `{"error": {"detail": "Cloud resource does not exist"}}`)
			}
		case http.MethodPut:
			b.puts.Add(1)
			_, _ = fmt.Fprint(w, `{"result": {"spec": {"cloud_provider": "AWS", "compute_stack": "VM", "dataplane_iam_mapping": {"mode": "CUSTOMER_MANAGED"}}}}`)
		default:
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
		}
	})
	b.server = httptest.NewServer(mux)
	t.Cleanup(b.server.Close)
	return b
}

func (b *iamGuardBackend) resource() *CloudIAMMappingResource {
	return &CloudIAMMappingResource{client: NewClientWithToken(b.server.URL, "test-token")}
}

func iamGuardModel(t *testing.T, cloudID, cloudResourceID string) CloudIAMMappingResourceModel {
	t.Helper()
	return CloudIAMMappingResourceModel{
		ID:              types.StringValue(cloudID + "/" + cloudResourceID),
		CloudID:         types.StringValue(cloudID),
		CloudResourceID: types.StringValue(cloudResourceID),
		Rules:           cloudIAMMappingRulesFixture(t, [2]string{"workload-type=job", "role-a"}),
		FallbackRule:    types.StringValue("CLOUD_DEFAULT"),
		Mode:            types.StringValue("CUSTOMER_MANAGED"),
		Timeouts:        nullCloudIAMMappingTimeouts(),
	}
}

// TestCloudIAMMappingCreate_RejectsCloudResourceOfAnotherCloud proves an
// explicit cloud_resource_id that is not one of cloud_id's resources is
// refused before any write. The backend authorizes on cloud_id alone and loads
// the deployment by its own ID, so without this guard the write replaces the
// other cloud's IAM mapping and reports success.
func TestCloudIAMMappingCreate_RejectsCloudResourceOfAnotherCloud(t *testing.T) {
	const cloudID = "cld_guard_a"

	t.Run("foreign id is refused and nothing is written", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, "cldrsrc_guard_own")
		r := b.resource()
		plan := iamGuardModel(t, cloudID, "cldrsrc_other_cloud")

		tfPlan := buildCloudIAMMappingPlan(t, r, plan)
		resp := &resource.CreateResponse{State: tfsdk.State(tfPlan)}
		r.Create(context.Background(), resource.CreateRequest{Plan: tfPlan}, resp)

		if !diagsContainSummary(resp.Diagnostics, "Invalid cloud_resource_id") {
			t.Fatalf("want an Invalid cloud_resource_id error, got: %v", resp.Diagnostics)
		}
		for _, want := range []string{cloudID, "cldrsrc_other_cloud"} {
			if !strings.Contains(fmt.Sprint(resp.Diagnostics), want) {
				t.Errorf("the error should name %q, got: %v", want, resp.Diagnostics)
			}
		}
		if n := b.puts.Load(); n != 0 {
			t.Fatalf("the config PUT was sent %d time(s) for a resource of another cloud", n)
		}
	})

	t.Run("control: the cloud's own resource is written", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, "cldrsrc_guard_own")
		r := b.resource()
		plan := iamGuardModel(t, cloudID, "cldrsrc_guard_own")

		tfPlan := buildCloudIAMMappingPlan(t, r, plan)
		resp := &resource.CreateResponse{State: tfsdk.State(tfPlan)}
		r.Create(context.Background(), resource.CreateRequest{Plan: tfPlan}, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
		}
		if n := b.puts.Load(); n != 1 {
			t.Fatalf("PUT count = %d, want 1", n)
		}
	})
}

// TestCloudIAMMappingImportState_RejectsCloudResourceOfAnotherCloud is the
// import-side twin: importing "<cloud_id>/<foreign id>" would otherwise read,
// and later manage, another cloud's mapping under this cloud's address.
func TestCloudIAMMappingImportState_RejectsCloudResourceOfAnotherCloud(t *testing.T) {
	const cloudID = "cld_guard_import"
	importState := func(t *testing.T, r *CloudIAMMappingResource, id string) *resource.ImportStateResponse {
		t.Helper()
		resp := newImportStateResponse(t, r)
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		return resp
	}

	b := newIAMGuardBackend(t, cloudID, "cldrsrc_guard_own")
	r := b.resource()

	if resp := importState(t, r, cloudID+"/cldrsrc_other_cloud"); !diagsContainSummary(resp.Diagnostics, "Invalid cloud_resource_id") {
		t.Fatalf("want an Invalid cloud_resource_id error for a foreign id, got: %v", resp.Diagnostics)
	}
	// Control: the cloud's own resource imports.
	if resp := importState(t, r, cloudID+"/cldrsrc_guard_own"); resp.Diagnostics.HasError() {
		t.Fatalf("a resource of the cloud must import, got: %v", resp.Diagnostics)
	}
}

// TestCloudIAMMapping_DeploymentDeletedOutOfBand proves a deployment that no
// longer exists, reported by the backend as a 500, is treated as gone by Read
// and Delete instead of wedging every plan and destroy. The controls keep the
// 500 an error whenever the deployment is still listed, so a real backend
// failure is not mistaken for deletion.
func TestCloudIAMMapping_DeploymentDeletedOutOfBand(t *testing.T) {
	const cloudID = "cld_guard_gone"
	const cloudResourceID = "cldrsrc_guard_gone"

	read := func(t *testing.T, b *iamGuardBackend) (*resource.ReadResponse, tfsdk.State) {
		t.Helper()
		r := b.resource()
		state := buildCloudIAMMappingState(t, r, iamGuardModel(t, cloudID, cloudResourceID))
		resp := &resource.ReadResponse{State: state}
		r.Read(context.Background(), resource.ReadRequest{State: state}, resp)
		return resp, resp.State
	}

	t.Run("read: 500 and absent from the cloud removes the resource", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, "cldrsrc_guard_other")
		b.configStatus.Store(http.StatusInternalServerError)
		resp, state := read(t, b)
		if resp.Diagnostics.HasError() {
			t.Fatalf("a deleted deployment must not be an error: %v", resp.Diagnostics)
		}
		if !state.Raw.IsNull() {
			t.Fatal("the resource should be removed from state")
		}
	})

	t.Run("read control: 404 removes the resource", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, "cldrsrc_guard_other")
		b.configStatus.Store(http.StatusNotFound)
		resp, state := read(t, b)
		if resp.Diagnostics.HasError() || !state.Raw.IsNull() {
			t.Fatalf("404 must remove the resource without error; diags=%v removed=%v", resp.Diagnostics, state.Raw.IsNull())
		}
	})

	t.Run("read control: 500 while still listed stays an error", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, cloudResourceID)
		b.configStatus.Store(http.StatusInternalServerError)
		resp, state := read(t, b)
		if !resp.Diagnostics.HasError() {
			t.Fatal("a 500 for a deployment that still exists must be reported")
		}
		if state.Raw.IsNull() {
			t.Fatal("state must be kept when the deployment still exists")
		}
	})

	t.Run("delete: 500 and absent from the cloud succeeds without a write", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, "cldrsrc_guard_other")
		b.configStatus.Store(http.StatusInternalServerError)
		r := b.resource()
		state := buildCloudIAMMappingState(t, r, iamGuardModel(t, cloudID, cloudResourceID))
		resp := &resource.DeleteResponse{}
		r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("destroy of a deleted deployment must succeed: %v", resp.Diagnostics)
		}
		if n := b.puts.Load(); n != 0 {
			t.Fatalf("nothing should be written for a deployment that is gone, got %d PUT(s)", n)
		}
	})

	t.Run("delete control: 500 while still listed stays an error", func(t *testing.T) {
		b := newIAMGuardBackend(t, cloudID, cloudResourceID)
		b.configStatus.Store(http.StatusInternalServerError)
		r := b.resource()
		state := buildCloudIAMMappingState(t, r, iamGuardModel(t, cloudID, cloudResourceID))
		resp := &resource.DeleteResponse{}
		r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("a 500 for a deployment that still exists must fail the destroy")
		}
	})
}
