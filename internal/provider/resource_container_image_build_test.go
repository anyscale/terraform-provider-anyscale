package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestResolveContainerfile proves the real resolveContainerfile, not a hand-copy of its
// validation branch. The previous version of this test (TestContainerfileValidation) only ever
// simulated the "neither provided" branch inline; its "containerfile_path provided" case asserted
// wantError:false without ever reading a file, so the entire os.ReadFile branch -- including the
// wrapped read-error path -- had no real coverage at all.
func TestResolveContainerfile(t *testing.T) {
	r := &ContainerImageBuildResource{}
	const wantNeitherErr = "either containerfile or containerfile_path must be specified"

	t.Run("containerfile provided", func(t *testing.T) {
		plan := &ContainerImageBuildResourceModel{
			Containerfile:     types.StringValue("FROM anyscale/ray:2.9.0-py310\nRUN pip install requests"),
			ContainerfilePath: types.StringNull(),
		}
		got, err := r.resolveContainerfile(plan)
		if err != nil {
			t.Fatalf("resolveContainerfile() error = %v, want nil", err)
		}
		if got != plan.Containerfile.ValueString() {
			t.Errorf("resolveContainerfile() = %q, want %q", got, plan.Containerfile.ValueString())
		}
	})

	t.Run("containerfile_path provided reads the real file from disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "Containerfile")
		want := "FROM anyscale/ray:2.9.0-py310\nRUN pip install pandas\n"
		if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
			t.Fatalf("write fixture file: %v", err)
		}
		plan := &ContainerImageBuildResourceModel{
			Containerfile:     types.StringNull(),
			ContainerfilePath: types.StringValue(path),
		}
		got, err := r.resolveContainerfile(plan)
		if err != nil {
			t.Fatalf("resolveContainerfile() error = %v, want nil", err)
		}
		if got != want {
			t.Errorf("resolveContainerfile() = %q, want file content %q", got, want)
		}
	})

	t.Run("containerfile_path pointing at a nonexistent file surfaces a wrapped read error", func(t *testing.T) {
		plan := &ContainerImageBuildResourceModel{
			Containerfile:     types.StringNull(),
			ContainerfilePath: types.StringValue(filepath.Join(t.TempDir(), "does-not-exist")),
		}
		_, err := r.resolveContainerfile(plan)
		if err == nil {
			t.Fatal("resolveContainerfile() error = nil, want a file-read error")
		}
		if !strings.Contains(err.Error(), "failed to read containerfile from") {
			t.Errorf("resolveContainerfile() error = %q, want it to name the file that failed to read", err.Error())
		}
	})

	t.Run("neither provided", func(t *testing.T) {
		plan := &ContainerImageBuildResourceModel{
			Containerfile:     types.StringNull(),
			ContainerfilePath: types.StringNull(),
		}
		_, err := r.resolveContainerfile(plan)
		if err == nil || err.Error() != wantNeitherErr {
			t.Errorf("resolveContainerfile() error = %v, want %q", err, wantNeitherErr)
		}
	})

	t.Run("empty containerfile with no path falls through to the same neither-provided error", func(t *testing.T) {
		plan := &ContainerImageBuildResourceModel{
			Containerfile:     types.StringValue(""),
			ContainerfilePath: types.StringNull(),
		}
		_, err := r.resolveContainerfile(plan)
		if err == nil || err.Error() != wantNeitherErr {
			t.Errorf("resolveContainerfile() error = %v, want %q", err, wantNeitherErr)
		}
	})
}

