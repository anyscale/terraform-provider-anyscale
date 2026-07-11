package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDoRequestAndParse_404ToleranceSignal is exploratory verification for workbench #7,
// Cluster 1: getInvitationByID currently detects "not found" via an early, pre-body-read check
// on httpResp.StatusCode == http.StatusNotFound, returning a distinct error before ever parsing
// the response body. Migrating it to DoRequestAndParse (like compute_config's GET-with-404-
// tolerance) would fold 404 into the accepted-statuses list instead. This test proves,
// empirically rather than by assumption, exactly what a caller sees in that case: does err come
// back nil or non-nil, and is the parsed result's zero-value distinguishable as "not found"?
func TestDoRequestAndParse_404ToleranceSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail": "Invitation not found"}`))
	}))
	defer server.Close()

	client := NewClientWithToken(server.URL, "test-token")
	ctx := context.Background()

	result, err := DoRequestAndParse[OrganizationInvitationResponse](
		ctx, client, "GET", "/api/v2/organization_invitations/does-not-exist", nil,
		http.StatusOK, http.StatusNotFound,
	)

	// Empirical finding: a 404 in the accepted-statuses list does NOT produce an error - the
	// body ({"detail": "..."}) has no "result" key, so json.Unmarshal succeeds with Result left
	// at its zero value. err is nil and result is a non-nil pointer to a zero-valued struct.
	if err != nil {
		t.Fatalf("DoRequestAndParse() with 404 in accepted statuses returned an error = %v; expected nil (404 body unmarshals cleanly into a zero-valued Result)", err)
	}
	if result == nil {
		t.Fatal("DoRequestAndParse() with 404 in accepted statuses returned a nil result; expected a non-nil pointer to a zero-valued struct")
	}
	if result.Result.ID != "" {
		t.Errorf("DoRequestAndParse() Result.ID = %q on a 404, want empty (zero-value) - this IS the not-found signal a caller must check explicitly", result.Result.ID)
	}

	// Conclusion for the migration: the not-found signal survives, but as result.Result.ID == ""
	// with err == nil, NOT as a distinct error the way the current early-404-check works. A
	// straight swap to DoRequestAndParse without adding an explicit
	// "if result.Result.ID == "" { return nil, notFoundErr }" check after the call would silently
	// change getInvitationByID's contract: callers checking `err != nil` for not-found (as the
	// current code and its callers do) would stop detecting it at all.
}
