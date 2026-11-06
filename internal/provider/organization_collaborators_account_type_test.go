package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// orgCollaboratorListIncludes reports whether GET /api/v2/organization_collaborators
// returns a record of the given account type for query q, with the precedence
// described on orgCollaboratorTypeAllAccounts. A mock that filters through it
// cannot find a service account for a lookup that forgets the account-type
// parameter, any more than the real API can.
func orgCollaboratorListIncludes(q url.Values, isServiceAccount bool) bool {
	switch q.Get(orgCollaboratorTypeParam) {
	case orgCollaboratorTypeAllAccounts:
		return true
	case "only_service_accounts":
		return isServiceAccount
	case "only_user_accounts":
		return !isServiceAccount
	}
	if v := q.Get("is_service_account"); v != "" {
		return (v == "true") == isServiceAccount
	}
	return !isServiceAccount
}

func TestOrgCollaboratorListIncludes_MatchesBackendPrecedence(t *testing.T) {
	for _, tc := range []struct {
		query                string
		user, serviceAccount bool
	}{
		{"", true, false},
		{"is_service_account=true", false, true},
		{"is_service_account=false", true, false},
		{"collaborator_type=all_accounts", true, true},
		{"collaborator_type=all_accounts&is_service_account=true", true, true},
		{"collaborator_type=only_user_accounts&is_service_account=true", true, false},
	} {
		q, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		if got := orgCollaboratorListIncludes(q, false); got != tc.user {
			t.Errorf("%q: user account included = %v, want %v", tc.query, got, tc.user)
		}
		if got := orgCollaboratorListIncludes(q, true); got != tc.serviceAccount {
			t.Errorf("%q: service account included = %v, want %v", tc.query, got, tc.serviceAccount)
		}
	}
}

// orgCollaboratorRecord is one row of the account-type mock below. Unlike the
// email-keyed fixtures elsewhere, records are a list, so a user and a service
// account can share an address.
type orgCollaboratorRecord struct {
	identityID       string
	userID           string
	email            string
	isServiceAccount bool
}

// newOrgCollaboratorAccountTypeMock serves the organization_collaborators list
// (filtered by account type and, like the backend's ilike, by email substring) and the
// singular GET keyed by user_id. It returns the client and every list query
// received.
func newOrgCollaboratorAccountTypeMock(t *testing.T, records ...orgCollaboratorRecord) (*Client, *[]url.Values) {
	t.Helper()
	var queries []url.Values
	toResult := func(rec orgCollaboratorRecord) map[string]any {
		return map[string]any{
			"id": rec.identityID, "user_id": rec.userID, "email": rec.email, "name": nil,
			"permission_level": "collaborator", "base_role": "collaborator", "additional_roles": []string{},
			"created_at": "2026-01-01T00:00:00.000000+00:00", "is_service_account": rec.isServiceAccount,
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const base = "/api/v2/organization_collaborators"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base:
			q := r.URL.Query()
			queries = append(queries, q)
			email := strings.ToLower(q.Get("email"))
			results := []map[string]any{}
			for _, rec := range records {
				if email != "" && !strings.Contains(strings.ToLower(rec.email), email) {
					continue
				}
				if !orgCollaboratorListIncludes(q, rec.isServiceAccount) {
					continue
				}
				results = append(results, toResult(rec))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": results, "metadata": map[string]any{"total": len(results), "next_paging_token": nil},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
			userID := strings.TrimPrefix(r.URL.Path, base+"/")
			for _, rec := range records {
				if rec.userID == userID {
					_ = json.NewEncoder(w).Encode(map[string]any{"result": toResult(rec)})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return NewClientWithToken(server.URL, "test-token"), &queries
}

var (
	accountTypeUser = orgCollaboratorRecord{identityID: "ide_human", userID: "usr_human", email: "human@example.com"}
	accountTypeSA   = orgCollaboratorRecord{
		identityID: "ide_robot", userID: "usr_robot", email: "robot@example.com", isServiceAccount: true,
	}
	// accountTypeSAShared is a service account sharing the user's address.
	accountTypeSAShared = orgCollaboratorRecord{
		identityID: "ide_shared_sa", userID: "usr_shared_sa", email: "human@example.com", isServiceAccount: true,
	}
)

// The by-email lookup behind anyscale_cloud_access, anyscale_organization_user_role
// and anyscale_organization_user resolves a service account, asking for every
// account type in the same request as the email.
func TestListOrgCollaboratorByEmail_AsksForAllAccountsInOneRequest(t *testing.T) {
	client, queries := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSA)

	identityID, userID, err := resolveIdentityForEmail(context.Background(), client, "Robot@Example.com")
	if err != nil {
		t.Fatalf("a service account must resolve by email: %v", err)
	}
	if identityID != "ide_robot" || userID != "usr_robot" {
		t.Errorf("got identity %q user %q, want ide_robot usr_robot", identityID, userID)
	}
	if len(*queries) != 1 {
		t.Fatalf("expected one list request, got %d", len(*queries))
	}
	q := (*queries)[0]
	if q.Get("email") != "Robot@Example.com" || q.Get(orgCollaboratorTypeParam) != orgCollaboratorTypeAllAccounts {
		t.Errorf("the lookup must send email and collaborator_type=all_accounts in one request, sent %v", q)
	}
}

func TestFindOrgCollaboratorByEmail_ResolvesServiceAccount(t *testing.T) {
	client, _ := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSA)

	c, err := findOrgCollaboratorByEmail(context.Background(), client, "robot@example.com")
	if err != nil {
		t.Fatalf("a service account must resolve by email: %v", err)
	}
	if c.ID != "ide_robot" {
		t.Errorf("resolved identity %q, want ide_robot", c.ID)
	}
}

func TestResolveIdentityForEmail_StillResolvesUser(t *testing.T) {
	client, _ := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSA)

	identityID, userID, err := resolveIdentityForEmail(context.Background(), client, "human@example.com")
	if err != nil {
		t.Fatalf("a user account must still resolve: %v", err)
	}
	if identityID != "ide_human" || userID != "usr_human" {
		t.Errorf("got identity %q user %q, want ide_human usr_human", identityID, userID)
	}
}

// One address held by a user and a service account cannot be resolved to
// either: the error names both records rather than taking the first.
func TestResolveIdentityForEmail_AmbiguousEmailNamesBothRecords(t *testing.T) {
	client, _ := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSAShared)

	_, _, err := resolveIdentityForEmail(context.Background(), client, "Human@Example.com")
	if err == nil {
		t.Fatal("an email matching a user and a service account must be an error, not first-wins")
	}
	for _, want := range []string{"ide_human", "usr_human", "ide_shared_sa", "usr_shared_sa", "human@example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the ambiguity error must name both records; missing %q in: %v", want, err)
		}
	}
}

