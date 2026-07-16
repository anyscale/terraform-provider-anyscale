package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// newImportStateResponse builds an ImportStateResponse with a State
// pre-initialized to an all-null value matching r's schema, the same way the
// real framework runtime initializes it before calling ImportState - needed
// so SetAttribute has a schema to validate against.
func newImportStateResponse(t *testing.T, r resource.ResourceWithImportState) *resource.ImportStateResponse {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return &resource.ImportStateResponse{
		State: tfsdk.State{
			Schema: schemaResp.Schema,
			Raw:    tftypes.NewValue(tfType, nil),
		},
	}
}

// This file covers C3-v2's hard acceptance gates. C3-v2 supersedes the
// original C3 Phase 1 design after C12 exposed a plan-consistency flaw:
// config blocks (aws_config/gcp_config/kubernetes_config/object_storage/
// file_storage) are NOT Computed, so populating one during Create/Read that
// the user's .tf never set produces a hard "provider produced inconsistent
// result after apply" error - not a harmless enhancement. Under C3-v2:
//   - readCloudState/readCloudResource NEVER touch config blocks, in any
//     state (null or populated). They still backfill the Computed fields
//     (is_empty_cloud, cloud_deployment_id).
//   - ImportState is the ONLY place that recovers config blocks from the
//     API, and only the compute-stack-REQUIRED ones.

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

// --- readCloudState: never touches config blocks, in either direction ---

func TestReadCloudState_NeverTouchesAlreadyPopulatedConfigBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-1":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-1", "name": "prod", "provider": "AWS", "region": "us-east-1"}}`)
		case "/api/v2/clouds/cloud-1/resources":
			// Deliberately DIFFERENT from what's already in state - if this
			// leaked into state, the upgrade-safety gate (empty plan for an
			// unchanged pre-upgrade state) would be violated.
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

// TestReadCloudState_NeverInjectsIntoNullConfigBlock is the literal
// regression test for C3-v2: this is the exact shape of a K8S-only create
// (kubernetes_config set, aws_config/gcp_config genuinely never configured -
// optional for K8S) whose first post-create Read used to inject aws_config
// from the default resource, which Terraform then hard-errors on
// ("inconsistent result after apply: .aws_config was absent, but now
// present") since aws_config is not Computed. readCloudState must leave a
// null config block null, full stop - the API returning one is irrelevant.
func TestReadCloudState_NeverInjectsIntoNullConfigBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-k8s":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-k8s", "name": "k8s-cloud", "provider": "AWS", "region": "us-east-1", "compute_stack": "K8S"}}`)
		case "/api/v2/clouds/cloud-k8s/resources":
			// The backend may echo back an aws_config the K8S add_resource
			// call never sent (or one the operator infers) - doesn't matter
			// why the API has one; state must not gain it either way.
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "default", "is_default": true, "compute_stack": "K8S", "aws_config": {"vpc_id": "vpc-MUST-NOT-BE-INJECTED"}, "kubernetes_config": {"anyscale_operator_iam_identity": "arn:aws:iam::1:role/op"}}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	// Mirrors exactly what Create() leaves in `plan` right before its own
	// call to readCloudState for a K8S-only all-in-one config: aws_config
	// was never in the user's .tf, so it's null - not "not yet populated".
	state := &CloudResourceModel{
		ID:               types.StringValue("cloud-k8s"),
		IsEmptyCloud:     types.BoolValue(false),
		CloudProvider:    types.StringValue("AWS"),
		AWSConfig:        types.ObjectNull(awsConfigAttrTypes()),
		GCPConfig:        types.ObjectNull(gcpConfigAttrTypes()),
		KubernetesConfig: buildKubernetesConfigState(t),
		ObjectStorage:    types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:      types.ObjectNull(fileStorageAttrTypes()),
	}

	if err := r.readCloudState(context.Background(), "cloud-k8s", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.AWSConfig.IsNull() {
		t.Errorf("AWSConfig = %v, want null - a config block the user's .tf never set must never be injected by Read (this is the exact C12/C3-v2 regression)", state.AWSConfig)
	}
}

func buildKubernetesConfigState(t *testing.T) types.Object {
	t.Helper()
	obj, diags := flattenKubernetesConfig(context.Background(), &KubernetesConfig{AnyscaleOperatorIAMIdentity: "arn:aws:iam::1:role/op"})
	if diags.HasError() {
		t.Fatalf("failed to build test kubernetes_config: %v", diags)
	}
	return obj
}

// --- readCloudState: Computed-only backfills still work ---

