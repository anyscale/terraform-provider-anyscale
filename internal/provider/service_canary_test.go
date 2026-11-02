package provider

import "testing"

func TestServiceHoldSettled(t *testing.T) {
	w := func(v int64) *int64 { return &v }
	canary := func(state string, current, target *int64) *ServiceResult {
		return &ServiceResult{CanaryVersion: &ServiceVersionResult{ID: "v2", CurrentState: state, CurrentWeight: current, TargetWeight: target}}
	}
	failing := canary("RUNNING", w(10), w(10))
	failing.ServiceStatusChecklist = &ServiceStatusChecklistResult{PerVersion: []VersionChecklistResult{
		{VersionID: "v2", Items: []StatusChecklistItemResult{{Kind: "APPLICATION", State: "STARTING"}}},
	}}
	tests := []struct {
		name    string
		service *ServiceResult
		percent int64
		want    bool
	}{
		{"settled", canary("RUNNING", w(10), w(10)), 10, true},
		// current_weight is null on the first polls after an apply (observed 0/null/0).
		{"null current_weight is not settled at 0", canary("RUNNING", nil, w(0)), 0, false},
		{"settled at 0", canary("RUNNING", w(0), w(0)), 0, true},
		{"still shifting", canary("RUNNING", w(5), w(10)), 10, false},
		{"target differs from plan", canary("RUNNING", w(25), w(25)), 10, false},
		{"canary not running", canary("STARTING", w(10), w(10)), 10, false},
		{"canary checklist row not running", failing, 10, false},
		{"no canary", &ServiceResult{}, 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceHoldSettled(tt.service, tt.percent); got != tt.want {
				t.Errorf("serviceHoldSettled = %t, want %t", got, tt.want)
			}
		})
	}
}
