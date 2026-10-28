package provider

import (
	"strings"
	"testing"
)

func svcWith(goal string, primary, canary string) *ServiceResult {
	s := &ServiceResult{GoalState: goal, CurrentState: serviceStateRunning}
	if primary != "" {
		s.PrimaryVersion = &ServiceVersionResult{ID: primary, Version: "name-" + primary}
	}
	if canary != "" {
		s.CanaryVersion = &ServiceVersionResult{ID: canary, Version: "name-" + canary}
	}
	return s
}

func TestReconcileServiceVersionTracking(t *testing.T) {
	tests := []struct {
		name      string
		tracking  *serviceVersionTracking
		service   *ServiceResult
		wantID    string // "" means tracking returned unchanged
		wantDrift bool
	}{
		{"legacy state seeds silently", nil, svcWith("RUNNING", "v1", ""), "v1", false},
		{"legacy state mid-rollout waits", nil, svcWith("RUNNING", "v1", "v2"), "", false},
		{"steady on tracked version", &serviceVersionTracking{VersionID: "v1"}, svcWith("RUNNING", "v1", ""), "v1", false},
		{"out-of-band deploy", &serviceVersionTracking{VersionID: "v1"}, svcWith("RUNNING", "cli", ""), "cli", true},
		{"canary present is never drift", &serviceVersionTracking{VersionID: "v1"}, svcWith("RUNNING", "cli", "v2"), "", false},
		{"terminated is never drift", &serviceVersionTracking{VersionID: "v1"}, svcWith("TERMINATED", "cli", ""), "", false},
		{"no primary yet", &serviceVersionTracking{VersionID: "v1"}, svcWith("RUNNING", "", ""), "", false},
		{"pending won", &serviceVersionTracking{VersionID: "v1", Pending: true, PendingVersionID: "v2"}, svcWith("RUNNING", "v2", ""), "v2", false},
		{"pending rolled back", &serviceVersionTracking{VersionID: "v1", Pending: true, PendingVersionID: "v2"}, svcWith("RUNNING", "v1", ""), "v1", true},
		{"pending unidentified, primary changed", &serviceVersionTracking{VersionID: "v1", Pending: true}, svcWith("RUNNING", "v2", ""), "v2", false},
		{"pending unidentified, primary unchanged", &serviceVersionTracking{VersionID: "v1", Pending: true}, svcWith("RUNNING", "v1", ""), "v1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, drift := reconcileServiceVersionTracking(tt.tracking, tt.service)
			if drift != tt.wantDrift {
				t.Errorf("drift = %v, want %v", drift, tt.wantDrift)
			}
			if tt.wantID == "" {
				if next != tt.tracking {
					t.Errorf("tracking changed to %+v, want it unchanged", next)
				}
				return
			}
			if next == nil || next.VersionID != tt.wantID || next.Pending {
				t.Errorf("tracking = %+v, want settled on %q", next, tt.wantID)
			}
		})
	}
}

func TestServiceAppliedVersionID(t *testing.T) {
	tests := []struct {
		name    string
		applied *ServiceResult
		pre     string
		inPlace bool
		want    string
	}{
		{"update rollout: canary", svcWith("RUNNING", "v1", "v2"), "v1", false, "v2"},
		{"create/restart: new primary", svcWith("RUNNING", "v2", ""), "v1", false, "v2"},
		{"unidentified", svcWith("RUNNING", "v1", ""), "v1", false, ""},
		{"in place keeps the primary id", svcWith("RUNNING", "v1", ""), "v1", true, "v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceAppliedVersionID(tt.applied, tt.pre, tt.inPlace); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceRolloutRolledBack(t *testing.T) {
	tests := []struct {
		name     string
		final    *ServiceResult
		newID    string
		pre      string
		wantBack bool
	}{
		{"new version promoted", svcWith("RUNNING", "v2", ""), "v2", "v1", false},
		{"old version restored", svcWith("RUNNING", "v1", ""), "v2", "v1", true},
		{"unidentified, primary changed", svcWith("RUNNING", "v2", ""), "", "v1", false},
		{"unidentified, primary unchanged", svcWith("RUNNING", "v1", ""), "", "v1", true},
		{"unidentified, no prior primary", svcWith("RUNNING", "v1", ""), "", "", false},
		{"no primary reported", svcWith("RUNNING", "", ""), "v2", "v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceRolloutRolledBack(tt.final, tt.newID, tt.pre); got != tt.wantBack {
				t.Errorf("got %v, want %v", got, tt.wantBack)
			}
		})
	}
}

