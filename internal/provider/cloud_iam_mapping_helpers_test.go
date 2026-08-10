package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolvePrimaryCloudResourceID(t *testing.T) {
	ctx := context.Background()

	t.Run("single primary resource", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [
					{"cloud_resource_id": "cldrsrc_a", "name": "a", "is_default": false},
					{"cloud_resource_id": "cldrsrc_b", "name": "b", "is_default": true}
				],
				"metadata": {"total": 2, "next_paging_token": null}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")
		id, err := resolvePrimaryCloudResourceID(ctx, client, "cld_x")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "cldrsrc_b" {
			t.Errorf("expected cldrsrc_b, got %q", id)
		}
	})

	t.Run("zero primary resources errors explicitly", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [{"cloud_resource_id": "cldrsrc_a", "name": "a", "is_default": false}],
				"metadata": {"total": 1, "next_paging_token": null}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")
		if _, err := resolvePrimaryCloudResourceID(ctx, client, "cld_x"); err == nil {
			t.Fatal("expected an error for zero primary resources, got nil")
		}
	})

	// This is the case findDefaultInCloudResources (a different, tolerant
	// helper used elsewhere) would silently pick the first match for -
	// resolution for import must never guess.
	t.Run("multiple primary resources errors explicitly rather than guessing", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"results": [
					{"cloud_resource_id": "cldrsrc_a", "name": "a", "is_default": true},
					{"cloud_resource_id": "cldrsrc_b", "name": "b", "is_default": true}
				],
				"metadata": {"total": 2, "next_paging_token": null}
			}`))
		}))
		defer server.Close()

		client := NewClientWithToken(server.URL, "test-token")
		if _, err := resolvePrimaryCloudResourceID(ctx, client, "cld_x"); err == nil {
			t.Fatal("expected an error for multiple primary resources, got nil")
		}
	})
}

func TestValidateIAMMappingSelector(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		wantErr  bool
	}{
		{"valid user selector", "user=alice@example.com", false},
		{"valid project selector", "project=my-project", false},
		{"valid workload-type job", "workload-type=job", false},
		{"valid workload-type service", "workload-type=service", false},
		{"valid workload-type workspace", "workload-type=workspace", false},
		{"valid combined selector", "workload-type=job,project=my-project", false},
		{"invalid key", "team=platform", true},
		{"invalid workload-type value", "workload-type=notebook", true},
		{"invalid selector syntax", "===", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problem := validateIAMMappingSelector(tt.selector)
			gotErr := problem != ""
			if gotErr != tt.wantErr {
				t.Errorf("validateIAMMappingSelector(%q) = %q, wantErr=%v", tt.selector, problem, tt.wantErr)
			}
		})
	}
}

func TestDataplaneIAMMappingRulesRoundTrip(t *testing.T) {
	ctx := context.Background()

	wireRules := []DataplaneIAMMappingRule{
		{Selector: "workload-type=job", Value: "role-a"},
		{Selector: "project=my-project", Value: "role-b"},
	}

	stateList, diags := dataplaneIAMMappingRulesToState(wireRules)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	roundTripped, diags := dataplaneIAMMappingRulesFromState(ctx, stateList)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if len(roundTripped) != len(wireRules) {
		t.Fatalf("expected %d rules, got %d", len(wireRules), len(roundTripped))
	}
	for i, r := range roundTripped {
		if r != wireRules[i] {
			t.Errorf("rule %d: expected %+v, got %+v (order must be preserved)", i, wireRules[i], r)
		}
	}
}

func TestDataplaneIAMMappingRulesToState_EmptyMapsToNull(t *testing.T) {
	// dataplane_iam_mapping has no omitempty on the wire, so GET always
	// emits the key - as {} (zero rules) when unset. That must map to a
	// null list, never an empty-but-non-nil one, or a cloud that has never
	// had a mapping configured would show a phantom empty-rules diff.
	list, diags := dataplaneIAMMappingRulesToState(nil)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !list.IsNull() {
		t.Errorf("expected a null list for zero rules, got %v", list)
	}
}
