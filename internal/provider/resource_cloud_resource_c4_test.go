package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// C4 regression tests: operator_status_details was typed *string on
// CloudDeploymentResult, but the API has always returned an object
// (operator_version, check_results[], reported_at). A K8s cloud_resource
// whose operator had reported in failed to decode on Read at all - these
// tests use the real object shape, which would fail json.Unmarshal against
// the pre-fix *string type.

func TestReadCloudResource_K8sOperatorStatusDetailsDecodesWithoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{
				"name": "k8s-resource", "is_default": true, "compute_stack": "K8S", "region": "us-east-1",
				"operator_status": "HEALTHY",
				"operator_status_details": {
					"operator_version": "1.4.2",
					"check_results": [{"name": "connectivity", "status": "HEALTHY", "details": "ok"}],
					"reported_at": "2026-01-01T00:00:00Z"
				}
			}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	var state CloudResourceResourceModel

	if err := r.readCloudResource(context.Background(), "cloud-id", "k8s-resource", &state); err != nil {
		t.Fatalf("unexpected decode error (this is exactly the pre-fix failure mode): %v", err)
	}

	if state.Status.ValueString() != "HEALTHY" {
		t.Errorf("Status = %v, want HEALTHY", state.Status.ValueString())
	}
	if state.OperatorStatus.ValueString() != "HEALTHY" {
		t.Errorf("OperatorStatus = %v, want HEALTHY", state.OperatorStatus.ValueString())
	}
	if state.OperatorVersion.ValueString() != "1.4.2" {
		t.Errorf("OperatorVersion = %v, want 1.4.2", state.OperatorVersion.ValueString())
	}
	if state.ReportedAt.ValueString() != "2026-01-01T00:00:00Z" {
		t.Errorf("ReportedAt = %v, want 2026-01-01T00:00:00Z", state.ReportedAt.ValueString())
	}
}

func TestReadCloudResource_VMResourceHasNullOperatorFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{"name": "vm-resource", "is_default": true, "compute_stack": "VM", "region": "us-east-1"}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	var state CloudResourceResourceModel

	if err := r.readCloudResource(context.Background(), "cloud-id", "vm-resource", &state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.OperatorStatus.IsNull() {
		t.Errorf("OperatorStatus = %v, want null for a VM resource", state.OperatorStatus)
	}
	if !state.OperatorVersion.IsNull() {
		t.Errorf("OperatorVersion = %v, want null for a VM resource", state.OperatorVersion)
	}
	if !state.ReportedAt.IsNull() {
		t.Errorf("ReportedAt = %v, want null for a VM resource", state.ReportedAt)
	}
}

func TestReadCloudResource_K8sResourceNotYetReportedHasNullOperatorDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// A K8s resource that exists but whose operator hasn't reported in
		// yet: operator_status_details is absent, operator_status may still
		// be present (e.g. UNSPECIFIED) or absent too.
		_, _ = fmt.Fprint(w, `{
			"results": [{"name": "k8s-pending", "is_default": true, "compute_stack": "K8S", "region": "us-east-1", "operator_status": "UNSPECIFIED"}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	var state CloudResourceResourceModel

	if err := r.readCloudResource(context.Background(), "cloud-id", "k8s-pending", &state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if state.OperatorStatus.ValueString() != "UNSPECIFIED" {
		t.Errorf("OperatorStatus = %v, want UNSPECIFIED", state.OperatorStatus.ValueString())
	}
	if !state.OperatorVersion.IsNull() {
		t.Errorf("OperatorVersion = %v, want null - operator hasn't reported yet", state.OperatorVersion)
	}
	if !state.ReportedAt.IsNull() {
		t.Errorf("ReportedAt = %v, want null - operator hasn't reported yet", state.ReportedAt)
	}
}