// TestParseTimeout proves the real parseTimeout, not a hand-copy of it. The previous version
// (TestBuildTimeoutParsing) duplicated defaultBuildTimeout as a bare "30 * time.Minute" literal
// (silently drifts if the constant ever changes) and populated errorContains on two cases without
// ever asserting it -- both invalid-format cases would have passed even with an empty or wrong
// error message.
func TestParseTimeout(t *testing.T) {
	r := &ContainerImageBuildResource{}
	tests := []struct {
		name          string
		timeoutStr    string
		wantDuration  time.Duration
		wantError     bool
		errorContains string
	}{
		{name: "30 minutes", timeoutStr: "30m", wantDuration: 30 * time.Minute},
		{name: "1 hour", timeoutStr: "1h", wantDuration: 1 * time.Hour},
		{name: "45 minutes", timeoutStr: "45m", wantDuration: 45 * time.Minute},
		{name: "1 hour 30 minutes", timeoutStr: "1h30m", wantDuration: 90 * time.Minute},
		{name: "empty string uses the provider's real default constant", timeoutStr: "", wantDuration: defaultBuildTimeout},
		{name: "invalid format", timeoutStr: "invalid", wantError: true, errorContains: "invalid timeout format"},
		{name: "missing unit", timeoutStr: "30", wantError: true, errorContains: "invalid timeout format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			duration, err := r.parseTimeout(tt.timeoutStr)

			if tt.wantError {
				if err == nil {
					t.Fatalf("parseTimeout(%q) error = nil, want an error", tt.timeoutStr)
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("parseTimeout(%q) error = %q, want it to contain %q", tt.timeoutStr, err.Error(), tt.errorContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseTimeout(%q) error = %v, want nil", tt.timeoutStr, err)
			}
			if duration != tt.wantDuration {
				t.Errorf("parseTimeout(%q) = %v, want %v", tt.timeoutStr, duration, tt.wantDuration)
			}
		})
	}
}

// TestEvaluateBuildStatus_AllAcceptedStatuses proves evaluateBuildStatus — the pure classifier
// waitForBuild's polling loop now delegates to — correctly classifies every status the backend's
// BuildStatus/ClusterEnvironmentBuildStatus enums actually emit, by calling the REAL function
// rather than a hand-copied switch. That distinction is the whole point: the two tests this
// replaced (TestBuildStatusValues, TestBuildStatusTerminalCheck) each re-implemented the switch
// inline using the two-L "cancelled" spelling, so both passed even while the real waitForBuild
// switch only matched two-L and silently mis-handled the backend's actual one-L "canceled" value
// as "unknown build status" (F1).
func TestEvaluateBuildStatus_AllAcceptedStatuses(t *testing.T) {
	tests := []struct {
		name            string
		status          string
		errorMessage    *string
		wantDone        bool
		wantErr         bool
		wantErrContains string
		wantErrExcludes string
	}{
		{name: "pending is not done", status: "pending", wantDone: false, wantErr: false},
		{name: "in_progress is not done", status: "in_progress", wantDone: false, wantErr: false},
		{name: "pending_cancellation is not done", status: "pending_cancellation", wantDone: false, wantErr: false},
		{name: "succeeded is done with no error", status: "succeeded", wantDone: true, wantErr: false},
		{
			name:            "failed surfaces the build's error message",
			status:          "failed",
			errorMessage:    strPtr("dependency not found"),
			wantDone:        true,
			wantErr:         true,
			wantErrContains: "dependency not found",
		},
		{
			name:            "failed with no error message falls back to a generic message",
			status:          "failed",
			wantDone:        true,
			wantErr:         true,
			wantErrContains: "build failed",
		},
		{
			// Non-nil but empty must fall back the same as nil, not render a bare
			// trailing colon (was covered only by a hand-copied test, never the
			// real function — see TestBuildErrorMessageHandling's removal).
			name:            "failed with an empty (non-nil) error message also falls back to a generic message",
			status:          "failed",
			errorMessage:    strPtr(""),
			wantDone:        true,
			wantErr:         true,
			wantErrContains: "build failed",
			wantErrExcludes: "build failed:",
		},
		{
			// This is the F1 regression case: the backend's real wire value is one L.
			name:            "canceled (one L, the real backend spelling) is a clean terminal cancellation",
			status:          "canceled",
			wantDone:        true,
			wantErr:         true,
			wantErrContains: "cancelled",
			wantErrExcludes: "unknown build status",
		},
		{
			name:            "cancelled (two L, defensive) is also a clean terminal cancellation",
			status:          "cancelled",
			wantDone:        true,
			wantErr:         true,
			wantErrContains: "cancelled",
			wantErrExcludes: "unknown build status",
		},
		{
			name:            "an unrecognized status is a terminal error, not a silent hang",
			status:          "some_future_status_the_provider_does_not_know_about",
			wantDone:        true,
			wantErr:         true,
			wantErrContains: "unknown build status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			build := &BuildResult{
				ID:           "bld_test",
				Status:       tt.status,
				ErrorMessage: tt.errorMessage,
			}

			done, err := evaluateBuildStatus(build)

			if done != tt.wantDone {
				t.Errorf("evaluateBuildStatus(status=%q) done = %v, want %v", tt.status, done, tt.wantDone)
			}
			if tt.wantErr && err == nil {
				t.Fatalf("evaluateBuildStatus(status=%q) err = nil, want an error", tt.status)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("evaluateBuildStatus(status=%q) err = %v, want nil", tt.status, err)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("evaluateBuildStatus(status=%q) err = %q, want it to contain %q", tt.status, err.Error(), tt.wantErrContains)
			}
			if tt.wantErrExcludes != "" && strings.Contains(err.Error(), tt.wantErrExcludes) {
				t.Errorf("evaluateBuildStatus(status=%q) err = %q, must NOT contain %q — that is the exact F1 "+
					"regression signature of a real status falling through to the default case", tt.status, err.Error(), tt.wantErrExcludes)
			}
		})
	}
}