func TestReadCloudState_ComputedFieldsStillBackfillOnImport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-2":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-2", "name": "imported", "provider": "AWS", "region": "us-east-2"}}`)
		case "/api/v2/clouds/cloud-2/resources":
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "default", "is_default": true, "cloud_deployment_id": "cd-1", "aws_config": {"vpc_id": "vpc-real"}}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	// A fresh terraform import only ever sets id via ImportState - the
	// Computed fields start null/unknown, same as before C3-v2.
	state := &CloudResourceModel{
		ID:                types.StringValue("cloud-2"),
		IsEmptyCloud:      types.BoolNull(),
		CloudDeploymentID: types.StringNull(),
		AWSConfig:         types.ObjectNull(awsConfigAttrTypes()),
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
	// The Computed fields backfill, but config blocks still do not - that's
	// ImportState's job now, tested separately below.
	if !state.AWSConfig.IsNull() {
		t.Errorf("AWSConfig = %v, want still null - readCloudState never populates config blocks under C3-v2", state.AWSConfig)
	}
}

func TestReadCloudState_EmptyCloudStaysEmptyEvenAfterResourceAttached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-3":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-3", "name": "multi-resource-cloud", "provider": "AWS", "region": "us-east-2"}}`)
		case "/api/v2/clouds/cloud-3/resources":
			// Simulates a anyscale_cloud_resource having been attached AFTER
			// this cloud was created empty - a real default resource now
			// exists server-side.
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "attached-later", "is_default": true, "cloud_deployment_id": "cd-later"}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	// IsEmptyCloud is explicitly true - set at Create time for the
	// multi-resource cloud pattern and persisted ever since. This must be sticky.
	state := &CloudResourceModel{
		ID:                types.StringValue("cloud-3"),
		IsEmptyCloud:      types.BoolValue(true),
		CloudDeploymentID: types.StringNull(),
		CloudProvider:     types.StringValue("AWS"),
	}

	if err := r.readCloudState(context.Background(), "cloud-3", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.IsEmptyCloud.ValueBool() {
		t.Error("IsEmptyCloud flipped to false - sticky guard failed, a live empty cloud would be corrupted")
	}
	if !state.CloudDeploymentID.IsNull() {
		t.Errorf("CloudDeploymentID = %v, want still null - the empty-cloud gate blocks backfilling this too, consistent with never touching a known-empty cloud's state", state.CloudDeploymentID)
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

// --- readCloudResource: never touches config blocks, in either direction ---

func TestReadCloudResource_NeverTouchesAlreadyPopulatedConfigBlock(t *testing.T) {
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

func TestReadCloudResource_NeverInjectsIntoNullConfigBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{"name": "r1", "cloud_deployment_id": "cd-1", "is_default": true, "compute_stack": "VM", "region": "us-east-1", "provider": "GCP", "gcp_config": {"project_id": "proj-MUST-NOT-BE-INJECTED"}}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	state := &CloudResourceResourceModel{
		GCPConfig: types.ObjectNull(gcpConfigAttrTypes()),
		AWSConfig: types.ObjectNull(awsConfigAttrTypes()),
	}

	if err := r.readCloudResource(context.Background(), "cloud-id", "r1", state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.GCPConfig.IsNull() {
		t.Errorf("GCPConfig = %v, want still null - readCloudResource never populates config blocks under C3-v2 (same regression class as anyscale_cloud)", state.GCPConfig)
	}
}

// --- ImportState: the ONLY place config blocks are recovered, required-only ---

func TestRequiredImportConfigBlocks_VMPopulatesProviderBlockOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("AWS: aws_config recovered, object_storage/file_storage are not (optional for VM)", func(t *testing.T) {
		defaultResource := &CloudDeploymentResult{
			ComputeStack:  "VM",
			AWSConfig:     &AWSConfig{VPCID: "vpc-real"},
			ObjectStorage: &ObjectStorage{BucketName: "s3://real-bucket"},
			FileStorage:   &FileStorage{FileStorageID: "fs-real"},
		}

		blocks, diags := requiredImportConfigBlocks(ctx, "AWS", defaultResource)
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}

		if _, ok := blocks["aws_config"]; !ok {
			t.Fatal("aws_config missing - destructive-import bug not fixed for VM/AWS")
		}
		if _, ok := blocks["object_storage"]; ok {
			t.Error("object_storage present - it's optional for VM and must not be recovered at import")
		}
		if _, ok := blocks["file_storage"]; ok {
			t.Error("file_storage present - it's optional everywhere and must not be recovered at import")
		}
		if _, ok := blocks["gcp_config"]; ok {
			t.Error("gcp_config present on an AWS cloud")
		}

		var awsModel AWSConfigModel
		blocks["aws_config"].As(ctx, &awsModel, basetypes.ObjectAsOptions{})
		if awsModel.VPCID.ValueString() != "vpc-real" {
			t.Errorf("aws_config.VPCID = %v, want vpc-real", awsModel.VPCID.ValueString())
		}
	})

	t.Run("GCP: gcp_config recovered, aws_config is not", func(t *testing.T) {
		defaultResource := &CloudDeploymentResult{
			ComputeStack: "VM",
			GCPConfig:    &GCPConfig{ProjectID: "proj-real"},
		}

		blocks, diags := requiredImportConfigBlocks(ctx, "GCP", defaultResource)
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}

		if _, ok := blocks["gcp_config"]; !ok {
			t.Fatal("gcp_config missing - destructive-import bug not fixed for VM/GCP")
		}
		if _, ok := blocks["aws_config"]; ok {
			t.Error("aws_config present on a GCP cloud")
		}
	})
}

