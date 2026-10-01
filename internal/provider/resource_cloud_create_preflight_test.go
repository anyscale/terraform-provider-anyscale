package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
)

// preflightObject builds an object of the given shape with every attribute null
// except those in set.
func preflightObject(t *testing.T, attrTypes map[string]attr.Type, set map[string]attr.Value) types.Object {
	t.Helper()
	vals := make(map[string]attr.Value, len(attrTypes))
	for k, typ := range attrTypes {
		if v, ok := set[k]; ok {
			vals[k] = v
			continue
		}
		null, err := typ.ValueFromTerraform(context.Background(), tftypes.NewValue(typ.TerraformType(context.Background()), nil))
		if err != nil {
			t.Fatalf("null value for %s: %v", k, err)
		}
		vals[k] = null
	}
	obj, diags := types.ObjectValue(attrTypes, vals)
	if diags.HasError() {
		t.Fatalf("build object: %v", diags)
	}
	return obj
}

// preflightPlan returns a create plan for an anyscale_cloud with no blocks.
// computeStack and region are set only when non-empty, mirroring an omitted
// Optional+Computed attribute, which is unknown in a create plan.
func preflightPlan(t *testing.T, name, provider, computeStack, region string) CloudResourceModel {
	t.Helper()
	plan := CloudResourceModel{
		ID:                     types.StringUnknown(),
		Name:                   types.StringValue(name),
		CloudProvider:          types.StringValue(provider),
		ComputeStack:           types.StringUnknown(),
		Region:                 types.StringUnknown(),
		IsPrivateCloud:         types.BoolValue(false),
		AutoAddUser:            types.BoolValue(false),
		Credentials:            types.StringValue("arn:aws:iam::123456789012:role/cp"),
		LineageTrackingEnabled: types.BoolValue(false),
		AggregatedLogsEnabled:  types.BoolValue(false),
		AWSConfig:              types.ObjectNull(awsConfigAttrTypes()),
		GCPConfig:              types.ObjectNull(gcpConfigAttrTypes()),
		AzureConfig:            types.ObjectNull(azureConfigAttrTypes()),
		KubernetesConfig:       types.ObjectNull(kubernetesConfigAttrTypes()),
		ObjectStorage:          types.ObjectNull(objectStorageAttrTypes()),
		FileStorage:            types.ObjectNull(fileStorageAttrTypes()),
		IsEmptyCloud:           types.BoolUnknown(),
		CloudResourceID:        types.StringUnknown(),
		Timeouts:               timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType})},
	}
	if computeStack != "" {
		plan.ComputeStack = types.StringValue(computeStack)
	}
	if region != "" {
		plan.Region = types.StringValue(region)
	}
	return plan
}

func preflightAWSConfig(t *testing.T) types.Object {
	t.Helper()
	return preflightObject(t, awsConfigAttrTypes(), map[string]attr.Value{
		"vpc_id":                    types.StringValue("vpc-0123"),
		"controlplane_iam_role_arn": types.StringValue("arn:aws:iam::123456789012:role/cp"),
		"dataplane_iam_role_arn":    types.StringValue("arn:aws:iam::123456789012:role/dp"),
		"subnet_ids": types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("subnet-1"),
		}),
	})
}

func preflightK8SConfig(t *testing.T) types.Object {
	t.Helper()
	return preflightObject(t, kubernetesConfigAttrTypes(), map[string]attr.Value{
		"anyscale_operator_iam_identity": types.StringValue("arn:aws:iam::123456789012:role/op"),
	})
}

// preflightServer counts calls by "METHOD path" and answers the adopt lookup
// with no match. A POST to /api/v2/clouds is answered with a 500, so a Create
// that reaches it stops there with a recognizable error.
func preflightServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/clouds":
			posts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "mock refuses to create"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &posts
}

