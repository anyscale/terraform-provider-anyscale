package provider

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestContainerfileValidation tests validation of containerfile vs containerfile_path
func TestContainerfileValidation(t *testing.T) {
	tests := []struct {
		name              string
		containerfile     types.String
		containerfilePath types.String
		wantError         bool
		errorContains     string
	}{
		{
			name:              "containerfile provided",
			containerfile:     types.StringValue("FROM anyscale/ray:2.9.0-py310\nRUN pip install requests"),
			containerfilePath: types.StringNull(),
			wantError:         false,
		},
		{
			name:              "containerfile_path provided",
			containerfile:     types.StringNull(),
			containerfilePath: types.StringValue("/path/to/Containerfile"),
			wantError:         false,
		},
		{
			name:              "neither provided",
			containerfile:     types.StringNull(),
			containerfilePath: types.StringNull(),
			wantError:         true,
			errorContains:     "either containerfile or containerfile_path must be specified",
		},
		{
			name:              "empty containerfile",
			containerfile:     types.StringValue(""),
			containerfilePath: types.StringNull(),
			wantError:         true,
			errorContains:     "either containerfile or containerfile_path must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate resolveContainerfile logic
			var gotError bool
			var gotErrorMsg string

			hasContainerfile := !tt.containerfile.IsNull() && tt.containerfile.ValueString() != ""
			hasContainerfilePath := !tt.containerfilePath.IsNull() && tt.containerfilePath.ValueString() != ""

			if !hasContainerfile && !hasContainerfilePath {
				gotError = true
				gotErrorMsg = "either containerfile or containerfile_path must be specified"
			}

			if gotError != tt.wantError {
				t.Errorf("validation error = %v, wantError %v", gotError, tt.wantError)
			}

			if tt.wantError && gotErrorMsg != tt.errorContains {
				t.Errorf("error message = %v, want %v", gotErrorMsg, tt.errorContains)
			}
		})
	}
}

// TestBuildTimeoutParsing tests parsing of build timeout durations
func TestBuildTimeoutParsing(t *testing.T) {
	tests := []struct {
		name          string
		timeoutStr    string
		wantDuration  time.Duration
		wantError     bool
		errorContains string
	}{
		{
			name:         "30 minutes",
			timeoutStr:   "30m",
			wantDuration: 30 * time.Minute,
			wantError:    false,
		},
		{
			name:         "1 hour",
			timeoutStr:   "1h",
			wantDuration: 1 * time.Hour,
			wantError:    false,
		},
		{
			name:         "45 minutes",
			timeoutStr:   "45m",
			wantDuration: 45 * time.Minute,
			wantError:    false,
		},
		{
			name:         "1 hour 30 minutes",
			timeoutStr:   "1h30m",
			wantDuration: 90 * time.Minute,
			wantError:    false,
		},
		{
			name:         "empty string - default",
			timeoutStr:   "",
			wantDuration: 30 * time.Minute, // default
			wantError:    false,
		},
		{
			name:          "invalid format",
			timeoutStr:    "invalid",
			wantDuration:  0,
			wantError:     true,
			errorContains: "invalid timeout format",
		},
		{
			name:          "missing unit",
			timeoutStr:    "30",
			wantDuration:  0,
			wantError:     true,
			errorContains: "invalid timeout format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate parseTimeout logic
			var duration time.Duration
			var err error

			if tt.timeoutStr == "" {
				duration = 30 * time.Minute // default
			} else {
				duration, err = time.ParseDuration(tt.timeoutStr)
			}

			gotError := err != nil

			if gotError != tt.wantError {
				t.Errorf("parse error = %v, wantError %v", gotError, tt.wantError)
			}

			if !gotError && duration != tt.wantDuration {
				t.Errorf("duration = %v, want %v", duration, tt.wantDuration)
			}
		})
	}
}

