package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// mockTerminateServer serves the two endpoints Invoke calls: terminate and describe. describe's
// status advances from Terminating to Terminated after terminateAfterPolls describe calls, so
// tests can control exactly how many polls occur before the wait loop's target condition is met.
type mockTerminateServer struct {
	terminateStatus     int // HTTP status the terminate endpoint returns
	terminateCalled     int32
	describeCallCount   int32
	terminateAfterPolls int32 // describe reports Terminated once describeCallCount reaches this
	stuckStatus         string
}

func newMockTerminateServer(t *testing.T, cloudID string, s *mockTerminateServer) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/system_workload/"+cloudID+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.terminateCalled, 1)
		w.WriteHeader(s.terminateStatus)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"detail": "mock detail"}})
	})

	mux.HandleFunc("/api/v2/system_workload/"+cloudID+"/describe", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&s.describeCallCount, 1)

		status := s.stuckStatus
		if status == "" {
			status = "Terminating"
		}
		if s.terminateAfterPolls > 0 && n >= s.terminateAfterPolls {
			status = systemClusterStateTerminated
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"result": {"cluster_id": %[1]q, "workload_service_url": null, "workload_service_url_auth": null, "status": %[2]q, "is_enabled": true}}`, cloudID, status)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func newTerminateAction(t *testing.T, serverURL string) *SystemClusterTerminateAction {
	t.Helper()
	a := &SystemClusterTerminateAction{}
	configResp := &action.ConfigureResponse{}
	a.Configure(context.Background(), action.ConfigureRequest{
		ProviderData: NewClientWithToken(serverURL, "test-token"),
	}, configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("Configure returned diagnostics: %s", configResp.Diagnostics)
	}
	return a
}

// invokeTerminate builds a real InvokeRequest/Response pair (mirroring the framework's own RPC
// plumbing that populates these before calling an ephemeral resource's Open or an action's Invoke)
// and calls Invoke directly. This is not a workaround here - per this repo's Design Verification
// Policy trace, terraform-plugin-testing v1.16.0 has no acctest tooling for action{} blocks at
// all, so calling Invoke directly against a mocked client is the only way to test an Action's
// real logic, not a supplement to a fuller test.
func invokeTerminate(t *testing.T, a *SystemClusterTerminateAction, cloudID string) (*action.InvokeResponse, []string) {
	t.Helper()

	schemaResp := &action.SchemaResponse{}
	a.Schema(context.Background(), action.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema() returned diagnostics: %s", schemaResp.Diagnostics)
	}

	configObj := types.ObjectValueMust(
		map[string]attr.Type{"cloud_id": types.StringType},
		map[string]attr.Value{"cloud_id": types.StringValue(cloudID)},
	)
	rawVal, err := configObj.ToTerraformValue(context.Background())
	if err != nil {
		t.Fatalf("failed to build InvokeRequest.Config.Raw: %s", err)
	}

	var progress []string
	resp := &action.InvokeResponse{
		SendProgress: func(event action.InvokeProgressEvent) {
			progress = append(progress, event.Message)
		},
	}
	a.Invoke(context.Background(), action.InvokeRequest{
		Config: tfsdk.Config{Raw: rawVal, Schema: schemaResp.Schema},
	}, resp)

	return resp, progress
}

func TestSystemClusterTerminateAction_Invoke(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		cloudID := "cld_term_ok"
		s := &mockTerminateServer{terminateStatus: http.StatusAccepted, terminateAfterPolls: 2}
		server := newMockTerminateServer(t, cloudID, s)
		a := newTerminateAction(t, server.URL)

		// Drive the wait loop directly with tiny timing so the test doesn't pay real wall-clock
		// time (same pattern system_workload_helpers_test.go uses for waitForSystemClusterStateWithTiming).
		resp := &action.InvokeResponse{SendProgress: func(action.InvokeProgressEvent) {}}
		var config SystemClusterTerminateActionModel
		config.CloudID = types.StringValue(cloudID)

		if err := terminateSystemCluster(context.Background(), a.client, cloudID); err != nil {
			t.Fatalf("terminateSystemCluster: %v", err)
		}
		a.waitForTerminatedWithTiming(context.Background(), resp, cloudID, 200*time.Millisecond, 5*time.Millisecond)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error diagnostics: %s", resp.Diagnostics.Errors())
		}
		if len(resp.Diagnostics.Warnings()) != 0 {
			t.Errorf("unexpected warnings on clean success: %s", resp.Diagnostics.Warnings())
		}
		if atomic.LoadInt32(&s.terminateCalled) != 1 {
			t.Errorf("terminateCalled = %d, want 1", s.terminateCalled)
		}
	})

	t.Run("FullInvoke_ReportsProgress", func(t *testing.T) {
		// Exercises the real Invoke entrypoint end to end (not just the wait-loop helper above),
		// confirming SendProgress fires and terminate is called exactly once - but against the
		// default 20-minute timeout path, so this only works because terminateAfterPolls=1 means
		// the very first describe call already reports Terminated; it does not exercise the
		// timeout branch (see TimeoutStillWarns below for that, driven via the timing helper
		// directly rather than Invoke, to avoid a real 20-minute wait).
		cloudID := "cld_term_full"
		s := &mockTerminateServer{terminateStatus: http.StatusAccepted, terminateAfterPolls: 1}
		server := newMockTerminateServer(t, cloudID, s)
		a := newTerminateAction(t, server.URL)

		resp, progress := invokeTerminate(t, a, cloudID)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error diagnostics: %s", resp.Diagnostics.Errors())
		}
		if atomic.LoadInt32(&s.terminateCalled) != 1 {
			t.Errorf("terminateCalled = %d, want 1", s.terminateCalled)
		}
		if len(progress) == 0 {
			t.Error("expected at least one SendProgress call, got none")
		}
	})

	t.Run("NoClusterExists404", func(t *testing.T) {
		cloudID := "cld_term_404"
		s := &mockTerminateServer{terminateStatus: http.StatusNotFound}
		server := newMockTerminateServer(t, cloudID, s)
		a := newTerminateAction(t, server.URL)

		resp, _ := invokeTerminate(t, a, cloudID)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error diagnostic for a 404, got none")
		}
		if got := resp.Diagnostics.Errors()[0].Summary(); got != "No System Cluster Exists" {
			t.Errorf("error summary = %q, want %q", got, "No System Cluster Exists")
		}
		if atomic.LoadInt32(&s.describeCallCount) != 0 {
			t.Errorf("describe should never be called when terminate itself 404s, got %d calls", s.describeCallCount)
		}
	})

	t.Run("AlreadyTerminated409", func(t *testing.T) {
		cloudID := "cld_term_409"
		s := &mockTerminateServer{terminateStatus: http.StatusConflict}
		server := newMockTerminateServer(t, cloudID, s)
		a := newTerminateAction(t, server.URL)

		resp, _ := invokeTerminate(t, a, cloudID)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error diagnostic for a 409, got none")
		}
		if got := resp.Diagnostics.Errors()[0].Summary(); got != "System Cluster Already Terminated" {
			t.Errorf("error summary = %q, want %q", got, "System Cluster Already Terminated")
		}
	})

	t.Run("TimeoutStillWarns", func(t *testing.T) {
		// terminateAfterPolls=0 means describe reports "Terminating" forever - the wait loop must
		// time out and emit a WARNING, not an error, since the terminate call itself succeeded
		// and the cluster may simply still be in progress.
		cloudID := "cld_term_timeout"
		s := &mockTerminateServer{terminateStatus: http.StatusAccepted, terminateAfterPolls: 0}
		server := newMockTerminateServer(t, cloudID, s)
		a := newTerminateAction(t, server.URL)

		if err := terminateSystemCluster(context.Background(), a.client, cloudID); err != nil {
			t.Fatalf("terminateSystemCluster: %v", err)
		}

		resp := &action.InvokeResponse{SendProgress: func(action.InvokeProgressEvent) {}}
		a.waitForTerminatedWithTiming(context.Background(), resp, cloudID, 17*time.Millisecond, 5*time.Millisecond)

		if resp.Diagnostics.HasError() {
			t.Fatalf("expected no error diagnostics on timeout, got: %s", resp.Diagnostics.Errors())
		}
		warnings := resp.Diagnostics.Warnings()
		if len(warnings) != 1 {
			t.Fatalf("got %d warning(s), want exactly 1: %v", len(warnings), warnings)
		}
		if got := warnings[0].Summary(); got != "Termination Not Yet Confirmed" {
			t.Errorf("warning summary = %q, want %q", got, "Termination Not Yet Confirmed")
		}
	})

	t.Run("ContextCancelled_StillWarns", func(t *testing.T) {
		cloudID := "cld_term_cancel"
		s := &mockTerminateServer{terminateStatus: http.StatusAccepted, terminateAfterPolls: 0}
		server := newMockTerminateServer(t, cloudID, s)
		a := newTerminateAction(t, server.URL)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()

		resp := &action.InvokeResponse{SendProgress: func(action.InvokeProgressEvent) {}}
		a.waitForTerminatedWithTiming(ctx, resp, cloudID, 5*time.Second, 2*time.Second)

		if resp.Diagnostics.HasError() {
			t.Fatalf("expected no error diagnostics on context cancellation, got: %s", resp.Diagnostics.Errors())
		}
		if len(resp.Diagnostics.Warnings()) != 1 {
			t.Fatalf("got %d warning(s), want exactly 1", len(resp.Diagnostics.Warnings()))
		}
	})
}