// TestCloudResourceCreate_LocalRequirementsCheckedBeforePost proves every
// requirement decidable from local values fails Create before POST
// /api/v2/clouds. Previously each ran after the POST and left a real cloud
// behind, saved to state as tainted.
func TestCloudResourceCreate_LocalRequirementsCheckedBeforePost(t *testing.T) {
	cases := []struct {
		name        string
		plan        func(t *testing.T) CloudResourceModel
		wantSummary string
		wantDetail  string
	}{
		{
			name: "no compute_stack with embedded config",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-preflight-stack", "AWS", "", "us-east-1")
				p.AWSConfig = preflightAWSConfig(t)
				return p
			},
			wantSummary: "Missing Required Field",
			wantDetail:  "compute_stack is required",
		},
		{
			name: "kubernetes cloud with no region to infer",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-preflight-region", "AWS", "K8S", "")
				p.KubernetesConfig = preflightK8SConfig(t)
				return p
			},
			wantSummary: "Region Could Not Be Determined",
			wantDetail:  "region could not be determined",
		},
		{
			name: "VM cloud without aws_config",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-preflight-vm", "AWS", "VM", "us-east-1")
				p.KubernetesConfig = preflightK8SConfig(t)
				return p
			},
			wantSummary: "Invalid Cloud Configuration",
			wantDetail:  "aws_config is required when cloud_provider is AWS and compute_stack is VM",
		},
		{
			name: "kubernetes cloud without object_storage",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-preflight-k8s", "AWS", "K8S", "us-east-1")
				p.KubernetesConfig = preflightK8SConfig(t)
				return p
			},
			wantSummary: "Invalid Cloud Configuration",
			wantDetail:  "object_storage is required when cloud_provider is AWS and compute_stack is K8S",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, posts := preflightServer(t)
			r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}

			_, diags := runCloudResourceCreate(t, r, tc.plan(t))

			if !diagsContainSummary(diags, tc.wantSummary) {
				t.Fatalf("want a %q diagnostic, got: %v", tc.wantSummary, diags)
			}
			if !strings.Contains(fmt.Sprint(diags), tc.wantDetail) {
				t.Errorf("diagnostic detail should contain %q, got: %v", tc.wantDetail, diags)
			}
			if n := posts.Load(); n != 0 {
				t.Fatalf("POST /api/v2/clouds was called %d time(s); a config that cannot succeed must not create a cloud", n)
			}
		})
	}

	// Positive control: a valid config clears the same checks and reaches the POST, which the
	// mock refuses, so the only way to the API error is through the preflight.
	t.Run("control: valid VM config reaches POST", func(t *testing.T) {
		server, posts := preflightServer(t)
		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		p := preflightPlan(t, "tfacc-preflight-ok", "AWS", "VM", "us-east-1")
		p.AWSConfig = preflightAWSConfig(t)

		_, diags := runCloudResourceCreate(t, r, p)

		if !diagsContainSummary(diags, "Cloud Creation Failed") {
			t.Fatalf("want the mock's POST refusal, got: %v", diags)
		}
		if n := posts.Load(); n != 1 {
			t.Fatalf("POST count = %d, want 1", n)
		}
	})
}

// TestValidateEmbeddedCreateConfig covers the plan-time form of the same
// checks: known values are checked, an unknown value skips the check instead
// of failing it, and an empty cloud is never held to the embedded-config rules.
func TestValidateEmbeddedCreateConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("known gap is an error", func(t *testing.T) {
		p := preflightPlan(t, "x", "AWS", "K8S", "")
		p.Region = types.StringNull() // omitted in config
		p.KubernetesConfig = preflightK8SConfig(t)
		diags := validateEmbeddedCreateConfig(ctx, &p, noCloudNamed)
		if !diagsContainSummary(diags, "Region Could Not Be Determined") {
			t.Fatalf("want a region error, got: %v", diags)
		}
	})

	t.Run("control: unknown compute_stack is skipped", func(t *testing.T) {
		p := preflightPlan(t, "x", "AWS", "", "us-east-1")
		p.AWSConfig = preflightAWSConfig(t) // compute_stack stays unknown
		if diags := validateEmbeddedCreateConfig(ctx, &p, noCloudNamed); len(diags) != 0 {
			t.Fatalf("an unknown compute_stack must not error at plan time, got: %v", diags)
		}
	})

	t.Run("control: unknown region is skipped", func(t *testing.T) {
		p := preflightPlan(t, "x", "AWS", "K8S", "")
		p.Region = types.StringUnknown()
		p.KubernetesConfig = preflightK8SConfig(t)
		if diags := validateEmbeddedCreateConfig(ctx, &p, noCloudNamed); len(diags) != 0 {
			t.Fatalf("an unknown region must not error at plan time, got: %v", diags)
		}
	})

	t.Run("control: region inferred from subnet_ids_to_az", func(t *testing.T) {
		p := preflightPlan(t, "x", "AWS", "VM", "")
		p.Region = types.StringNull()
		p.AWSConfig = preflightObject(t, awsConfigAttrTypes(), map[string]attr.Value{
			"vpc_id":                    types.StringValue("vpc-0123"),
			"controlplane_iam_role_arn": types.StringValue("arn:aws:iam::123456789012:role/cp"),
			"dataplane_iam_role_arn":    types.StringValue("arn:aws:iam::123456789012:role/dp"),
			"subnet_ids_to_az": types.MapValueMust(types.StringType, map[string]attr.Value{
				"subnet-1": types.StringValue("us-east-2a"),
			}),
		})
		if diags := validateEmbeddedCreateConfig(ctx, &p, noCloudNamed); len(diags) != 0 {
			t.Fatalf("an inferable region must not error, got: %v", diags)
		}
	})

	t.Run("control: empty cloud is exempt", func(t *testing.T) {
		p := preflightPlan(t, "x", "AWS", "", "")
		p.ComputeStack, p.Region = types.StringNull(), types.StringNull()
		if diags := validateEmbeddedCreateConfig(ctx, &p, noCloudNamed); len(diags) != 0 {
			t.Fatalf("an empty cloud has no embedded requirements, got: %v", diags)
		}
	})
}

