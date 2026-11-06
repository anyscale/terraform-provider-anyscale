package provider

import "testing"

func TestParseRoleBindingNaturalKey(t *testing.T) {
	valid := map[string]roleBindingNaturalKey{
		"cloud/cld_1/user_group/ug_1/rol_1": {ResourceType: "cloud", ResourceID: "cld_1", PrincipalType: "user_group", PrincipalID: "ug_1", RoleID: "rol_1"},
		"project/prj_1/user/usr_1/rol_2":    {ResourceType: "project", ResourceID: "prj_1", PrincipalType: "user", PrincipalID: "usr_1", RoleID: "rol_2"},
		"organization/org_1/user/usr_1/rol_3": {
			ResourceType: "organization", ResourceID: "org_1", PrincipalType: "user", PrincipalID: "usr_1", RoleID: "rol_3",
		},
	}
	for id, want := range valid {
		got, err := parseRoleBindingNaturalKey(id)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", id, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %+v, want %+v", id, got, want)
		}
	}

	invalid := []string{
		"",
		"cloud/cld_1/user_group/ug_1", // 4 parts
		"cloud/cld_1/user_group/ug_1/rol_1/extra", // 6 parts
		"cloud//user_group/ug_1/rol_1",            // empty scope id
		"cloud/cld_1/user_group/ug_1/",            // empty role id
		"workspace/wsp_1/user/usr_1/rol_1",        // unknown scope type
		"cloud/cld_1/service_account/usr_1/rol_1", // unknown principal type
		"Cloud/cld_1/user/usr_1/rol_1",            // enums are case-sensitive
	}
	for _, id := range invalid {
		if _, err := parseRoleBindingNaturalKey(id); err == nil {
			t.Errorf("%q: expected an error", id)
		}
	}
}