// TestWaitForBuildRealPath_TerminalStatuses is the end-to-end companion to
// TestEvaluateBuildStatus_AllAcceptedStatuses: it drives the REAL waitForBuild against a mock
// backend (not evaluateBuildStatus directly), proving the poll loop's HTTP plumbing — request
// method/path and response decoding — correctly reaches evaluateBuildStatus and returns its
// verdict, especially for a one-L "canceled" build. The two layers are deliberately not
// redundant: this one guards the wiring around evaluateBuildStatus, the other guards the
// classification logic itself (matches the three-test-layers-not-two lesson from prior review).
func TestWaitForBuildRealPath_TerminalStatuses(t *testing.T) {
	tests := []struct {
		name            string
		status          string
		errorMessage    *string
		wantErr         bool
		wantErrContains string
		wantErrExcludes string
	}{
		{
			name:            "canceled (one L) resolves to a clean cancelled error, not unknown status",
			status:          "canceled",
			wantErr:         true,
			wantErrContains: "cancelled",
			wantErrExcludes: "unknown build status",
		},
		{
			name:    "succeeded returns the build with no error",
			status:  "succeeded",
			wantErr: false,
		},
		{
			name:            "failed surfaces the build's error message",
			status:          "failed",
			errorMessage:    strPtr("dependency not found"),
			wantErr:         true,
			wantErrContains: "dependency not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(BuildResponse{
					Result: BuildResult{
						ID:           "bld_test",
						Status:       tt.status,
						ErrorMessage: tt.errorMessage,
					},
				})
			}))
			defer server.Close()

			r := &ContainerImageBuildResource{client: NewClientWithToken(server.URL, "test-token")}
			build, err := r.waitForBuild(context.Background(), "bld_test", 5*time.Second)

			if gotMethod != http.MethodGet {
				t.Errorf("request method = %q, want GET", gotMethod)
			}
			if gotPath != "/api/v2/builds/bld_test" {
				t.Errorf("request path = %q, want /api/v2/builds/bld_test", gotPath)
			}

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("waitForBuild() error = %v, want nil", err)
				}
				if build == nil {
					t.Fatal("waitForBuild() returned a nil build alongside a nil error")
				}
				return
			}

			if err == nil {
				t.Fatalf("waitForBuild() error = nil, want an error containing %q", tt.wantErrContains)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("waitForBuild() error = %q, want it to contain %q", err.Error(), tt.wantErrContains)
			}
			if tt.wantErrExcludes != "" && strings.Contains(err.Error(), tt.wantErrExcludes) {
				t.Errorf("waitForBuild() error = %q, must NOT contain %q — this is F1: a real cancelled build "+
					"falling through to the default case instead of a clean cancellation error", err.Error(), tt.wantErrExcludes)
			}
		})
	}
}

