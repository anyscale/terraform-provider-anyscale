package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestRegistryTypeNames pins the provider's registered type names for the
// anyscale_iam_ role resources: the three renamed types are present and none
// of the pre-0.37.0 names survive (docs/decisions/role-grant-rename/).
func TestRegistryTypeNames(t *testing.T) {
	p := &AnyscaleProvider{}

	wantResources := map[string]bool{
		"anyscale_iam_role_grant":   false,
		"anyscale_iam_role_binding": false,
	}
	neverResources := []string{
		"anyscale_role_binding",
		"anyscale_role_binding_policy",
	}

	for _, f := range p.Resources(context.Background()) {
		var resp resource.MetadataResponse
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "anyscale"}, &resp)
		if _, ok := wantResources[resp.TypeName]; ok {
			wantResources[resp.TypeName] = true
		}
		for _, n := range neverResources {
			if resp.TypeName == n {
				t.Errorf("registry exposes removed resource type %q", n)
			}
		}
	}
	for name, seen := range wantResources {
		if !seen {
			t.Errorf("registry does not expose resource type %q", name)
		}
	}

	wantDataSources := map[string]bool{
		"anyscale_iam_role_binding": false,
	}

	for _, f := range p.DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		f().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "anyscale"}, &resp)
		if _, ok := wantDataSources[resp.TypeName]; ok {
			wantDataSources[resp.TypeName] = true
		}
		if resp.TypeName == "anyscale_role_binding_policy" {
			t.Errorf("registry exposes removed data source type %q", resp.TypeName)
		}
	}
	for name, seen := range wantDataSources {
		if !seen {
			t.Errorf("registry does not expose data source type %q", name)
		}
	}
}