// TestCloudResourceCreate_ResponseBodyDebugLogsAreSanitized proves the create
// and add_resource response bodies are redacted in the debug log, like the
// request bodies logged beside them. Both responses echo the request's
// credentials and IAM role.
func TestCloudResourceCreate_ResponseBodyDebugLogsAreSanitized(t *testing.T) {
	const (
		cloudID          = "cld_logsanitize"
		secretCredential = "SECRET-CREDENTIAL-IN-CREATE-RESPONSE"
		secretRole       = "SECRET-ROLE-IN-ADD-RESOURCE-RESPONSE"
		// Not on the redaction list, so it must survive: proves the log is still informative.
		visibleMarker = "cldrsrc_log_visible"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds":
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/clouds":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": "log", "provider": "AWS", "region": "us-east-1", "credentials": %q, "compute_stack": "VM"}}`, cloudID, secretCredential)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/clouds/"+cloudID+"/add_resource":
			_, _ = fmt.Fprintf(w, `{"result": {"cloud_resource_id": %q, "aws_config": {"anyscale_iam_role_id": %q}}}`, visibleMarker, secretRole)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds/"+cloudID:
			_, _ = fmt.Fprintf(w, `{"result": {"id": %q, "name": "log", "provider": "AWS", "region": "us-east-1", "state": "ACTIVE", "status": "ready", "compute_stack": "VM"}}`, cloudID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds/"+cloudID+"/resources":
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var logs bytes.Buffer
	t.Setenv("TF_LOG_PROVIDER", "DEBUG")
	ctx := tflogtest.RootLogger(context.Background(), &logs)

	r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
	plan := preflightPlan(t, "tfacc-log-sanitize", "AWS", "VM", "us-east-1")
	plan.AWSConfig = preflightAWSConfig(t)

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	if d := tfPlan.Set(ctx, &plan); d.HasError() {
		t.Fatalf("plan fixture: %v", d)
	}
	createResp := &resource.CreateResponse{State: tfsdk.State(tfPlan)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics)
	}

	out := logs.String()
	if !strings.Contains(out, visibleMarker) {
		t.Fatalf("the add_resource response should be logged (control); log was:\n%s", out)
	}
	for _, secret := range []string{secretCredential, secretRole} {
		if strings.Contains(out, secret) {
			t.Errorf("debug log contains unsanitized %q", secret)
		}
	}
}

// TestCloudResourceDelete_FailedMachinePoolDetachWarns proves a failed detach
// is reported as a diagnostic warning naming the cloud and the pool, not only
// logged. Delete still goes ahead (the backend decides whether the pool blocks
// it), so without the warning a practitioner learns nothing about pools left
// attached. The control shows a clean detach adds no warning.
func TestCloudResourceDelete_FailedMachinePoolDetachWarns(t *testing.T) {
	const cloudID = "cld_detach_warn"
	const poolName = "tfacc-pool-attached"

	run := func(t *testing.T, detachStatus int) (diagsOut string, warnings, errs int, deletes int32) {
		t.Helper()
		var deleteCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v2/machine_pools/":
				_, _ = fmt.Fprintf(w, `{"result": {"machine_pools": [{"machine_pool_name": %q, "cloud_ids": [%q]}]}}`, poolName, cloudID)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v2/machine_pools/detach":
				w.WriteHeader(detachStatus)
				_, _ = fmt.Fprint(w, `{"result": {}}`)
			case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/clouds/"+cloudID:
				deleteCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		var schemaResp resource.SchemaResponse
		r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
		state := tfsdk.State{Schema: schemaResp.Schema}
		model := preflightPlan(t, "tfacc-detach-warn", "AWS", "VM", "us-east-1")
		model.ID = types.StringValue(cloudID)
		model.IsEmptyCloud = types.BoolValue(false)
		model.CloudResourceID = types.StringNull()
		if d := state.Set(context.Background(), &model); d.HasError() {
			t.Fatalf("state fixture: %v", d)
		}

		resp := &resource.DeleteResponse{}
		r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
		return fmt.Sprint(resp.Diagnostics), resp.Diagnostics.WarningsCount(), resp.Diagnostics.ErrorsCount(), deleteCalls.Load()
	}

	t.Run("failed detach is a warning that names the cloud and pool", func(t *testing.T) {
		out, warnings, errs, deletes := run(t, http.StatusInternalServerError)
		if errs != 0 {
			t.Fatalf("a failed detach must not fail the delete: %s", out)
		}
		if deletes != 1 {
			t.Fatalf("the delete should still be attempted, got %d DELETE call(s)", deletes)
		}
		if warnings != 1 {
			t.Fatalf("want exactly one warning, got %d: %s", warnings, out)
		}
		for _, want := range []string{cloudID, poolName} {
			if !strings.Contains(out, want) {
				t.Errorf("the warning should name %q: %s", want, out)
			}
		}
	})

	t.Run("failed pool listing is a warning that names the cloud and says the pools could not be listed", func(t *testing.T) {
		var deleteCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v2/machine_pools/":
				w.WriteHeader(http.StatusInternalServerError)
			case r.Method == http.MethodDelete && r.URL.Path == "/api/v2/clouds/"+cloudID:
				deleteCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
		var schemaResp resource.SchemaResponse
		r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
		state := tfsdk.State{Schema: schemaResp.Schema}
		model := preflightPlan(t, "tfacc-detach-list", "AWS", "VM", "us-east-1")
		model.ID = types.StringValue(cloudID)
		model.IsEmptyCloud = types.BoolValue(false)
		model.CloudResourceID = types.StringNull()
		if d := state.Set(context.Background(), &model); d.HasError() {
			t.Fatalf("state fixture: %v", d)
		}
		resp := &resource.DeleteResponse{}
		r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
		out := fmt.Sprint(resp.Diagnostics)
		if resp.Diagnostics.ErrorsCount() != 0 || resp.Diagnostics.WarningsCount() != 1 || deleteCalls.Load() != 1 {
			t.Fatalf("want one warning and a delete; diags=%s deletes=%d", out, deleteCalls.Load())
		}
		for _, want := range []string{cloudID, "Could not list machine pools"} {
			if !strings.Contains(out, want) {
				t.Errorf("the warning should contain %q: %s", want, out)
			}
		}
		if strings.Contains(out, "Could not detach machine pools") {
			t.Errorf("a list failure must not claim a detach was attempted: %s", out)
		}
	})

	t.Run("control: clean detach adds no warning", func(t *testing.T) {
		out, warnings, errs, deletes := run(t, http.StatusOK)
		if errs != 0 || warnings != 0 || deletes != 1 {
			t.Fatalf("want a silent delete; warnings=%d errors=%d deletes=%d: %s", warnings, errs, deletes, out)
		}
	})
}

// noCloudNamed is a lookup that finds no existing cloud, so the plan-time
// checks are not exempted by an adopt.
func noCloudNamed(context.Context, string) (string, error) { return "", nil }