// The resolver behind anyscale_iam_user_group_members lists the whole organization
// once; it must list service accounts too.
func TestResolveOrgUserIDsByEmail_ResolvesServiceAccount(t *testing.T) {
	client, queries := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSA)

	got, err := resolveOrgUserIDsByEmail(context.Background(), client, []string{"human@example.com", "ROBOT@example.com"})
	if err != nil {
		t.Fatalf("a service account member must resolve: %v", err)
	}
	if got["human@example.com"] != "usr_human" || got["ROBOT@example.com"] != "usr_robot" {
		t.Errorf("resolved %v, want human -> usr_human and ROBOT -> usr_robot", got)
	}
	for _, q := range *queries {
		if q.Get(orgCollaboratorTypeParam) != orgCollaboratorTypeAllAccounts {
			t.Errorf("every page must ask for all account types, sent %v", q)
		}
	}
}

func TestResolveOrgUserIDsByEmail_AmbiguousEmailNamesBothRecords(t *testing.T) {
	client, _ := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSAShared, accountTypeSA)

	_, err := resolveOrgUserIDsByEmail(context.Background(), client, []string{"human@example.com", "robot@example.com"})
	if err == nil {
		t.Fatal("an email matching a user and a service account must be an error, not resolved to either")
	}
	for _, want := range []string{"ide_human", "ide_shared_sa", "human@example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the ambiguity error must name both records; missing %q in: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "ide_robot") {
		t.Errorf("an unambiguous member must not be reported: %v", err)
	}
}

// runOrganizationUserDataSourceRead drives the singular data source's real Read.
func runOrganizationUserDataSourceRead(t *testing.T, client *Client, model OrganizationUserDataSourceModel) (OrganizationUserDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	d := &OrganizationUserDataSource{client: client}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatalf("failed to build config fixture: %v", diags)
	}
	readResp := &datasource.ReadResponse{State: state}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config(state)}, readResp)
	if readResp.Diagnostics.HasError() {
		return OrganizationUserDataSourceModel{}, readResp.Diagnostics
	}
	var out OrganizationUserDataSourceModel
	if diags := readResp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("failed to decode result state: %v", diags)
	}
	return out, readResp.Diagnostics
}

func organizationUserDataSourceConfig() OrganizationUserDataSourceModel {
	return OrganizationUserDataSourceModel{
		ID: types.StringNull(), UserID: types.StringNull(), Email: types.StringNull(),
		Name: types.StringNull(), PermissionLevel: types.StringNull(), CreatedAt: types.StringNull(),
		BaseRole: types.StringNull(), AdditionalRoles: types.ListNull(types.StringType),
	}
}

// data.anyscale_organization_user finds a service account by email, identity ID
// or user ID, and still finds a user.
func TestOrganizationUserDataSourceRead_FindsServiceAccount(t *testing.T) {
	client, _ := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSA)

	for _, tc := range []struct {
		name   string
		set    func(*OrganizationUserDataSourceModel)
		wantID string
	}{
		{"service account by email", func(m *OrganizationUserDataSourceModel) { m.Email = types.StringValue("Robot@Example.com") }, "ide_robot"},
		{"service account by identity ID", func(m *OrganizationUserDataSourceModel) { m.ID = types.StringValue("ide_robot") }, "ide_robot"},
		{"service account by user ID", func(m *OrganizationUserDataSourceModel) { m.UserID = types.StringValue("usr_robot") }, "ide_robot"},
		{"user by email", func(m *OrganizationUserDataSourceModel) { m.Email = types.StringValue("human@example.com") }, "ide_human"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := organizationUserDataSourceConfig()
			tc.set(&config)
			out, diags := runOrganizationUserDataSourceRead(t, client, config)
			if diags.HasError() {
				t.Fatalf("lookup failed: %v", diags)
			}
			if out.ID.ValueString() != tc.wantID {
				t.Errorf("found identity %q, want %q", out.ID.ValueString(), tc.wantID)
			}
		})
	}
}

func TestOrganizationUserDataSourceRead_AmbiguousEmailNamesBothRecords(t *testing.T) {
	client, _ := newOrgCollaboratorAccountTypeMock(t, accountTypeUser, accountTypeSAShared)

	config := organizationUserDataSourceConfig()
	config.Email = types.StringValue("human@example.com")
	_, diags := runOrganizationUserDataSourceRead(t, client, config)
	if !diags.HasError() {
		t.Fatal("an email matching a user and a service account must be an error, not first-wins")
	}
	var detail string
	for _, d := range diags.Errors() {
		detail += d.Detail()
	}
	for _, want := range []string{"ide_human", "ide_shared_sa"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the ambiguity error must name both records; missing %q in: %s", want, detail)
		}
	}
}
