package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// This file covers C3 Phase 1's hard acceptance gates directly against
// readCloudState (anyscale_cloud) and readCloudResource (anyscale_cloud_resource):
// upgrade safety (a populated block is never touched), non-destructive import
// (a null block gets populated), the empty-cloud corruption trap, and
// credentials never round-tripping into state.

// buildAWSConfigState is a small builder for a fully-populated aws_config
// Object, standing in for "what Create() would have written into state".
func buildAWSConfigState(t *testing.T, vpcID string) types.Object {
	t.Helper()
	ctx := context.Background()
	obj, diags := flattenAWSConfig(ctx, &AWSConfig{VPCID: vpcID, SubnetIDs: []string{"subnet-orig"}, Zones: []string{"us-east-2a"}})
	if diags.HasError() {
		t.Fatalf("failed to build test aws_config: %v", diags)
	}
	return obj
}

func TestReadCloudState_AlreadyPopulatedConfigBlockIsNeverTouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-1":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-1", "name": "prod", "provider": "AWS", "region": "us-east-1"}}`)
		case "/api/v2/clouds/cloud-1/resources":
			// Deliberately DIFFERENT from what's already in state - if Phase 1
			// re-derived a populated block, this VPC ID would leak into state
			// and the upgrade-safety gate (empty plan for an unchanged
			// pre-upgrade state) would be violated.
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "default", "is_default": true, "aws_config": {"vpc_id": "vpc-FROM-API-SHOULD-NOT-APPEAR", "subnet_ids": ["subnet-new"], "zones": ["us-east-2b"]}}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	originalAWSConfig := buildAWSConfigState(t, "vpc-ORIGINAL")
	state := &CloudResourceModel{
		ID:            types.StringValue("cloud-1"),
		IsEmptyCloud:  types.BoolValue(false),
		CloudProvider: types.StringValue("AWS"),
		AWSConfig:     originalAWSConfig,
		GCPConfig:     types.ObjectNull(gcpConfigAttrTypes()),
	}

	if err := r.readCloudState(context.Background(), "cloud-1", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.AWSConfig.Equal(originalAWSConfig) {
		t.Errorf("AWSConfig was modified by Read despite already being populated.\nbefore: %#v\nafter:  %#v", originalAWSConfig, state.AWSConfig)
	}
}

func TestReadCloudState_ImportPopulatesNullConfigBlockAndIsEmptyCloud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-2":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-2", "name": "imported", "provider": "AWS", "region": "us-east-2"}}`)
		case "/api/v2/clouds/cloud-2/resources":
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "default", "is_default": true, "cloud_deployment_id": "cd-1", "aws_config": {"vpc_id": "vpc-real", "subnet_ids": ["subnet-1"], "zones": ["us-east-2a"]}, "object_storage": {"bucket_name": "s3://real-bucket"}}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	// A fresh terraform import only ever sets id - everything else, including
	// is_empty_cloud, starts null/unknown.
	state := &CloudResourceModel{
		ID:                types.StringValue("cloud-2"),
		IsEmptyCloud:      types.BoolNull(),
		CloudDeploymentID: types.StringNull(),
		AWSConfig:         types.ObjectNull(awsConfigAttrTypes()),
		GCPConfig:         types.ObjectNull(gcpConfigAttrTypes()),
		KubernetesConfig:  types.ObjectNull(kubernetesConfigAttrTypes()),
		ObjectStorage:     types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:       types.ObjectNull(fileStorageAttrTypes()),
	}

	if err := r.readCloudState(context.Background(), "cloud-2", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if state.IsEmptyCloud.IsNull() || state.IsEmptyCloud.ValueBool() {
		t.Errorf("IsEmptyCloud = %v, want false (a default resource exists)", state.IsEmptyCloud)
	}
	if state.CloudDeploymentID.ValueString() != "cd-1" {
		t.Errorf("CloudDeploymentID = %v, want cd-1", state.CloudDeploymentID)
	}
	if state.AWSConfig.IsNull() {
		t.Fatal("AWSConfig is still null after import - destructive-import bug not fixed")
	}
	var awsModel AWSConfigModel
	state.AWSConfig.As(context.Background(), &awsModel, basetypes.ObjectAsOptions{})
	if awsModel.VPCID.ValueString() != "vpc-real" {
		t.Errorf("AWSConfig.VPCID = %v, want vpc-real", awsModel.VPCID.ValueString())
	}
	if state.ObjectStorage.IsNull() {
		t.Fatal("ObjectStorage is still null after import")
	}
}

