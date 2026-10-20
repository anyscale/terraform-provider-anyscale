package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// apiStatusError builds the error DoRequestRaw returns for a backend
// AnyscaleHTTPException: {"error": {"detail": ...}}.
func apiStatusError(t *testing.T, status int, detail string) error {
	t.Helper()
	body, err := json.Marshal(map[string]any{"error": map[string]any{"detail": detail}})
	if err != nil {
		t.Fatal(err)
	}
	return &UnexpectedStatusError{StatusCode: status, Body: string(body)}
}

// The details below are the backend's own strings at product 3b1f86a; each
// classifier must match exactly one of them.
const (
	testConvertedDetail    = "This organization uses role bindings. Manage access in Users & IAM or with the role bindings API."
	testNotConvertedDetail = "Switch this organization to role bindings to change role bindings."
	testDuplicateDetail    = "The principal already holds this role on this resource."
	testArchivedDetail     = "This role is archived and cannot be newly assigned."
	testLockoutDetail      = "This change would leave the organization without an Organization owner."
	testConcurrentDetail   = "A role binding was revoked concurrently. Retry the request."
	testAutoAddUserDetail  = "Users cannot be removed from clouds which have auto add users enabled."
)

func TestOrgAccessModeClassifiers(t *testing.T) {
	classifiers := map[string]func(error) bool{
		"converted":     isOrgConvertedConflict,
		"not_converted": isOrgNotConvertedConflict,
		"duplicate": func(err error) bool {
			return isStatusWithSubstring(err, http.StatusConflict, roleBindingDuplicateSubstring)
		},
		"archived_role": func(err error) bool {
			return isStatusWithSubstring(err, http.StatusConflict, roleBindingArchivedRoleSubstring)
		},
		"lockout": func(err error) bool {
			return isStatusWithSubstring(err, http.StatusConflict, roleBindingLockoutSubstring)
		},
	}
	cases := []struct {
		name   string
		status int
		detail string
		want   string // the one classifier that must match; "" for none
	}{
		{"converted", http.StatusConflict, testConvertedDetail, "converted"},
		{"not converted", http.StatusConflict, testNotConvertedDetail, "not_converted"},
		{"duplicate", http.StatusConflict, testDuplicateDetail, "duplicate"},
		{"archived role", http.StatusConflict, testArchivedDetail, "archived_role"},
		{"lockout", http.StatusConflict, testLockoutDetail, "lockout"},
		{"concurrent revoke", http.StatusConflict, testConcurrentDetail, ""},
		{"auto add user", http.StatusConflict, testAutoAddUserDetail, ""},
		// The status must match as well as the text.
		{"converted text on a 403", http.StatusForbidden, testConvertedDetail, ""},
		{"duplicate text on a 400", http.StatusBadRequest, testDuplicateDetail, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := apiStatusError(t, tc.status, tc.detail)
			for name, classify := range classifiers {
				if got, want := classify(err), name == tc.want; got != want {
					t.Errorf("%s classifier = %v, want %v", name, got, want)
				}
			}
		})
	}
}

func TestOrgAccessModeClassifiersIgnoreNonStatusErrors(t *testing.T) {
	err := fmt.Errorf("transport: %s", testConvertedDetail)
	if isOrgConvertedConflict(err) {
		t.Error("a non-HTTP error carrying the converted text must not classify as converted")
	}
}

func TestOrgConvertedDetailNamesReplacement(t *testing.T) {
	detail := orgConvertedDetail(apiStatusError(t, http.StatusConflict, testConvertedDetail))
	for _, want := range []string{testConvertedDetail, "anyscale_role_binding", "user_group_id", "user_id", "#organization-access-mode"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not contain %q", detail, want)
		}
	}
}
