package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestFetchCurrentOrganization_HappyPath covers AC1: a zero-argument read
// maps all four attributes from organizations[0].
func TestFetchCurrentOrganization_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"result": {
				"organizations": [
					{"id": "org_abc", "name": "Acme Corp", "public_identifier": "acme-corp", "default_cloud_id": "cld_123"}
				]
			}
		}`)
	}))
	defer server.Close()

	org, err := fetchCurrentOrganization(context.Background(), NewClientWithToken(server.URL, "test-token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if org.ID.ValueString() != "org_abc" {
		t.Errorf("ID = %q, want %q", org.ID.ValueString(), "org_abc")
	}
	if org.Name.ValueString() != "Acme Corp" {
		t.Errorf("Name = %q, want %q", org.Name.ValueString(), "Acme Corp")
	}
	if org.PublicIdentifier.ValueString() != "acme-corp" {
		t.Errorf("PublicIdentifier = %q, want %q", org.PublicIdentifier.ValueString(), "acme-corp")
	}
	if org.DefaultCloudID.ValueString() != "cld_123" {
		t.Errorf("DefaultCloudID = %q, want %q", org.DefaultCloudID.ValueString(), "cld_123")
	}
}

// TestFetchCurrentOrganization_NullDefaultCloudID is the mutation-proof test
// for AC2: when userinfo omits default_cloud_id, state must hold
// types.StringNull(), never types.StringValue(""). Asserting only
// ValueString() == "" would pass for either representation, so this asserts
// IsNull() and non-equality to StringValue("") explicitly - flipping the
// implementation to StringValue(*ptr) with a "" zero-value fallback must
// fail this test.
func TestFetchCurrentOrganization_NullDefaultCloudID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"result": {
				"organizations": [
					{"id": "org_abc", "name": "Acme Corp", "public_identifier": "acme-corp"}
				]
			}
		}`)
	}))
	defer server.Close()

	org, err := fetchCurrentOrganization(context.Background(), NewClientWithToken(server.URL, "test-token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !org.DefaultCloudID.IsNull() {
		t.Errorf("DefaultCloudID.IsNull() = false, want true (ValueString() = %q) - omitted default_cloud_id must map to types.StringNull(), not types.StringValue(\"\")", org.DefaultCloudID.ValueString())
	}
	if org.DefaultCloudID.Equal(types.StringValue("")) {
		t.Error("DefaultCloudID must not equal types.StringValue(\"\") when the API omits the field")
	}
}

// TestFetchCurrentOrganization_EmptyOrganizations covers AC3: an empty
// organizations array is a defensive anomaly, not a panic.
func TestFetchCurrentOrganization_EmptyOrganizations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result": {"organizations": []}}`)
	}))
	defer server.Close()

	_, err := fetchCurrentOrganization(context.Background(), NewClientWithToken(server.URL, "test-token"))
	if err == nil {
		t.Fatal("expected an error for empty organizations, got nil")
	}
	if !strings.Contains(err.Error(), "userinfo returned no organization for the authenticated token") {
		t.Errorf("error = %q, want it to contain the contract's exact anomaly message", err.Error())
	}
}
