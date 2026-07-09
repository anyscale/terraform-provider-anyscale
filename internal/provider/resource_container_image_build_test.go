package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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

// This file previously held four more tests (TestContainerImageBuildModelMapping,
// TestCreateApplicationTemplateRequestStructure, TestNullableFieldHandling,
// TestNameVersionFormatting) that built ApplicationTemplateResult/BuildResult/
// CreateApplicationTemplateRequest literals and either hand-copied Create()'s/
// Read()'s field-mapping and nil-check branches inline, or (TestNameVersionFormatting)
// bare re-implemented the one-line fmt.Sprintf("%s:%d", ...) expression those methods
// use -- none of them called the resource's real Create() or Read(), so none could
// catch a regression in either.
//
// TestContainerImageBuildCreate_MapsFieldsFromThreeCallSequence below replaces all
// four. It drives the resource's real Create() directly as a plain Go call (not
// through resource.Test/terraform apply) because ContainerImageBuildResource.client
// is unexported, so a test that constructs the resource directly against a mock
// server must live in package provider rather than internal/acctest -- the same
// constraint and pattern as resource_container_image_registry_test.go and its
// orphan-prevention neighbor.

// TestContainerImageBuildCreate_MapsFieldsFromThreeCallSequence drives the real
// Create() against a mock server that implements all three calls in its real
// sequence: POST /api/v2/application_templates/ (creates the template and
// triggers a build), GET /api/v2/application_templates/{id} (getLatestBuildID's
// re-fetch -- must carry a populated LatestBuild since the bare create response
// never does, and getLatestBuildID reads template.LatestBuild.ID off exactly this
// response), and GET /api/v2/builds/{id} (waitForBuild/getBuild -- returns status
// "succeeded" immediately so the poll loop exits on its first iteration, since
// evaluateBuildStatus's polling behavior itself is already covered by
// TestWaitForBuildRealPath_TerminalStatuses above).
//
// It captures the real request body Create() sends on call 1, salvaging
// TestCreateApplicationTemplateRequestStructure's genuine intent against what
// Create() actually puts on the wire instead of a hand-built literal, and
// table-drives over the build response's nullable fields (RayVersion,
// DockerImageName, Digest) being present vs. absent, salvaging
// TestNullableFieldHandling's genuine intent against Create()'s real nil-check
// branches. The final NameVersion assertion salvages TestNameVersionFormatting's
// intent against the real fmt.Sprintf("%s:%d", ...) call in Create(), which is not
// factored into a standalone helper in production code.
func TestContainerImageBuildCreate_MapsFieldsFromThreeCallSequence(t *testing.T) {
	tests := []struct {
		name            string
		rayVersion      *string
		dockerImageName *string
		digest          *string
	}{
		{
			name:            "with all optional fields populated",
			rayVersion:      strPtr("2.9.0"),
			dockerImageName: strPtr("anyscale/my-custom-image:v1"),
			digest:          strPtr("sha256:buildtestdigest000000000000000000000000000000000000000000000"),
		},
		{
			name:            "nullable fields absent",
			rayVersion:      nil,
			dockerImageName: nil,
			// Deliberately non-nil, unlike rayVersion/dockerImageName above: a
			// permanently-nil digest now drives Create()'s waitForBuildDigest into
			// its real 30s poll-then-give-up path (the GET handler below is static,
			// so it would echo nil on every poll). That timeout path is already
			// covered fast in container_image_helpers_test.go; this case is about
			// nullable-field mapping, not digest-settle timing, so give it a
			// pre-settled digest to keep this test on its fast path.
			digest: strPtr("sha256:buildtestdigestnullablecase00000000000000000000000000"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const (
				templateID           = "apptemp_create_test_123"
				buildID              = "bld_create_test_789"
				resourceName         = "my-custom-image"
				projectID            = "prj_create_test"
				containerfileContent = "FROM anyscale/ray:2.9.0-py310\nRUN pip install requests"
				revision             = 3
				createdAt            = "2024-01-01T00:00:00Z"
			)

			var templateReq CreateApplicationTemplateRequest

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/application_templates/":
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("failed to read request body: %v", err)
					}
					if err := json.Unmarshal(body, &templateReq); err != nil {
						t.Fatalf("failed to decode template request: %v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					// Call 1's response is deliberately bare (no LatestBuild) -- this is
					// the real API contract Create() relies on to justify the separate
					// getLatestBuildID re-fetch below.
					_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
						Result: ApplicationTemplateResult{
							ID:        templateID,
							Name:      resourceName,
							CreatorID: "user_1",
							CreatedAt: createdAt,
						},
					})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v2/application_templates/"+templateID:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					// This re-fetch must carry LatestBuild -- getLatestBuildID reads
					// template.LatestBuild.ID directly off this response.
					_ = json.NewEncoder(w).Encode(ApplicationTemplateResponse{
						Result: ApplicationTemplateResult{
							ID:          templateID,
							Name:        resourceName,
							CreatorID:   "user_1",
							CreatedAt:   createdAt,
							LatestBuild: &MiniBuildResult{ID: buildID, Revision: revision, Status: "succeeded"},
						},
					})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v2/builds/"+buildID:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					// Status "succeeded" makes waitForBuild's poll loop return on its
					// first iteration.
					_ = json.NewEncoder(w).Encode(BuildResponse{
						Result: BuildResult{
							ID:                    buildID,
							ApplicationTemplateID: templateID,
							Status:                "succeeded",
							RayVersion:            tt.rayVersion,
							DockerImageName:       tt.dockerImageName,
							Revision:              revision,
							CreatorID:             "user_1",
							CreatedAt:             createdAt,
							LastModifiedAt:        createdAt,
							Digest:                tt.digest,
						},
					})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			r := &ContainerImageBuildResource{client: NewClientWithToken(server.URL, "fake-token-build-create-test")}
			ctx := context.Background()

			var schemaResp resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
			if schemaResp.Diagnostics.HasError() {
				t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
			}

			plan := tfsdk.Plan{Schema: schemaResp.Schema}
			planDiags := plan.Set(ctx, &ContainerImageBuildResourceModel{
				Name:          types.StringValue(resourceName),
				Containerfile: types.StringValue(containerfileContent),
				ProjectID:     types.StringValue(projectID),
				BuildTimeout:  types.StringValue("30m"),
			})
			if planDiags.HasError() {
				t.Fatalf("failed to build plan: %v", planDiags)
			}

			createResp := &resource.CreateResponse{
				// The real runtime pre-populates CreateResponse.State from CreateRequest.Plan.
				State: tfsdk.State(plan),
			}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)

			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create reported an unexpected error: %v", createResp.Diagnostics)
			}

			// Real request-body assertions against what Create() actually sent on call 1.
			if templateReq.Name != resourceName {
				t.Errorf("call 1 request Name = %q, want %q", templateReq.Name, resourceName)
			}
			if templateReq.Containerfile != containerfileContent {
				t.Errorf("call 1 request Containerfile = %q, want %q", templateReq.Containerfile, containerfileContent)
			}
			if templateReq.ProjectID == nil || *templateReq.ProjectID != projectID {
				t.Errorf("call 1 request ProjectID = %v, want %q", templateReq.ProjectID, projectID)
			}

			var state ContainerImageBuildResourceModel
			getDiags := createResp.State.Get(ctx, &state)
			if getDiags.HasError() {
				t.Fatalf("failed to decode final state: %v", getDiags)
			}

			// This resource's documented identity is the cluster environment
			// (application template) id, never the build id.
			if state.ID.ValueString() != templateID {
				t.Errorf("state.ID = %q, want template id %q (NOT build id %q)", state.ID.ValueString(), templateID, buildID)
			}
			if state.BuildID.ValueString() != buildID {
				t.Errorf("state.BuildID = %q, want %q", state.BuildID.ValueString(), buildID)
			}
			if state.BuildStatus.ValueString() != "succeeded" {
				t.Errorf("state.BuildStatus = %q, want %q", state.BuildStatus.ValueString(), "succeeded")
			}
			if state.CreatedAt.ValueString() != createdAt {
				t.Errorf("state.CreatedAt = %q, want %q", state.CreatedAt.ValueString(), createdAt)
			}
			if state.Revision.ValueInt64() != int64(revision) {
				t.Errorf("state.Revision = %d, want %d", state.Revision.ValueInt64(), revision)
			}

			if tt.dockerImageName == nil {
				if !state.ImageURI.IsNull() {
					t.Errorf("state.ImageURI = %q, want null when DockerImageName is nil", state.ImageURI.ValueString())
				}
			} else if state.ImageURI.ValueString() != *tt.dockerImageName {
				t.Errorf("state.ImageURI = %q, want %q", state.ImageURI.ValueString(), *tt.dockerImageName)
			}

			if tt.rayVersion == nil {
				if !state.RayVersion.IsNull() {
					t.Errorf("state.RayVersion = %q, want null when RayVersion is nil", state.RayVersion.ValueString())
				}
			} else if state.RayVersion.ValueString() != *tt.rayVersion {
				t.Errorf("state.RayVersion = %q, want %q", state.RayVersion.ValueString(), *tt.rayVersion)
			}

			if tt.digest == nil {
				if !state.Digest.IsNull() {
					t.Errorf("state.Digest = %q, want null when Digest is nil", state.Digest.ValueString())
				}
			} else if state.Digest.ValueString() != *tt.digest {
				t.Errorf("state.Digest = %q, want %q", state.Digest.ValueString(), *tt.digest)
			}

			wantNameVersion := fmt.Sprintf("%s:%d", resourceName, revision)
			if state.NameVersion.ValueString() != wantNameVersion {
				t.Errorf("state.NameVersion = %q, want %q", state.NameVersion.ValueString(), wantNameVersion)
			}
		})
	}
}

// Helper function for creating string pointers
func strPtr(s string) *string {
	return &s
}
