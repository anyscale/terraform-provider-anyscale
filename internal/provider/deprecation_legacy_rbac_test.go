package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rsschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// deprecatable is what resource and data source schemas, and their attributes, share.
type deprecatable interface {
	GetDeprecationMessage() string
	GetMarkdownDescription() string
}

func resourceSchema(t *testing.T, r resource.Resource) rsschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func dataSourceSchema(t *testing.T, d datasource.DataSource) dsschema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

// dataSourceAttr resolves a dotted path ("users.base_role") through list-nested
// attributes. A missing path fails the test: a renamed attribute must not pass silently.
func dataSourceAttr(t *testing.T, s dsschema.Schema, dotted string) deprecatable {
	t.Helper()
	attrs := s.Attributes
	parts := strings.Split(dotted, ".")
	for i, p := range parts {
		a, ok := attrs[p]
		if !ok {
			t.Fatalf("attribute %q not found in schema", dotted)
		}
		if i == len(parts)-1 {
			return a
		}
		nested, ok := a.(dsschema.ListNestedAttribute)
		if !ok {
			t.Fatalf("%q: %q is not a list-nested attribute", dotted, p)
		}
		attrs = nested.NestedObject.Attributes
	}
	return nil
}

func resourceAttr(t *testing.T, s rsschema.Schema, name string) deprecatable {
	t.Helper()
	a, ok := s.Attributes[name]
	if !ok {
		t.Fatalf("attribute %q not found in schema", name)
	}
	return a
}

func expectDeprecated(t *testing.T, what string, d deprecatable) {
	t.Helper()
	if d.GetDeprecationMessage() == "" {
		t.Errorf("%s: DeprecationMessage is empty", what)
	}
	if !strings.Contains(d.GetMarkdownDescription(), "deprecation") && !strings.Contains(d.GetMarkdownDescription(), "Deprecated") {
		t.Errorf("%s: MarkdownDescription does not point at the deprecation (the Registry docs do not render DeprecationMessage)", what)
	}
}

// Every legacy RBAC surface carries both channels: the warning and the docs callout.
func TestLegacyRBACDeprecation_Positive(t *testing.T) {
	expectDeprecated(t, "anyscale_cloud_access", resourceSchema(t, NewCloudAccessResource()))
	expectDeprecated(t, "anyscale_organization_user_role", resourceSchema(t, NewOrganizationUserRoleResource()))
	expectDeprecated(t, "anyscale_cloud.auto_add_user", resourceAttr(t, resourceSchema(t, NewCloudResource()), "auto_add_user"))

	for _, tc := range []struct {
		name string
		ds   datasource.DataSource
		path string
	}{
		{"anyscale_cloud", NewCloudDataSource(), "auto_add_user"},
		{"anyscale_clouds", NewCloudsDataSource(), "clouds.auto_add_user"},
		{"anyscale_project", NewProjectDataSource(), "collaborators"},
		{"anyscale_organization_user", NewOrganizationUserDataSource(), "permission_level"},
		{"anyscale_organization_user", NewOrganizationUserDataSource(), "base_role"},
		{"anyscale_organization_user", NewOrganizationUserDataSource(), "additional_roles"},
		{"anyscale_organization_users", NewOrganizationUsersDataSource(), "users.permission_level"},
		{"anyscale_organization_users", NewOrganizationUsersDataSource(), "users.base_role"},
		{"anyscale_organization_users", NewOrganizationUsersDataSource(), "users.additional_roles"},
		{"anyscale_user", NewUserDataSource(), "organization_permission_level"},
	} {
		expectDeprecated(t, "data."+tc.name+"."+tc.path, dataSourceAttr(t, dataSourceSchema(t, tc.ds), tc.path))
	}
}

// Nothing outside the legacy RBAC set is deprecated by it: not the schema, and not any
// top-level attribute. lineage_tracking_enabled shares auto_add_user's backend helper but is
// not legacy RBAC.
func TestLegacyRBACDeprecation_Negative(t *testing.T) {
	notDeprecated := func(what string, d deprecatable) {
		t.Helper()
		if d.GetDeprecationMessage() != "" {
			t.Errorf("%s must not be deprecated, has DeprecationMessage %q", what, d.GetDeprecationMessage())
		}
	}
	cloud := resourceSchema(t, NewCloudResource())
	notDeprecated("anyscale_cloud", cloud)
	notDeprecated("anyscale_cloud.lineage_tracking_enabled", resourceAttr(t, cloud, "lineage_tracking_enabled"))

	for name, r := range map[string]resource.Resource{
		"anyscale_organization_user":       NewOrganizationUserResource(),
		"anyscale_organization_invitation": NewOrganizationInvitationResource(),
		"anyscale_user_group":              NewUserGroupResource(),
		"anyscale_user_group_members":      NewUserGroupMembersResource(),
	} {
		s := resourceSchema(t, r)
		notDeprecated(name, s)
		for attr, a := range s.Attributes {
			notDeprecated(name+"."+attr, a)
		}
	}
	for name, d := range map[string]datasource.DataSource{
		"anyscale_user_group":  NewUserGroupDataSource(),
		"anyscale_user_groups": NewUserGroupsDataSource(),
	} {
		s := dataSourceSchema(t, d)
		notDeprecated("data."+name, s)
		for attr, a := range s.Attributes {
			notDeprecated("data."+name+"."+attr, a)
		}
	}
}