func TestReadCloudState_EmptyCloudNeverGetsConfigInjectedEvenAfterResourceAttached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-3":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-3", "name": "split-pattern-cloud", "provider": "AWS", "region": "us-east-2"}}`)
		case "/api/v2/clouds/cloud-3/resources":
			// Simulates a anyscale_cloud_resource having been attached AFTER
			// this cloud was created empty - a real default resource now
			// exists server-side.
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "attached-later", "is_default": true, "aws_config": {"vpc_id": "vpc-should-not-leak-into-cloud-state"}}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	// IsEmptyCloud is explicitly true - set at Create time for the split
	// pattern and persisted ever since. This must be sticky.
	state := &CloudResourceModel{
		ID:            types.StringValue("cloud-3"),
		IsEmptyCloud:  types.BoolValue(true),
		CloudProvider: types.StringValue("AWS"),
		AWSConfig:     types.ObjectNull(awsConfigAttrTypes()),
	}

	if err := r.readCloudState(context.Background(), "cloud-3", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.IsEmptyCloud.ValueBool() {
		t.Error("IsEmptyCloud flipped to false - sticky guard failed, a live empty cloud would be corrupted")
	}
	if !state.AWSConfig.IsNull() {
		t.Errorf("AWSConfig = %v, want null - config from a later-attached resource must never be injected into the empty cloud's own state", state.AWSConfig)
	}
}

func TestReadCloudState_CredentialsNeverPopulated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-4":
			// Even if the API returns credentials in the payload (CloudResult
			// has a Credentials field), it must never reach state.
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-4", "name": "c", "provider": "AWS", "region": "us-east-1", "credentials": "arn:aws:iam::999:role/should-never-be-in-state"}}`)
		case "/api/v2/clouds/cloud-4/resources":
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	state := &CloudResourceModel{ID: types.StringValue("cloud-4"), Credentials: types.StringNull()}

	if err := r.readCloudState(context.Background(), "cloud-4", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.Credentials.IsNull() {
		t.Errorf("Credentials = %v, want null - must never be populated from the API's read-back", state.Credentials)
	}
}

// --- anyscale_cloud_resource: same gates, minus the empty-cloud guard (a
// cloud_resource always represents real, non-empty infrastructure).

func TestReadCloudResource_AlreadyPopulatedConfigBlockIsNeverTouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{"name": "r1", "cloud_deployment_id": "cd-1", "is_default": true, "compute_stack": "VM", "region": "us-east-1", "aws_config": {"vpc_id": "vpc-FROM-API-SHOULD-NOT-APPEAR"}}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	originalAWSConfig := buildAWSConfigState(t, "vpc-ORIGINAL")
	state := &CloudResourceResourceModel{AWSConfig: originalAWSConfig}

	if err := r.readCloudResource(context.Background(), "cloud-id", "r1", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.AWSConfig.Equal(originalAWSConfig) {
		t.Errorf("AWSConfig was modified despite already being populated.\nbefore: %#v\nafter:  %#v", originalAWSConfig, state.AWSConfig)
	}
}

func TestReadCloudResource_ImportPopulatesNullConfigBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{"name": "r1", "cloud_deployment_id": "cd-1", "is_default": true, "compute_stack": "VM", "region": "us-east-1", "provider": "GCP", "gcp_config": {"project_id": "proj-real", "vpc_name": "vpc-real"}}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	// A fresh import only sets id/cloud_id/name - config blocks start null.
	state := &CloudResourceResourceModel{
		GCPConfig: types.ObjectNull(gcpConfigAttrTypes()),
		AWSConfig: types.ObjectNull(awsConfigAttrTypes()),
	}

	if err := r.readCloudResource(context.Background(), "cloud-id", "r1", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if state.GCPConfig.IsNull() {
		t.Fatal("GCPConfig is still null after import - destructive-import bug not fixed for anyscale_cloud_resource")
	}
	var gcpModel GCPConfigModel
	state.GCPConfig.As(context.Background(), &gcpModel, basetypes.ObjectAsOptions{})
	if gcpModel.ProjectID.ValueString() != "proj-real" {
		t.Errorf("GCPConfig.ProjectID = %v, want proj-real", gcpModel.ProjectID.ValueString())
	}
}
