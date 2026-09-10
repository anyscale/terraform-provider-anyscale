package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Transport-layer tests for the Anyscale Scheduler API.
//
// Every fixture body below is the real wire shape observed against the live
// API or produced verbatim by the backend's own response models - the envelope
// differences in particular ({"result": {...}} for config/apply vs
// {"results": [...], "metadata": {...}} for the version list) are exactly what
// a hand-invented fixture would smooth over, and a fixture that smooths them
// over would pass against a parser that ignores them.

func schedulerTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}
}

// TestGetActiveSchedulerConfigNotFound is the regression guard for this repo's
// known 404-swallowing bug class: DoRequestAndParse returns a non-nil pointer
// to a ZERO-VALUED struct when 404 is passed as an accepted status, which is
// byte-for-byte indistinguishable from a real, empty config document. The
// distinction matters because "no config has ever been applied" and "a config
// exists but declares nothing" are different states of the world for Read.
//
// Mutation check: adding http.StatusNotFound to getActiveSchedulerConfig's
// accepted statuses makes this test fail with a nil error and a non-nil
// response - i.e. it detects precisely the mistake it exists to prevent.
func TestGetActiveSchedulerConfigNotFound(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		// The real body, not an empty one: the endpoint answers 404 with a
		// populated error envelope when nothing has been applied.
		_, _ = w.Write([]byte(`{"error":{"detail":"No active scheduler config found."}}`))
	}))

	resp, err := getActiveSchedulerConfig(context.Background(), client)
	if err == nil {
		t.Fatal("expected an error for a 404, got nil - a swallowed 404 makes 'never applied' look like an empty config")
	}
	if !errors.Is(err, ErrSchedulerConfigNotFound) {
		t.Fatalf("expected ErrSchedulerConfigNotFound, got %v", err)
	}
	if resp != nil {
		t.Fatalf("expected a nil response alongside the not-found error, got %+v", resp)
	}
}

func TestGetActiveSchedulerConfigParsesResultEnvelope(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != schedulerConfigPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"result":{"version":7,"is_active":true,"created_at":"2026-09-01T00:00:00Z","creator_id":"usr_1","config":{"resource_queues":[{"name":"default","cohort_name":"c1","resource_groups":[{"covered_resources":["cpu","memory_gb"],"flavors":[{"name":"std","resources":[{"name":"cpu","nominal_quota":0}]}]}]}]}}}`))
	}))

	resp, err := getActiveSchedulerConfig(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result.Version != 7 || !resp.Result.IsActive {
		t.Fatalf("version/is_active not parsed: %+v", resp.Result)
	}
	queues := resp.Result.Config.ResourceQueues
	if len(queues) != 1 || queues[0].Name != "default" {
		t.Fatalf("resource_queues not parsed: %+v", queues)
	}
	if queues[0].CohortName == nil || *queues[0].CohortName != "c1" {
		t.Fatalf("cohort_name not parsed: %+v", queues[0].CohortName)
	}
	// An explicit 0 quota must survive as a pointer to 0, not collapse to nil.
	// Unset means unlimited and 0 means blocked, so collapsing the two inverts
	// the instruction this field carries.
	q := queues[0].ResourceGroups[0].Flavors[0].Resources[0]
	if q.NominalQuota == nil {
		t.Fatal("explicit nominal_quota 0 was parsed as unset (nil); unset means unlimited, 0 means blocked")
	}
	if *q.NominalQuota != 0 {
		t.Fatalf("nominal_quota = %v, want 0", *q.NominalQuota)
	}
}

// TestApplySchedulerConfigOmitsUnsetFields proves the encoder does not send
// zero values for unset optionals. Sending "nominal_quota": 0 for a field the
// practitioner never wrote would silently block a resource the backend would
// otherwise treat as unlimited.
func TestApplySchedulerConfigOmitsUnsetFields(t *testing.T) {
	var captured string
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		captured = string(buf)
		_, _ = w.Write([]byte(`{"result":{"version":3}}`))
	}))

	version, err := applySchedulerConfig(context.Background(), client, SchedulerConfig{
		ResourceQueues: []SchedulerResourceQueue{{
			Name: "default",
			ResourceGroups: []SchedulerResourceGroup{{
				CoveredResources: []string{"cpu"},
				Flavors:          []SchedulerFlavorQuota{{Name: "std", Resources: []SchedulerResourceQuotaSpec{{Name: "cpu"}}}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != 3 {
		t.Fatalf("version = %d, want 3", version)
	}
	for _, unwanted := range []string{"nominal_quota", "lending_limit", "borrowing_limit", "cohort_name", "recycle_policy", "scheduling_rules", "resource_flavors"} {
		if strings.Contains(captured, unwanted) {
			t.Errorf("request body sent unset field %q: %s", unwanted, captured)
		}
	}
	if !strings.Contains(captured, `"config"`) {
		t.Errorf("request body missing the config wrapper: %s", captured)
	}
}

// TestTranslateSchedulerAPIErrorAdmissionFlag covers the flag-off 403. The
// backend's own wording uses the surface's former internal name, which appears
// nowhere in this provider or in Anyscale's public documentation, so passing it
// through unchanged tells a practitioner nothing they can act on.
//
// Both bodies below must classify: the one upstream sends today, and the one
// it will plausibly send once the rename reaches its own error string. The
// second case is the load-bearing one - classification drives both fail-open
// paths, so a miss turns every plan into a hard workspace-wide failure, and
// the change that caused it would be a cosmetic reword upstream that nothing
// here would otherwise flag.
func TestTranslateSchedulerAPIErrorAdmissionFlag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detail string
	}{
		{"current wording", "GRS is not enabled for this organization."},
		{"post-rename wording", "Anyscale Scheduler is not enabled for this organization."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, tc.detail)
			}))

			_, err := getActiveSchedulerConfig(context.Background(), client)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrSchedulerNotEnabled) {
				t.Fatalf("403 from the admission gate was not classified: %v", err)
			}
			if !strings.Contains(err.Error(), "Anyscale Scheduler is not enabled") {
				t.Errorf("diagnostic does not name the product: %v", err)
			}
			if strings.Contains(err.Error(), "GRS") {
				t.Errorf("diagnostic leaks the backend's internal name: %v", err)
			}
		})
	}
}

// A 403 that is NOT the admission gate must stay a plain error. Classifying on
// status alone would relabel every permission failure on these endpoints as
// "the scheduler is not enabled", sending the reader after the wrong problem.
func TestTranslateSchedulerAPIErrorOther403(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"detail":"User does not have permission to update scheduler configs."}}`))
	}))

	_, err := getActiveSchedulerConfig(context.Background(), client)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrSchedulerNotEnabled) {
		t.Fatalf("an unrelated 403 was misclassified as the admission gate: %v", err)
	}
	if !strings.Contains(err.Error(), "does not have permission") {
		t.Errorf("backend detail was lost: %v", err)
	}
}