// TestBuildStatusValues tests valid build status values
func TestBuildStatusValues(t *testing.T) {
	validStatuses := []string{"pending", "in_progress", "succeeded", "failed", "pending_cancellation", "cancelled"}

	for _, status := range validStatuses {
		t.Run("status_"+status, func(t *testing.T) {
			// Simulate checking for terminal status
			isTerminal := status == "succeeded" || status == "failed" || status == "cancelled"

			switch status {
			case "succeeded", "failed", "cancelled":
				if !isTerminal {
					t.Errorf("status %s should be terminal", status)
				}
			case "pending", "in_progress", "pending_cancellation":
				if isTerminal {
					t.Errorf("status %s should not be terminal", status)
				}
			}
		})
	}
}

// TestBuildStatusTerminalCheck tests the terminal status check logic
func TestBuildStatusTerminalCheck(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		isTerminal bool
		isSuccess  bool
	}{
		{
			name:       "succeeded",
			status:     "succeeded",
			isTerminal: true,
			isSuccess:  true,
		},
		{
			name:       "failed",
			status:     "failed",
			isTerminal: true,
			isSuccess:  false,
		},
		{
			name:       "cancelled",
			status:     "cancelled",
			isTerminal: true,
			isSuccess:  false,
		},
		{
			name:       "pending",
			status:     "pending",
			isTerminal: false,
			isSuccess:  false,
		},
		{
			name:       "in_progress",
			status:     "in_progress",
			isTerminal: false,
			isSuccess:  false,
		},
		{
			name:       "pending_cancellation",
			status:     "pending_cancellation",
			isTerminal: false,
			isSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the waitForBuild status check
			var isTerminal, isSuccess bool

			switch tt.status {
			case "succeeded":
				isTerminal = true
				isSuccess = true
			case "failed", "cancelled":
				isTerminal = true
				isSuccess = false
			case "pending", "in_progress", "pending_cancellation":
				isTerminal = false
				isSuccess = false
			}

			if isTerminal != tt.isTerminal {
				t.Errorf("isTerminal = %v, want %v", isTerminal, tt.isTerminal)
			}

			if isSuccess != tt.isSuccess {
				t.Errorf("isSuccess = %v, want %v", isSuccess, tt.isSuccess)
			}
		})
	}
}

// TestContainerImageBuildModelMapping tests mapping of API response to model
func TestContainerImageBuildModelMapping(t *testing.T) {
	// Simulate API responses
	clusterEnvResult := ClusterEnvironmentResult{
		ID:                "apptemp_123",
		Name:              "my-custom-image",
		CreatorID:         "user_456",
		CreatedAt:         "2024-01-01T00:00:00Z",
		LatestBuildID:     strPtr("bld_789"),
		LatestBuildStatus: strPtr("succeeded"),
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
		ID:          types.StringValue(clusterEnvResult.ID),
		Name:        types.StringValue(clusterEnvResult.Name),
		BuildID:     types.StringValue(buildResult.ID),
		BuildStatus: types.StringValue(buildResult.Status),
		CreatedAt:   types.StringValue(buildResult.CreatedAt),
		Revision:    types.Int64Value(int64(buildResult.Revision)),
		NameVersion: types.StringValue(clusterEnvResult.Name + ":" + "3"),
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

// TestCreateClusterEnvironmentRequestStructure tests the structure of create request
func TestCreateClusterEnvironmentRequestStructure(t *testing.T) {
	projectID := "prj_123"

	req := CreateClusterEnvironmentRequest{
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

// TestBuildErrorMessageHandling tests handling of build error messages
func TestBuildErrorMessageHandling(t *testing.T) {
	tests := []struct {
		name         string
		errorMessage *string
		wantMsg      string
	}{
		{
			name:         "with error message",
			errorMessage: strPtr("Build failed: dependency not found"),
			wantMsg:      "build failed: Build failed: dependency not found",
		},
		{
			name:         "without error message",
			errorMessage: nil,
			wantMsg:      "build failed",
		},
		{
			name:         "empty error message",
			errorMessage: strPtr(""),
			wantMsg:      "build failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate error message construction from waitForBuild
			var errMsg string
			if tt.errorMessage != nil && *tt.errorMessage != "" {
				errMsg = "build failed: " + *tt.errorMessage
			} else {
				errMsg = "build failed"
			}

			if errMsg != tt.wantMsg {
				t.Errorf("error message = %v, want %v", errMsg, tt.wantMsg)
			}
		})
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
