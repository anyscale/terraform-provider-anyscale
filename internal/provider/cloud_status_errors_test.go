package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// TestCloudRequestsCarryHTTPStatus pins that the cloud, cloud data source and
// invitation requests report a failed call as an UnexpectedStatusError, so
// AddAPIError shows the HTTP status and the API's own message instead of the
// raw body.
func TestCloudRequestsCarryHTTPStatus(t *testing.T) {
	ctx := context.Background()
	const apiDetail = "the control plane is unavailable"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"detail":"` + apiDetail + `"}}`))
	}))
	t.Cleanup(server.Close)
	client := NewClientWithToken(server.URL, "test-token")

	cases := map[string]func() error{
		"anyscale_cloud readCloudState": func() error {
			_, err := (&CloudResource{client: client}).readCloudState(ctx, "cld_x", &CloudResourceModel{}, nil)
			return err
		},
		"anyscale_cloud add_resource": func() error {
			plan := CloudResourceModel{AWSConfig: preflightAWSConfig(t)}
			return (&CloudResource{client: client}).addCloudResource(ctx, &plan, "cld_x", "AWS", "VM")
		},
		"anyscale_cloud data source": func() error {
			var config CloudDataSourceModel
			return (&CloudDataSource{client: client}).readCloudIntoModel(ctx, "cld_x", &config)
		},
		"anyscale_iam_organization_invitation": func() error {
			_, err := (&OrganizationInvitationResource{client: client}).getInvitationByID(ctx, "orginv_x")
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !isHTTPStatus(err, http.StatusInternalServerError) {
				t.Fatalf("error = %v, want an UnexpectedStatusError with status 500", err)
			}
			var diags diag.Diagnostics
			AddAPIError(&diags, "do it", err)
			if want := "Failed to do it (HTTP 500): " + apiDetail; !strings.HasPrefix(diags[0].Detail(), want) {
				t.Errorf("diagnostic = %q, want prefix %q", diags[0].Detail(), want)
			}
		})
	}
}
