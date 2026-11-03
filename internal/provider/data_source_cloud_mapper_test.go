package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestCloudDataSources_SameCloudSameValues feeds one cloud through both
// anyscale_cloud and anyscale_clouds and asserts every attribute they share
// comes out identical. Each shared attribute is found by its tfsdk tag, so an
// attribute added to one model and the mapper but not copied into the other
// fails here too.
func TestCloudDataSources_SameCloudSameValues(t *testing.T) {
	ctx := context.Background()
	externalID := "org_external"
	cloud := CloudResult{
		ID:                      "cld_x",
		Name:                    "mapper-cloud",
		Provider:                "AWS",
		ComputeStack:            "VM",
		Region:                  "us-west-2",
		IsK8s:                   true,
		AvailabilityZones:       []string{"us-west-2a", "us-west-2b"},
		IsPrivateCloud:          true,
		AutoAddUser:             true,
		LineageTrackingEnabled:  true,
		ExternalID:              &externalID,
		CreatorID:               "usr_creator",
		CreatedAt:               "2026-11-03T00:00:00Z",
		Status:                  "", // mapped to null by both
		State:                   "ACTIVE",
		Version:                 "v2",
		IsDefault:               true,
		IsAggregatedLogsEnabled: true,
	}

	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Errorf("encoding mock response: %v", err)
		}
	}
	mux.HandleFunc("/api/v2/clouds/cld_x", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, CloudResponse{Result: cloud})
	})
	mux.HandleFunc("/api/v2/clouds/cld_x/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}, "metadata": map[string]any{"next_paging_token": nil}})
	})
	mux.HandleFunc("/api/v2/clouds/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []CloudResult{cloud}, "metadata": map[string]any{"next_paging_token": nil}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := NewClientWithToken(server.URL, "test-token")

	var single CloudDataSourceModel
	if err := (&CloudDataSource{client: client}).readCloudIntoModel(ctx, "cld_x", &single); err != nil {
		t.Fatalf("anyscale_cloud read: %v", err)
	}
	list, err := (&CloudsDataSource{client: client}).fetchClouds(ctx, nil, "", "")
	if err != nil {
		t.Fatalf("anyscale_clouds read: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("anyscale_clouds returned %d clouds, want 1", len(list))
	}
	plural := list[0]

	// Both against the fixture, so a mapper bug fails each data source, not
	// only the comparison between them.
	azs, _ := types.ListValueFrom(ctx, types.StringType, cloud.AvailabilityZones)
	want := map[string]attr.Value{
		"id":                       types.StringValue("cld_x"),
		"name":                     types.StringValue("mapper-cloud"),
		"cloud_provider":           types.StringValue("AWS"),
		"compute_stack":            types.StringValue("VM"),
		"region":                   types.StringValue("us-west-2"),
		"status":                   types.StringNull(),
		"state":                    types.StringValue("ACTIVE"),
		"created_at":               types.StringValue("2026-11-03T00:00:00Z"),
		"creator_id":               types.StringValue("usr_creator"),
		"is_default":               types.BoolValue(true),
		"is_k8s":                   types.BoolValue(true),
		"is_private_cloud":         types.BoolValue(true),
		"auto_add_user":            types.BoolValue(true),
		"lineage_tracking_enabled": types.BoolValue(true),
		"aggregated_logs_enabled":  types.BoolValue(true),
		"availability_zones":       azs,
		"version":                  types.StringValue("v2"),
		"external_id":              types.StringValue("org_external"),
	}
	for name, model := range map[string]any{"anyscale_cloud": single, "anyscale_clouds": plural} {
		v := reflect.ValueOf(model)
		for i := 0; i < v.NumField(); i++ {
			tag := v.Type().Field(i).Tag.Get("tfsdk")
			w, ok := want[tag]
			if !ok {
				continue
			}
			if got := v.Field(i).Interface().(attr.Value); !got.Equal(w) {
				t.Errorf("%s.%s = %s, want %s", name, tag, got, w)
			}
		}
	}

	singleByTag := map[string]attr.Value{}
	sv := reflect.ValueOf(single)
	for i := 0; i < sv.NumField(); i++ {
		singleByTag[sv.Type().Field(i).Tag.Get("tfsdk")] = sv.Field(i).Interface().(attr.Value)
	}

	pv := reflect.ValueOf(plural)
	for i := 0; i < pv.NumField(); i++ {
		tag := pv.Type().Field(i).Tag.Get("tfsdk")
		pluralValue := pv.Field(i).Interface().(attr.Value)
		got, ok := singleByTag[tag]
		if !ok {
			t.Errorf("anyscale_cloud has no %q attribute", tag)
			continue
		}
		if _, ok := want[tag]; !ok {
			t.Errorf("shared attribute %q has no expected value in this test; add one", tag)
		}
		if !got.Equal(pluralValue) {
			t.Errorf("%s: anyscale_cloud = %s, anyscale_clouds = %s", tag, got, pluralValue)
		}
	}
}