func TestRequiredImportConfigBlocks_K8SPopulatesKubernetesConfigAndObjectStorageOnly(t *testing.T) {
	ctx := context.Background()
	defaultResource := &CloudDeploymentResult{
		ComputeStack:     "K8S",
		Provider:         "AWS",
		KubernetesConfig: &KubernetesConfig{AnyscaleOperatorIAMIdentity: "arn:aws:iam::1:role/op"},
		ObjectStorage:    &ObjectStorage{BucketName: "s3://k8s-bucket"},
		// K8S: aws_config is OPTIONAL - present here to prove it's still not
		// recovered even when the API happens to have one.
		AWSConfig:   &AWSConfig{VPCID: "vpc-optional-for-k8s"},
		FileStorage: &FileStorage{FileStorageID: "fs-optional"},
	}

	blocks, diags := requiredImportConfigBlocks(ctx, "AWS", defaultResource)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if _, ok := blocks["kubernetes_config"]; !ok {
		t.Error("kubernetes_config missing - destructive-import bug not fixed for K8S")
	}
	if _, ok := blocks["object_storage"]; !ok {
		t.Error("object_storage missing - required for K8S, must be recovered at import")
	}
	if _, ok := blocks["aws_config"]; ok {
		t.Error("aws_config present - optional for K8S, must not be recovered even though the API had one")
	}
	if _, ok := blocks["file_storage"]; ok {
		t.Error("file_storage present - optional everywhere, must not be recovered")
	}
	if len(blocks) != 2 {
		t.Errorf("blocks = %v, want exactly kubernetes_config + object_storage", blocks)
	}
}

func TestRequiredImportConfigBlocks_NilResourceReturnsEmpty(t *testing.T) {
	blocks, diags := requiredImportConfigBlocks(context.Background(), "AWS", nil)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if len(blocks) != 0 {
		t.Errorf("blocks = %v, want empty for a nil resource (empty cloud, nothing to recover)", blocks)
	}
}

// TestCloudImportState_RecoversRequiredBlockEndToEnd exercises anyscale_cloud's
// real ImportState against a mocked API, proving the full wiring (not just
// the pure decision function above) - id -> GET cloud -> list resources ->
// SetAttribute.
func TestCloudImportState_RecoversRequiredBlockEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v2/clouds/cloud-import":
			_, _ = fmt.Fprint(w, `{"result": {"id": "cloud-import", "name": "c", "provider": "AWS", "region": "us-east-1"}}`)
		case "/api/v2/clouds/cloud-import/resources":
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "default", "is_default": true, "compute_stack": "VM", "aws_config": {"vpc_id": "vpc-recovered"}}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	req := resource.ImportStateRequest{ID: "cloud-import"}
	resp := newImportStateResponse(t, r)

	r.ImportState(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}

	var state CloudResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &state)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error reading back state: %v", resp.Diagnostics)
	}

	if state.ID.ValueString() != "cloud-import" {
		t.Errorf("ID = %v, want cloud-import", state.ID.ValueString())
	}
	if state.AWSConfig.IsNull() {
		t.Fatal("AWSConfig is null after ImportState - required-block recovery not wired up end to end")
	}
	var awsModel AWSConfigModel
	state.AWSConfig.As(context.Background(), &awsModel, basetypes.ObjectAsOptions{})
	if awsModel.VPCID.ValueString() != "vpc-recovered" {
		t.Errorf("AWSConfig.VPCID = %v, want vpc-recovered", awsModel.VPCID.ValueString())
	}
}

// TestCloudResourceImportState_RecoversRequiredBlockEndToEnd is the
// anyscale_cloud_resource equivalent of the above: id:name -> parse -> list
// resources -> find by name -> SetAttribute, proving the full wiring for a
// K8S resource (kubernetes_config + object_storage recovered, file_storage
// and the optional aws_config are not, even though the mock includes both).
func TestCloudResourceImportState_RecoversRequiredBlockEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{
				"name": "k8s-resource", "is_default": true, "compute_stack": "K8S", "provider": "AWS",
				"kubernetes_config": {"anyscale_operator_iam_identity": "arn:aws:iam::1:role/op"},
				"object_storage": {"bucket_name": "s3://k8s-recovered-bucket"},
				"aws_config": {"vpc_id": "vpc-optional-must-not-be-recovered"},
				"file_storage": {"file_storage_id": "fs-optional-must-not-be-recovered"}
			}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
	req := resource.ImportStateRequest{ID: "cloud-id:k8s-resource"}
	resp := newImportStateResponse(t, r)

	r.ImportState(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}

	var state CloudResourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &state)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error reading back state: %v", resp.Diagnostics)
	}

	if state.KubernetesConfig.IsNull() {
		t.Error("KubernetesConfig is null after ImportState - required-block recovery not wired up end to end")
	}
	if state.ObjectStorage.IsNull() {
		t.Error("ObjectStorage is null after ImportState - required for K8S, must be recovered")
	}
	if !state.AWSConfig.IsNull() {
		t.Error("AWSConfig is populated after ImportState - optional for K8S, must not be recovered even though the mock had one")
	}
	if !state.FileStorage.IsNull() {
		t.Error("FileStorage is populated after ImportState - optional everywhere, must not be recovered")
	}
}