// TestContainerImageBuildModelMapping tests mapping of API response to model
func TestContainerImageBuildModelMapping(t *testing.T) {
	// Simulate API responses: an application template plus its build (contract-based,
	// via GET /api/v2/builds/{id} - not a separate list call).
	templateResult := ApplicationTemplateResult{
		ID:        "apptemp_123",
		Name:      "my-custom-image",
		CreatorID: "user_456",
		CreatedAt: "2024-01-01T00:00:00Z",
	}

	buildResult := BuildResult{
		ID:                    "bld_789",
		ApplicationTemplateID: "apptemp_123",
		Status:                "succeeded",
		RayVersion:            strPtr("2.9.0"),
		DockerImageName:       strPtr("anyscale/my-custom-image:v1"),
		CreatedAt:             "2024-01-01T00:00:00Z",
		Revision:              3,
	}

	// Map to model
	model := ContainerImageBuildResourceModel{
		ID:          types.StringValue(templateResult.ID),
		Name:        types.StringValue(templateResult.Name),
		BuildID:     types.StringValue(buildResult.ID),
		BuildStatus: types.StringValue(buildResult.Status),
		CreatedAt:   types.StringValue(buildResult.CreatedAt),
		Revision:    types.Int64Value(int64(buildResult.Revision)),
		NameVersion: types.StringValue(templateResult.Name + ":" + "3"),
	}

	if buildResult.DockerImageName != nil {
		model.ImageURI = types.StringValue(*buildResult.DockerImageName)
	}

	if buildResult.RayVersion != nil {
		model.RayVersion = types.StringValue(*buildResult.RayVersion)
	}

	// Verify mapping
	if model.ID.ValueString() != "apptemp_123" {
		t.Errorf("ID = %v, want 'apptemp_123'", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-custom-image" {
		t.Errorf("Name = %v, want 'my-custom-image'", model.Name.ValueString())
	}
	if model.BuildID.ValueString() != "bld_789" {
		t.Errorf("BuildID = %v, want 'bld_789'", model.BuildID.ValueString())
	}
	if model.BuildStatus.ValueString() != "succeeded" {
		t.Errorf("BuildStatus = %v, want 'succeeded'", model.BuildStatus.ValueString())
	}
	if model.ImageURI.ValueString() != "anyscale/my-custom-image:v1" {
		t.Errorf("ImageURI = %v, want 'anyscale/my-custom-image:v1'", model.ImageURI.ValueString())
	}
	if model.RayVersion.ValueString() != "2.9.0" {
		t.Errorf("RayVersion = %v, want '2.9.0'", model.RayVersion.ValueString())
	}
	if model.Revision.ValueInt64() != 3 {
		t.Errorf("Revision = %v, want 3", model.Revision.ValueInt64())
	}
	if model.NameVersion.ValueString() != "my-custom-image:3" {
		t.Errorf("NameVersion = %v, want 'my-custom-image:3'", model.NameVersion.ValueString())
	}
}

// TestCreateApplicationTemplateRequestStructure tests the structure of the
// application template create request (POST /api/v2/application_templates/,
// call 1 of the containerfile-build 2-call sequence).
func TestCreateApplicationTemplateRequestStructure(t *testing.T) {
	projectID := "prj_123"

	req := CreateApplicationTemplateRequest{
		Name:          "test-image",
		Containerfile: "FROM anyscale/ray:2.9.0-py310\nRUN pip install requests",
		ProjectID:     &projectID,
	}

	if req.Name != "test-image" {
		t.Errorf("name = %v, want 'test-image'", req.Name)
	}
	if req.Containerfile != "FROM anyscale/ray:2.9.0-py310\nRUN pip install requests" {
		t.Errorf("containerfile mismatch")
	}
	if req.ProjectID == nil || *req.ProjectID != "prj_123" {
		t.Errorf("project_id = %v, want 'prj_123'", req.ProjectID)
	}
}

// TestNullableFieldHandling tests handling of nullable fields in build response
func TestNullableFieldHandling(t *testing.T) {
	// Build without optional fields
	build := BuildResult{
		ID:                    "bld_123",
		ApplicationTemplateID: "apptemp_456",
		Status:                "succeeded",
		CreatedAt:             "2024-01-01T00:00:00Z",
		// Optional fields are nil
		RayVersion:      nil,
		DockerImageName: nil,
		ErrorMessage:    nil,
	}

	// Map to model - should handle nil values
	model := ContainerImageBuildResourceModel{
		ID:          types.StringValue(build.ApplicationTemplateID),
		BuildID:     types.StringValue(build.ID),
		BuildStatus: types.StringValue(build.Status),
		CreatedAt:   types.StringValue(build.CreatedAt),
	}

	if build.DockerImageName != nil {
		model.ImageURI = types.StringValue(*build.DockerImageName)
	} else {
		model.ImageURI = types.StringNull()
	}

	if build.RayVersion != nil {
		model.RayVersion = types.StringValue(*build.RayVersion)
	} else {
		model.RayVersion = types.StringNull()
	}

	// Verify nullable fields are properly set to null
	if !model.ImageURI.IsNull() {
		t.Error("ImageURI should be null when DockerImageName is nil")
	}
	if !model.RayVersion.IsNull() {
		t.Error("RayVersion should be null when RayVersion is nil")
	}
}

// TestNameVersionFormatting tests the name_version field formatting
func TestNameVersionFormatting(t *testing.T) {
	tests := []struct {
		name            string
		imageName       string
		revision        int
		wantNameVersion string
	}{
		{
			name:            "basic formatting",
			imageName:       "my-image",
			revision:        1,
			wantNameVersion: "my-image:1",
		},
		{
			name:            "higher revision",
			imageName:       "production-image",
			revision:        42,
			wantNameVersion: "production-image:42",
		},
		{
			name:            "revision zero",
			imageName:       "new-image",
			revision:        0,
			wantNameVersion: "new-image:0",
		},
		{
			name:            "image name with hyphens",
			imageName:       "my-custom-ray-image",
			revision:        5,
			wantNameVersion: "my-custom-ray-image:5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the name_version formatting logic
			nameVersion := tt.imageName + ":" + fmt.Sprintf("%d", tt.revision)

			if nameVersion != tt.wantNameVersion {
				t.Errorf("name_version = %v, want %v", nameVersion, tt.wantNameVersion)
			}
		})
	}
}

// Helper function for creating string pointers
func strPtr(s string) *string {
	return &s
}