func TestServiceRolledBackDetail(t *testing.T) {
	o := newServiceRolloutObserver("v2")
	o.observe(svcWith("RUNNING", "v1", "v2"))
	final := svcWith("RUNNING", "v1", "")

	noReason := serviceRolledBackDetail("svc_1", o, final)
	for _, want := range []string{`"name-v2" (v2)`, `"name-v1" (v1)`, "anyscale service events --id svc_1"} {
		if !strings.Contains(noReason, want) {
			t.Errorf("detail missing %q:\n%s", want, noReason)
		}
	}

	// A checklist failure on the new version is the reason; one on another version is not.
	withChecklist := svcWith("RUNNING", "v1", "v2")
	other, mine := "v1", "v2"
	withChecklist.ServiceStatusChecklist = &ServiceStatusChecklistResult{PerVersion: []VersionChecklistResult{
		{VersionID: "v1", Items: []StatusChecklistItemResult{{Kind: "CLUSTER", State: "UNHEALTHY", Message: "unrelated", VersionID: &other}}},
		{VersionID: "v2", Items: []StatusChecklistItemResult{{Kind: "APPLICATION", State: "UNHEALTHY", Message: "ImportError", VersionID: &mine}}},
	}}
	o.observe(withChecklist)
	withReason := serviceRolledBackDetail("svc_1", o, final)
	if !strings.Contains(withReason, "Reason: APPLICATION: ImportError.") || strings.Contains(withReason, "unrelated") {
		t.Errorf("detail did not carry exactly the new version's checklist reason:\n%s", withReason)
	}

	// error_message wins over the checklist.
	msg := "deploy failed"
	withChecklist.ErrorMessage = &msg
	o.observe(withChecklist)
	if got := serviceRolledBackDetail("svc_1", o, final); !strings.Contains(got, "Reason: deploy failed.") {
		t.Errorf("error_message not preferred:\n%s", got)
	}
}

func TestServiceRolloutObserver_ObserveUntilRolledBack(t *testing.T) {
	o := newServiceRolloutObserver("v2")
	if o.observeUntilRolledBack(svcWith("RUNNING", "v1", "v2")) {
		t.Error("canary still present: not decided")
	}
	if o.observeUntilRolledBack(svcWith("RUNNING", "v2", "")) {
		t.Error("new version promoted: not a rollback")
	}
	rollingBack := svcWith("RUNNING", "v1", "")
	rollingBack.CurrentState = "ROLLING_BACK"
	if !o.observeUntilRolledBack(rollingBack) {
		t.Error("canary gone with the old version primary: decided as rolled back")
	}
	if newServiceRolloutObserver("").observeUntilRolledBack(rollingBack) {
		t.Error("unidentified new version: never decided early")
	}
}

func TestEvaluateServiceState_RunningWithCanaryKeepsWaiting(t *testing.T) {
	if done, _ := evaluateServiceState(svcWith("RUNNING", "v1", "v2"), serviceStateRunning); done {
		t.Error("RUNNING with a canary present must not settle the wait")
	}
	if done, err := evaluateServiceState(svcWith("RUNNING", "v2", ""), serviceStateRunning); !done || err != nil {
		t.Errorf("RUNNING without a canary must settle: done=%v err=%v", done, err)
	}
	terminated := svcWith("TERMINATED", "v1", "")
	terminated.CurrentState = "TERMINATING"
	if done, err := evaluateServiceState(terminated, serviceStateRunning); !done || err == nil {
		t.Errorf("goal_state TERMINATED while waiting for RUNNING must fail: done=%v err=%v", done, err)
	}
	if done, err := evaluateServiceState(terminated, serviceStateTerminated); done || err != nil {
		t.Errorf("TERMINATING while waiting for TERMINATED must keep polling: done=%v err=%v", done, err)
	}
}

// The restart sequence observed on a real backend: UNHEALTHY while the version starts, and still
// UNHEALTHY after the version and every checklist item are RUNNING (health-probe jailing).
func TestEvaluateServiceState_UnconfirmedErrorKeepsWaiting(t *testing.T) {
	s := svcWith("RUNNING", "v2", "")
	s.CurrentState = "UNHEALTHY"
	s.PrimaryVersion.CurrentState = "STARTING"
	if done, err := evaluateServiceState(s, serviceStateRunning); done || err != nil {
		t.Errorf("UNHEALTHY while the primary is still STARTING must keep polling: done=%v err=%v", done, err)
	}

	s.PrimaryVersion.CurrentState = "RUNNING"
	s.ServiceStatusChecklist = &ServiceStatusChecklistResult{PerVersion: []VersionChecklistResult{{
		VersionID: "v2",
		Items:     []StatusChecklistItemResult{{Kind: "TARGET_NODE_GROUP", State: "RUNNING"}},
	}}}
	if done, err := evaluateServiceState(s, serviceStateRunning); done || err != nil {
		t.Errorf("UNHEALTHY with every component RUNNING must keep polling: done=%v err=%v", done, err)
	}

	s.ServiceStatusChecklist.PerVersion[0].Items = append(s.ServiceStatusChecklist.PerVersion[0].Items,
		StatusChecklistItemResult{Kind: "APPLICATION", State: "UNHEALTHY", Message: "deploy failed"})
	if done, err := evaluateServiceState(s, serviceStateRunning); !done || err == nil {
		t.Errorf("a component in an error state must fail fast: done=%v err=%v", done, err)
	}

	s.ServiceStatusChecklist = nil
	s.PrimaryVersion = nil
	if done, err := evaluateServiceState(s, serviceStateRunning); !done || err == nil {
		t.Errorf("UNHEALTHY with no component reported must fail: done=%v err=%v", done, err)
	}
}
