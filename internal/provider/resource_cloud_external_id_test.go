package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// createBodyServer answers the adopt lookup with no match and records the body
// of POST /api/v2/clouds, which it then refuses, so Create stops right after.
func createBodyServer(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/clouds":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/clouds":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("POST body is not JSON: %v", err)
			}
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": {"detail": "mock refuses to create"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

func awsConfigWithExternalID(t *testing.T, externalID string) types.Object {
	t.Helper()
	return preflightObject(t, awsConfigAttrTypes(), map[string]attr.Value{
		"vpc_id":                    types.StringValue("vpc-0123"),
		"controlplane_iam_role_arn": types.StringValue("arn:aws:iam::123456789012:role/cp"),
		"dataplane_iam_role_arn":    types.StringValue("arn:aws:iam::123456789012:role/dp"),
		"external_id":               types.StringValue(externalID),
		"subnet_ids": types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("subnet-1"),
		}),
	})
}

// TestCloudResourceCreate_SendsCloudLevelExternalID covers the create request.
// The services load balancer assumes the control-plane role with the cloud
// record's own external_id, falling back to the cloud ID when it is null, which
// the role's trust policy rejects. So aws_config.external_id must be sent on
// POST /api/v2/clouds, and only when set.
func TestCloudResourceCreate_SendsCloudLevelExternalID(t *testing.T) {
	const externalID = "org_abcd-1234"

	cases := []struct {
		name   string
		plan   func(t *testing.T) CloudResourceModel
		wantID string // "" means the key must be absent
	}{
		{
			name: "AWS VM with external_id sends it",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-extid", "AWS", "VM", "us-east-1")
				p.AWSConfig = awsConfigWithExternalID(t, externalID)
				return p
			},
			wantID: externalID,
		},
		{
			// The API refuses a non-org_ cloud-level external_id; sending one would fail a
			// create that works without it.
			name: "AWS VM with a non-org_ external_id omits it",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-extid", "AWS", "VM", "us-east-1")
				p.AWSConfig = awsConfigWithExternalID(t, "my-external-id")
				return p
			},
		},
		{
			name: "AWS VM without external_id omits it",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-extid", "AWS", "VM", "us-east-1")
				p.AWSConfig = preflightAWSConfig(t)
				return p
			},
		},
		{
			name: "AWS K8S omits it",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-extid", "AWS", "K8S", "us-east-1")
				p.KubernetesConfig = preflightK8SConfig(t)
				p.ObjectStorage = preflightObject(t, objectStorageAttrTypes(), map[string]attr.Value{
					"bucket_name": types.StringValue("s3://tfacc-bucket"),
				})
				return p
			},
		},
		{
			name: "GCP omits it",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-extid", "GCP", "K8S", "us-central1")
				p.KubernetesConfig = preflightK8SConfig(t)
				p.ObjectStorage = preflightObject(t, objectStorageAttrTypes(), map[string]attr.Value{
					"bucket_name": types.StringValue("s3://tfacc-bucket"),
				})
				return p
			},
		},
		{
			name: "empty cloud omits it",
			plan: func(t *testing.T) CloudResourceModel {
				p := preflightPlan(t, "tfacc-extid", "AWS", "", "")
				p.ComputeStack, p.Region = types.StringNull(), types.StringNull()
				return p
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, bodies := createBodyServer(t)
			r := &CloudResource{client: NewClientWithToken(server.URL, "test-token")}
			_, diags := runCloudResourceCreate(t, r, tc.plan(t))

			got := bodies()
			if len(got) != 1 {
				t.Fatalf("POST /api/v2/clouds count = %d, want 1; diags: %v", len(got), diags)
			}
			value, present := got[0]["external_id"]
			switch {
			case tc.wantID == "" && present:
				t.Errorf("external_id = %v, want the key absent", value)
			case tc.wantID != "" && value != tc.wantID:
				t.Errorf("external_id = %v (present=%v), want %q", value, present, tc.wantID)
			}
		})
	}
}

// TestWarnCreateExternalIDNotRecorded covers the create-time plan warning for
// an aws_config.external_id that Create will not record on the cloud.
func TestWarnCreateExternalIDNotRecorded(t *testing.T) {
	ctx := context.Background()
	planWith := func(t *testing.T, externalID types.String) CloudResourceModel {
		p := preflightPlan(t, "tfacc-extid", "AWS", "VM", "us-east-1")
		p.AWSConfig = preflightObject(t, awsConfigAttrTypes(), map[string]attr.Value{
			"vpc_id":                    types.StringValue("vpc-0123"),
			"controlplane_iam_role_arn": types.StringValue("arn:aws:iam::123456789012:role/cp"),
			"external_id":               externalID,
		})
		return p
	}

	t.Run("non-org_ value is a warning, not an error", func(t *testing.T) {
		p := planWith(t, types.StringValue("my-external-id"))
		diags := warnCreateExternalIDNotRecorded(ctx, &p)
		if diags.HasError() {
			t.Fatalf("want no error, got: %v", diags)
		}
		if diags.WarningsCount() != 1 || diags[0].Summary() != "External ID Not Recorded On The Cloud" {
			t.Fatalf("want one external ID warning, got: %v", diags)
		}
		if detail := diags[0].Detail(); !strings.Contains(detail, "services on a new cloud cannot reach their load balancer") {
			t.Errorf("detail %q does not state the consequence", detail)
		}
	})

	for name, v := range map[string]types.String{
		"org_ value":  types.StringValue("org_abcd-1234"),
		"unset":       types.StringNull(),
		"unknown":     types.StringUnknown(),
		"empty value": types.StringValue(""),
	} {
		t.Run("control: "+name+" has no warning", func(t *testing.T) {
			p := planWith(t, v)
			if diags := warnCreateExternalIDNotRecorded(ctx, &p); len(diags) != 0 {
				t.Fatalf("want no diagnostics, got: %v", diags)
			}
		})
	}
}

// TestCloudResourceModifyPlan_WarnsOnNonOrgExternalID proves ModifyPlan itself
// surfaces the warning on a create plan, not only the helper.
func TestCloudResourceModifyPlan_WarnsOnNonOrgExternalID(t *testing.T) {
	ctx := context.Background()
	r := &CloudResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	run := func(t *testing.T, externalID string) diag.Diagnostics {
		p := preflightPlan(t, "tfacc-extid", "AWS", "VM", "us-east-1")
		p.AWSConfig = awsConfigWithExternalID(t, externalID)
		plan := tfsdk.Plan{Schema: schemaResp.Schema}
		if d := plan.Set(ctx, &p); d.HasError() {
			t.Fatalf("plan fixture: %v", d)
		}
		req := resource.ModifyPlanRequest{
			Plan:   plan,
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: plan.Raw},
			State:  tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)},
		}
		resp := &resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(ctx, req, resp)
		return resp.Diagnostics
	}

	diags := run(t, "my-external-id")
	if diags.HasError() {
		t.Fatalf("want no error, got: %v", diags)
	}
	if !diagsContainSummary(diags, "External ID Not Recorded On The Cloud") {
		t.Fatalf("create plan with a non-org_ external_id: want the warning, got: %v", diags)
	}

	if diags := run(t, "org_abcd-1234"); diagsContainSummary(diags, "External ID Not Recorded On The Cloud") {
		t.Errorf("control: an org_ external_id must not warn, got: %v", diags)
	}
}
