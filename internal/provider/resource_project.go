package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &ProjectResource{}
	_ resource.ResourceWithConfigure   = &ProjectResource{}
	_ resource.ResourceWithImportState = &ProjectResource{}
)

// NewProjectResource creates a new project resource.
func NewProjectResource() resource.Resource {
	return &ProjectResource{}
}

// ProjectResource defines the resource implementation.
type ProjectResource struct {
	client *Client
}

// ProjectResourceModel describes the resource data model.
type ProjectResourceModel struct {
	// Identity
	ID types.String `tfsdk:"id"`

	// Cloud reference - mutually exclusive
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	// Core attributes
	Name                     types.String `tfsdk:"name"`
	Description              types.String `tfsdk:"description"`
	InitialClusterConfigID   types.String `tfsdk:"initial_cluster_config_id"`

	// Nested collaborators
	Collaborators []ProjectCollaboratorModel `tfsdk:"collaborator"`

	// Computed fields
	ClusterConfigID types.String `tfsdk:"cluster_config_id"`
	CreatorID       types.String `tfsdk:"creator_id"`
	CreatedAt       types.String `tfsdk:"created_at"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	LastUsedCloudID types.String `tfsdk:"last_used_cloud_id"`
	IsDefault       types.Bool   `tfsdk:"is_default"`
	DirectoryName   types.String `tfsdk:"directory_name"`
}

// ProjectCollaboratorModel represents a project collaborator.
type ProjectCollaboratorModel struct {
	Email           types.String `tfsdk:"email"`
	PermissionLevel types.String `tfsdk:"permission_level"`
	IdentityID      types.String `tfsdk:"identity_id"` // Computed
	UserID          types.String `tfsdk:"user_id"`     // Computed
}

// Metadata returns the resource type name.
func (r *ProjectResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

// Schema defines the schema for the resource.
func (r *ProjectResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale Project. Projects organize workspaces and resources within a cloud.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// Cloud reference (mutually exclusive)
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The cloud ID for this project. Either `cloud_id` or `cloud_name` must be specified.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The cloud name for this project. Either `cloud_id` or `cloud_name` must be specified. Will be resolved to cloud_id.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// Core attributes
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Description of the project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"initial_cluster_config_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The initial cluster configuration ID to use for workspaces in this project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// Computed fields
			"cluster_config_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The cluster configuration ID assigned to this project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"creator_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the user who created the project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the project was created.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The organization ID this project belongs to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"last_used_cloud_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The ID of the cloud last used by this project.",
			},
			"is_default": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is the default project for the organization.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"directory_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The directory name used for this project's storage.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},

		Blocks: map[string]schema.Block{
			"collaborator": schema.ListNestedBlock{
				MarkdownDescription: "Collaborators with access to this project. Can be added, removed, or modified in-place.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"email": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Email address of the collaborator.",
						},
						"permission_level": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Permission level: 'owner', 'writer', or 'readonly'.",
							Validators: []validator.String{
								stringvalidator.OneOf("owner", "writer", "readonly"),
							},
						},
						"identity_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The identity ID of the collaborator (computed).",
						},
						"user_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The user ID of the collaborator (computed).",
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *ProjectResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *ProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ProjectResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate cloud reference
	if plan.CloudID.IsNull() && plan.CloudName.IsNull() {
		resp.Diagnostics.AddError(
			"Cloud Reference Required",
			"Either 'cloud_id' or 'cloud_name' must be specified to create a project.",
		)
		return
	}

	if !plan.CloudID.IsNull() && !plan.CloudName.IsNull() {
		resp.Diagnostics.AddError(
			"Conflicting Cloud Reference",
			"Cannot specify both 'cloud_id' and 'cloud_name'. Please provide only one.",
		)
		return
	}

	// Resolve cloud_name to cloud_id if needed
	cloudID := plan.CloudID.ValueString()
	if plan.CloudID.IsNull() && !plan.CloudName.IsNull() {
		cloudName := plan.CloudName.ValueString()
		tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": cloudName})

		resolvedID, err := r.resolveCloudNameToID(ctx, cloudName)
		if err != nil {
			resp.Diagnostics.AddError(
				"Cloud Name Resolution Failed",
				fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
			)
			return
		}
		cloudID = resolvedID
		plan.CloudID = types.StringValue(cloudID)
	}

	// Build create request
	createReq := CreateProjectRequest{
		Name:          plan.Name.ValueString(),
		ParentCloudID: cloudID,
	}

	if !plan.Description.IsNull() {
		desc := plan.Description.ValueString()
		createReq.Description = &desc
	}

	if !plan.InitialClusterConfigID.IsNull() {
		configID := plan.InitialClusterConfigID.ValueString()
		createReq.InitialClusterConfigID = &configID
	}

	// Marshal request to JSON
	reqBody, err := json.Marshal(createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Request Serialization Error",
			fmt.Sprintf("Failed to serialize project create request: %s", err.Error()),
		)
		return
	}

	tflog.Debug(ctx, "Creating project", map[string]any{
		"name":            createReq.Name,
		"parent_cloud_id": createReq.ParentCloudID,
	})

	// Create project
	httpResp, err := r.client.DoRequest(ctx, "POST", "/api/v2/projects", strings.NewReader(string(reqBody)))
	if err != nil {
		resp.Diagnostics.AddError(
			"API Request Error",
			fmt.Sprintf("Failed to create project: %s", err.Error()),
		)
		return
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	// Check for errors
	if httpResp.StatusCode != http.StatusCreated && httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		resp.Diagnostics.AddError(
			"Project Creation Failed",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes)),
		)
		return
	}

	// Parse response
	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		resp.Diagnostics.AddError(
			"Response Read Error",
			fmt.Sprintf("Failed to read response body: %s", err.Error()),
		)
		return
	}

	var projectResp ProjectResponse
	if err := json.Unmarshal(bodyBytes, &projectResp); err != nil {
		resp.Diagnostics.AddError(
			"Response Parse Error",
			fmt.Sprintf("Failed to parse project response: %s", err.Error()),
		)
		return
	}

	projectID := projectResp.Result.ID
	plan.ID = types.StringValue(projectID)

	tflog.Info(ctx, "Project created successfully", map[string]any{"project_id": projectID})

	// Create collaborators if specified
	if len(plan.Collaborators) > 0 {
		if err := r.createCollaborators(ctx, projectID, plan.Collaborators); err != nil {
			resp.Diagnostics.AddError(
				"Collaborator Creation Failed",
				fmt.Sprintf("Project created but failed to add collaborators: %s", err.Error()),
			)
			// Continue to read state even if collaborators failed
		}
	}

	// Read back full state
	if err := r.readProject(ctx, projectID, &plan); err != nil {
		resp.Diagnostics.AddError(
			"Read After Create Failed",
			fmt.Sprintf("Project created but failed to read back state: %s", err.Error()),
		)
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *ProjectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ProjectResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ID.ValueString()

	// Read project
	if err := r.readProject(ctx, projectID, &state); err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			tflog.Warn(ctx, "Project not found, removing from state", map[string]any{"project_id": projectID})
			resp.State.RemoveResource(ctx)
			return
		}

		resp.Diagnostics.AddError(
			"Read Error",
			fmt.Sprintf("Failed to read project: %s", err.Error()),
		)
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *ProjectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ProjectResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ID.ValueString()

	tflog.Info(ctx, "Updating project collaborators", map[string]any{"project_id": projectID})

	// Only collaborators can be updated; other fields require replacement
	if err := r.syncCollaborators(ctx, projectID, plan.Collaborators, state.Collaborators); err != nil {
		resp.Diagnostics.AddError(
			"Collaborator Update Failed",
			fmt.Sprintf("Failed to update collaborators: %s", err.Error()),
		)
		return
	}

	// Read back full state
	if err := r.readProject(ctx, projectID, &plan); err != nil {
		resp.Diagnostics.AddError(
			"Read After Update Failed",
			fmt.Sprintf("Update succeeded but failed to read back state: %s", err.Error()),
		)
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *ProjectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ProjectResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ID.ValueString()

	tflog.Info(ctx, "Deleting project", map[string]any{"project_id": projectID})

	// Delete project
	httpResp, err := r.client.DoRequest(ctx, "DELETE", fmt.Sprintf("/api/v2/projects/%s", projectID), nil)
	if err != nil {
		resp.Diagnostics.AddError(
			"API Request Error",
			fmt.Sprintf("Failed to delete project: %s", err.Error()),
		)
		return
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]any{"error": closeErr.Error()})
		}
	}()

	// Handle response
	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent && httpResp.StatusCode != http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		resp.Diagnostics.AddError(
			"Project Deletion Failed",
			fmt.Sprintf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes)),
		)
		return
	}

	tflog.Info(ctx, "Project deleted successfully", map[string]any{"project_id": projectID})
}

// ImportState imports the resource into Terraform state.
func (r *ProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by project ID
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// Helper functions

// resolveCloudNameToID resolves a cloud name to a cloud ID.
func (r *ProjectResource) resolveCloudNameToID(ctx context.Context, cloudName string) (string, error) {
	tflog.Debug(ctx, "Resolving cloud name to ID", map[string]any{"cloud_name": cloudName})

	httpResp, err := r.client.DoRequest(ctx, "GET", "/api/v2/clouds", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return "", fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var cloudsResp CloudsListResponse
	if err := json.Unmarshal(bodyBytes, &cloudsResp); err != nil {
		return "", fmt.Errorf("failed to parse clouds response: %w", err)
	}

	// Find matching cloud(s)
	var matchedCloudID string
	var latestCreatedAt string

	for _, cloud := range cloudsResp.Results {
		if cloud.Name == cloudName {
			if matchedCloudID == "" || cloud.CreatedAt > latestCreatedAt {
				matchedCloudID = cloud.ID
				latestCreatedAt = cloud.CreatedAt
			}
		}
	}

	if matchedCloudID == "" {
		return "", fmt.Errorf("no cloud found with name '%s'", cloudName)
	}

	if latestCreatedAt != "" {
		// Check if there were multiple matches
		matchCount := 0
		for _, cloud := range cloudsResp.Results {
			if cloud.Name == cloudName {
				matchCount++
			}
		}
		if matchCount > 1 {
			tflog.Warn(ctx, "Multiple clouds found with same name, using most recent", map[string]any{
				"cloud_name": cloudName,
				"count":      matchCount,
				"selected":   matchedCloudID,
			})
		}
	}

	tflog.Info(ctx, "Resolved cloud name to ID", map[string]any{
		"cloud_name": cloudName,
		"cloud_id":   matchedCloudID,
	})

	return matchedCloudID, nil
}

// readProject reads a project's details and collaborators into the model.
func (r *ProjectResource) readProject(ctx context.Context, projectID string, model *ProjectResourceModel) error {
	tflog.Debug(ctx, "Reading project", map[string]any{"project_id": projectID})

	// Get project details
	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/projects/%s", projectID), nil)
	if err != nil {
		return fmt.Errorf("failed to get project: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("project not found (404)")
	}

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var projectResp ProjectResponse
	if err := json.Unmarshal(bodyBytes, &projectResp); err != nil {
		return fmt.Errorf("failed to parse project response: %w", err)
	}

	// Map to model
	result := projectResp.Result
	model.ID = types.StringValue(result.ID)
	model.Name = types.StringValue(result.Name)
	model.CloudID = types.StringValue(result.ParentCloudID)

	if result.Description != nil {
		model.Description = types.StringValue(*result.Description)
	} else {
		model.Description = types.StringNull()
	}

	model.ClusterConfigID = types.StringValue(result.ClusterConfig)
	model.CreatorID = types.StringValue(result.CreatorID)
	model.CreatedAt = types.StringValue(result.CreatedAt)
	model.OrganizationID = types.StringValue(result.OrganizationID)

	if result.LastUsedCloudID != nil {
		model.LastUsedCloudID = types.StringValue(*result.LastUsedCloudID)
	} else {
		model.LastUsedCloudID = types.StringNull()
	}

	model.IsDefault = types.BoolValue(result.IsDefault)
	model.DirectoryName = types.StringValue(result.DirectoryName)

	// Get collaborators
	collaborators, err := r.getCollaborators(ctx, projectID)
	if err != nil {
		tflog.Warn(ctx, "Failed to get collaborators", map[string]any{
			"project_id": projectID,
			"error":      err.Error(),
		})
		// Continue without collaborators rather than failing
		model.Collaborators = []ProjectCollaboratorModel{}
	} else {
		model.Collaborators = collaborators
	}

	return nil
}

// getCollaborators fetches the list of collaborators for a project.
func (r *ProjectResource) getCollaborators(ctx context.Context, projectID string) ([]ProjectCollaboratorModel, error) {
	httpResp, err := r.client.DoRequest(ctx, "GET", fmt.Sprintf("/api/v2/projects/%s/collaborators/users", projectID), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get collaborators: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var collabResp ProjectCollaboratorListResponse
	if err := json.Unmarshal(bodyBytes, &collabResp); err != nil {
		return nil, fmt.Errorf("failed to parse collaborators response: %w", err)
	}

	// Map to model
	collaborators := make([]ProjectCollaboratorModel, 0, len(collabResp.Results))
	for _, collab := range collabResp.Results {
		collaborators = append(collaborators, ProjectCollaboratorModel{
			Email:           types.StringValue(collab.Email),
			PermissionLevel: types.StringValue(collab.PermissionLevel),
			IdentityID:      types.StringValue(collab.IdentityID),
			UserID:          types.StringValue(collab.UserID),
		})
	}

	return collaborators, nil
}

// createCollaborators batch creates collaborators for a project.
func (r *ProjectResource) createCollaborators(ctx context.Context, projectID string, collaborators []ProjectCollaboratorModel) error {
	if len(collaborators) == 0 {
		return nil
	}

	tflog.Debug(ctx, "Creating collaborators", map[string]any{
		"project_id": projectID,
		"count":      len(collaborators),
	})

	// Build request
	entries := make(ProjectCollaboratorBatchRequest, 0, len(collaborators))
	for _, collab := range collaborators {
		entries = append(entries, ProjectCollaboratorEntry{
			Value: struct {
				Email string `json:"email"`
			}{
				Email: collab.Email.ValueString(),
			},
			PermissionLevel: collab.PermissionLevel.ValueString(),
		})
	}

	reqBody, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("failed to serialize collaborators request: %w", err)
	}

	httpResp, err := r.client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/projects/%s/collaborators/users/batch_create", projectID), strings.NewReader(string(reqBody)))
	if err != nil {
		return fmt.Errorf("failed to create collaborators: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent && httpResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("API returned status %d: %s", httpResp.StatusCode, string(bodyBytes))
	}

	tflog.Info(ctx, "Collaborators created successfully", map[string]any{
		"project_id": projectID,
		"count":      len(collaborators),
	})

	return nil
}

// syncCollaborators reconciles collaborator changes between plan and state.
func (r *ProjectResource) syncCollaborators(ctx context.Context, projectID string, planned, current []ProjectCollaboratorModel) error {
	// Build maps for comparison
	planMap := make(map[string]ProjectCollaboratorModel)
	for _, collab := range planned {
		planMap[collab.Email.ValueString()] = collab
	}

	currentMap := make(map[string]ProjectCollaboratorModel)
	for _, collab := range current {
		currentMap[collab.Email.ValueString()] = collab
	}

	// Determine adds, updates, removes
	var toAdd []ProjectCollaboratorModel
	var toUpdate []ProjectCollaboratorModel
	var toRemove []ProjectCollaboratorModel

	// Find adds and updates
	for email, planCollab := range planMap {
		if currentCollab, exists := currentMap[email]; exists {
			// Check if permission changed
			if currentCollab.PermissionLevel.ValueString() != planCollab.PermissionLevel.ValueString() {
				planCollab.IdentityID = currentCollab.IdentityID // Preserve identity_id for update
				toUpdate = append(toUpdate, planCollab)
			}
		} else {
			toAdd = append(toAdd, planCollab)
		}
	}

	// Find removes
	for email, currentCollab := range currentMap {
		if _, exists := planMap[email]; !exists {
			toRemove = append(toRemove, currentCollab)
		}
	}

	// Execute changes
	if len(toAdd) > 0 {
		tflog.Info(ctx, "Adding collaborators", map[string]any{"count": len(toAdd)})
		if err := r.createCollaborators(ctx, projectID, toAdd); err != nil {
			return fmt.Errorf("failed to add collaborators: %w", err)
		}
	}

	for _, collab := range toUpdate {
		tflog.Info(ctx, "Updating collaborator permission", map[string]any{
			"email":      collab.Email.ValueString(),
			"permission": collab.PermissionLevel.ValueString(),
		})

		updateReq := ProjectCollaboratorUpdateRequest{
			PermissionLevel: collab.PermissionLevel.ValueString(),
		}

		reqBody, err := json.Marshal(updateReq)
		if err != nil {
			return fmt.Errorf("failed to serialize update request: %w", err)
		}

		identityID := collab.IdentityID.ValueString()
		httpResp, err := r.client.DoRequest(ctx, "PUT", fmt.Sprintf("/api/v2/projects/%s/collaborators/%s", projectID, identityID), strings.NewReader(string(reqBody)))
		if err != nil {
			return fmt.Errorf("failed to update collaborator %s: %w", collab.Email.ValueString(), err)
		}
		httpResp.Body.Close()

		if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("failed to update collaborator %s: status %d", collab.Email.ValueString(), httpResp.StatusCode)
		}
	}

	for _, collab := range toRemove {
		tflog.Info(ctx, "Removing collaborator", map[string]any{"email": collab.Email.ValueString()})

		identityID := collab.IdentityID.ValueString()
		httpResp, err := r.client.DoRequest(ctx, "DELETE", fmt.Sprintf("/api/v2/projects/%s/collaborators/%s", projectID, identityID), nil)
		if err != nil {
			return fmt.Errorf("failed to remove collaborator %s: %w", collab.Email.ValueString(), err)
		}
		httpResp.Body.Close()

		if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent && httpResp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("failed to remove collaborator %s: status %d", collab.Email.ValueString(), httpResp.StatusCode)
		}
	}

	return nil
}
