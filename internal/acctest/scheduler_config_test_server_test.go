package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// schedulerConfigServer is a configurable mock of the /api/v2/scheduler and
// /api/v2/userinfo endpoints for anyscale_scheduler_config acceptance tests.
//
// This is a separate helper from mockSchedulerConfigServer (defined in
// resource_scheduler_config_advanced_config_acc_test.go, owned by the
// advanced_instance_config tests) rather than a shared rework of it: that
// helper is stable, already covers two committed criteria, and touching it
// would put two lanes' tests through the same file. Every knob here that the
// simpler helper does not need is a deliberate response to one of the
// remaining acceptance criteria - see each field's comment.
type schedulerConfigServer struct {
	mu sync.Mutex

	version    int64
	config     map[string]any
	readConfig string // literal JSON the GET returns under "config"; hand-written per test so Go's marshaller can't canonicalize it first.
	// readConfigAuto, when true, re-derives readConfig from the last applied
	// POST body (marshaled through encoding/json) after every apply, so a
	// multi-step test that updates its config sees the update reflected on
	// the next refresh without hand-authoring every intermediate shape. A
	// test that needs a specific wire shape (reordered keys, widened numeric
	// types, an omitted section) opts out by passing ReadConfig or calling
	// SetReadConfig, which locks the field to exactly what was given.
	readConfigAuto bool

	// Criterion 11: destroy must make no write call. Every handler on this
	// mock increments requests so a test can assert zero after Destroy.
	requests int

	// Criteria 19/20/21: force the validate or GET-config endpoint to answer
	// with a specific status/body instead of the default success path, so a
	// test can simulate "the server could not evaluate this" (5xx/timeout
	// stand-in) vs. "the server evaluated and rejected it" (400/422) vs. the
	// capability-gate 403.
	validateStatus int
	validateBody   string
	getStatus      int
	getBody        string

	// Criterion 17: the raw bytes of the last apply POST body, captured
	// before any decoding, so a test can assert an omitted section never
	// appears as a substring - decoding and re-encoding it back to a string
	// would silently repair the very omission being tested.
	lastApplyBodyRaw []byte

	// Criterion 12: the organization ID served on /api/v2/userinfo.
	orgID string
}

// schedulerConfigServerOpts configures a schedulerConfigServer at
// construction. Zero values mean "use the default success behavior."
type schedulerConfigServerOpts struct {
	ReadConfig     string
	OrgID          string
	ValidateStatus int
	ValidateBody   string
	GetStatus      int
	GetBody        string
	// InitialVersion seeds the server as if a config were already applied,
	// for tests (10, 21) that need Read/refresh behavior without an
	// intervening real Create in this test run.
	InitialVersion int64
}

func newSchedulerConfigServer(t *testing.T, opts schedulerConfigServerOpts) (*httptest.Server, *schedulerConfigServer) {
	t.Helper()

	orgID := opts.OrgID
	if orgID == "" {
		orgID = "org_mocktestorganizationid00"
	}

	s := &schedulerConfigServer{
		readConfig:     opts.ReadConfig,
		readConfigAuto: opts.ReadConfig == "",
		orgID:          orgID,
		validateStatus: opts.ValidateStatus,
		validateBody:   opts.ValidateBody,
		getStatus:      opts.GetStatus,
		getBody:        opts.GetBody,
		version:        opts.InitialVersion,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		id := s.orgID
		s.mu.Unlock()
		fmt.Fprintf(w, `{"result":{"organizations":[{"id":%q,"name":"Mock Org","public_identifier":"mock-org","default_cloud_id":null}]}}`, id)
	})

	mux.HandleFunc("/api/v2/scheduler/config/validate", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++

		if s.validateStatus != 0 {
			w.WriteHeader(s.validateStatus)
			if s.validateBody != "" {
				_, _ = w.Write([]byte(s.validateBody))
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v2/scheduler/config", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++

		switch r.Method {
		case http.MethodPost:
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read apply body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.lastApplyBodyRaw = raw

			var body struct {
				Config map[string]any `json:"config"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("undecodable apply body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.version++
			s.config = body.Config
			if s.readConfigAuto {
				// Re-derive what GET returns from what was actually posted, the
				// way a real backend would: an update-in-place test must see its
				// own second apply reflected on the next refresh without hand-
				// authoring every intermediate wire shape. A test that needs a
				// specific shape (reordered keys, widened numeric types, a
				// deliberately omitted section) opts out by passing ReadConfig or
				// calling SetReadConfig, which locks this field.
				if derived, err := json.Marshal(s.config); err == nil {
					s.readConfig = string(derived)
				}
			}
			fmt.Fprintf(w, `{"result":{"version":%d}}`, s.version)

		case http.MethodGet:
			if s.getStatus != 0 {
				w.WriteHeader(s.getStatus)
				if s.getBody != "" {
					_, _ = w.Write([]byte(s.getBody))
				}
				return
			}
			if s.version == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"detail":"No active scheduler config found."}}`))
				return
			}
			readConfig := s.readConfig
			if readConfig == "" {
				readConfig = "{}"
			}
			fmt.Fprintf(w, `{"result":{"version":%d,"is_active":true,"created_at":"2026-09-09T00:00:00Z","creator_id":"usr_mock","config":%s}}`, s.version, readConfig)

		default:
			t.Errorf("unexpected method %s on /api/v2/scheduler/config", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux), s
}

func (s *schedulerConfigServer) RequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *schedulerConfigServer) LastApplyBody() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastApplyBodyRaw
}

// SetValidateResponse changes what /config/validate answers for subsequent
// requests. Used mid-test (criteria 19/20) to flip the mock from "server
// cannot evaluate" back to "server rejects" without tearing down state.
func (s *schedulerConfigServer) SetValidateResponse(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validateStatus = status
	s.validateBody = body
}

// SetGetResponse changes what GET /config answers for subsequent requests.
// Used mid-test (criterion 21) to flip the mock between "refresh fails open"
// and "refresh hard-errors" against the same already-applied state.
func (s *schedulerConfigServer) SetGetResponse(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getStatus = status
	s.getBody = body
}

// SetReadConfig changes the literal "config" document GET returns on the
// success path, without touching the version counter or forcing an error
// status, and locks readConfig against further auto-derivation from applied
// bodies. Used by tests that need a specific wire shape GET must return
// regardless of what was last posted (reordering, numeric-form divergence, a
// deliberately omitted section).
func (s *schedulerConfigServer) SetReadConfig(readConfig string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readConfig = readConfig
	s.readConfigAuto = false
}