// A 422 carries FastAPI's structured field errors. Flattening them to
// "path: message" is the difference between a practitioner seeing which field
// is wrong and seeing a raw JSON dump.
func TestTranslateSchedulerAPIError422(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":[{"loc":["body","config","resource_queues",0,"name"],"msg":"Resource queue name must be non-empty","type":"value_error"}]}`))
	}))

	err := validateSchedulerConfig(context.Background(), client, SchedulerConfig{})
	if err == nil {
		t.Fatal("expected an error")
	}
	want := "config.resource_queues.0.name: Resource queue name must be non-empty"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

// The cross-reference errors are a 400 with a plain sentence, not the 422
// shape. No Terraform schema validator can catch this class, however precisely
// the document tree is typed, because it depends on the rest of the document -
// which is the whole argument for calling the validate endpoint at all.
func TestTranslateSchedulerAPIError400CrossReference(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"detail":"Scheduling rule #1 references unknown resource queue 'gpu-queue'."}}`))
	}))

	err := validateSchedulerConfig(context.Background(), client, SchedulerConfig{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "Scheduling rule #1 references unknown resource queue 'gpu-queue'." {
		t.Fatalf("backend sentence not surfaced verbatim: %q", err.Error())
	}
}

// The classifier that lets ModifyPlan tell "the server read this and rejected
// it" apart from "the check never ran". Both halves are asserted here: a 400
// and a 422 must NOT be marked unavailable (they are real rejections and must
// keep failing the plan), and a 503 must be, so the plan warns and proceeds
// instead of blocking on an outage.
//
// Mutation check: making schedulerServerEvaluatedDocument return false
// unconditionally flips the 400/422 subtests red; returning true
// unconditionally flips the 503 subtest red.
func TestValidateSchedulerConfigDistinguishesRejectionFromUnavailability(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		unavailable bool
	}{
		{"422 field error", http.StatusUnprocessableEntity, `{"detail":[{"loc":["body","config"],"msg":"bad","type":"value_error"}]}`, false},
		{"400 cross reference", http.StatusBadRequest, `{"error":{"detail":"Scheduling rule #1 references unknown resource queue 'q'."}}`, false},
		{"503 outage", http.StatusServiceUnavailable, `{"error":{"detail":"Service Unavailable"}}`, true},
		{"401 expired token", http.StatusUnauthorized, `{"error":{"detail":"Invalid token."}}`, true},
		{"403 capability gate", http.StatusForbidden, `{"error":{"detail":"GRS is not enabled for this organization."}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))

			err := validateSchedulerConfig(context.Background(), client, SchedulerConfig{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrSchedulerValidationUnavailable); got != tc.unavailable {
				t.Fatalf("unavailable = %v, want %v (err: %v)", got, tc.unavailable, err)
			}
			// The wrapper must not prefix the message it carries - the
			// diagnostic renders it verbatim, and the 403's translated text in
			// particular is asserted elsewhere to omit the backend's internal
			// name.
			if strings.Contains(err.Error(), "could not be performed") {
				t.Errorf("wrapper leaked its sentinel text into the message: %v", err)
			}
		})
	}
}

func TestValidateSchedulerConfigAccepts204(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != schedulerConfigValidatePath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := validateSchedulerConfig(context.Background(), client, SchedulerConfig{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The version list uses the results/metadata envelope, unlike every other
// model in this provider. A fixture using {"result": {...}} here would
// unmarshal cleanly into an empty slice and look exactly like "no versions",
// so this test uses the real envelope and asserts a non-empty result.
func TestListSchedulerConfigVersionsEnvelope(t *testing.T) {
	client := schedulerTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"version":2,"created_at":"2026-09-02T00:00:00Z","creator_id":"usr_2"},{"version":1,"created_at":"2026-09-01T00:00:00Z","creator_id":"usr_1"}],"metadata":{"total":2,"next_paging_token":null}}`))
	}))

	versions, err := listSchedulerConfigVersions(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2 - a result/results envelope mismatch parses as empty", len(versions))
	}
	if versions[0].Version != 2 || versions[0].CreatorID != "usr_2" {
		t.Fatalf("first version not parsed: %+v", versions[0])
	}
}
