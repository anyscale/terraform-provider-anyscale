package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &GlobalResourceSchedulerResource{}
	_ resource.ResourceWithConfigure   = &GlobalResourceSchedulerResource{}
	_ resource.ResourceWithImportState = &GlobalResourceSchedulerResource{}
)

// NewGlobalResourceSchedulerResource creates a new global resource scheduler resource.
func NewGlobalResourceSchedulerResource() resource.Resource {
	return &GlobalResourceSchedulerResource{}
}

// GlobalResourceSchedulerResource defines the resource implementation.
type GlobalResourceSchedulerResource struct {
	client *Client
}

// GlobalResourceSchedulerResourceModel describes the resource data model.
type GlobalResourceSchedulerResourceModel struct {
	// Identity
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Configuration
	EnableRootlessDataplaneConfig types.Bool `tfsdk:"enable_rootless_dataplane_config"`

	// Cloud attachments
	CloudAttachments []CloudAttachmentModel `tfsdk:"cloud_attachment"`

	// Spec configuration
	Spec []GlobalResourceSchedulerSpecModel `tfsdk:"spec"`

	// Computed fields
	OrganizationID types.String `tfsdk:"organization_id"`
	CloudIDs       types.List   `tfsdk:"cloud_ids"`
}

// CloudAttachmentModel represents a cloud attachment.
type CloudAttachmentModel struct {
	CloudID         types.String `tfsdk:"cloud_id"`
	CloudName       types.String `tfsdk:"cloud_name"`
	CloudResourceID types.String `tfsdk:"cloud_resource_id"`
}

// GlobalResourceSchedulerSpecModel represents the global resource scheduler specification.
type GlobalResourceSchedulerSpecModel struct {
	MachineTypes []MachineTypeModel `tfsdk:"machine_type"`
}

// MachineTypeModel represents a machine type configuration.
type MachineTypeModel struct {
	Name            types.String          `tfsdk:"name"`
	LaunchTemplates []LaunchTemplateModel `tfsdk:"launch_template"`
	RecyclePolicy   []RecyclePolicyModel  `tfsdk:"recycle_policy"`
	Partitions      []PartitionModel      `tfsdk:"partition"`
}

// LaunchTemplateModel represents a launch template configuration.
type LaunchTemplateModel struct {
	InstanceType           types.String `tfsdk:"instance_type"`
	MarketType             types.String `tfsdk:"market_type"`
	Zones                  types.List   `tfsdk:"zones"`
	AdvancedInstanceConfig types.Map    `tfsdk:"advanced_instance_config"`
}

// RecyclePolicyModel represents a recycle policy configuration.
type RecyclePolicyModel struct {
	MaxWorkloads     types.Int64  `tfsdk:"max_workloads"`
	RotationInterval types.String `tfsdk:"rotation_interval"`
	MaxIdleDuration  types.String `tfsdk:"max_idle_duration"`
}

// PartitionModel represents a partition configuration.
type PartitionModel struct {
	Name  types.String `tfsdk:"name"`
	Size  types.Int64  `tfsdk:"size"`
	Rules []RuleModel  `tfsdk:"rule"`
}

// RuleModel represents a scheduling rule configuration.
type RuleModel struct {
	Selector types.String `tfsdk:"selector"`
	Priority types.Int64  `tfsdk:"priority"`
	Quota    types.Int64  `tfsdk:"quota"`
}

// Metadata returns the resource type name.
func (r *GlobalResourceSchedulerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_global_resource_scheduler"
}

// Schema defines the schema for the resource.
func (r *GlobalResourceSchedulerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale Global Resource Scheduler. Global resource schedulers provide shared compute resources for workloads with priority-based scheduling and resource allocation.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the global resource scheduler.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the global resource scheduler. Must be unique within the organization.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enable_rootless_dataplane_config": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether to enable rootless dataplane configuration for nodes running under this global resource scheduler.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The organization ID that owns the global resource scheduler.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud_ids": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "List of cloud IDs attached to this global resource scheduler.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
		},

		Blocks: map[string]schema.Block{
			"cloud_attachment": schema.ListNestedBlock{
				MarkdownDescription: "Cloud attachments for this global resource scheduler. A global resource scheduler must be attached to at least one cloud to be used.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"cloud_id": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "The cloud ID to attach. Either `cloud_id` or `cloud_name` must be specified.",
						},
						"cloud_name": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "The cloud name to attach. Will be resolved to cloud_id. Either `cloud_id` or `cloud_name` must be specified.",
						},
						"cloud_resource_id": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "The specific cloud resource ID to attach to. If not specified, attaches to the primary cloud resource.",
						},
					},
				},
			},
			"spec": schema.ListNestedBlock{
				MarkdownDescription: "The global resource scheduler specification defining machine types, partitions, and scheduling rules. All global resource schedulers are Anyscale-managed.",
				NestedObject: schema.NestedBlockObject{
					Blocks: map[string]schema.Block{
						"machine_type": schema.ListNestedBlock{
							MarkdownDescription: "Machine type configurations. Each defines a resource class with launch templates and partitions.",
							NestedObject: schema.NestedBlockObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{
										Required:            true,
										MarkdownDescription: "Resource identifier (e.g., `RES-8CPU-32GB`). This name appears in the UI dropdown menu.",
									},
								},
								Blocks: map[string]schema.Block{
									"launch_template": schema.ListNestedBlock{
										MarkdownDescription: "Cloud-specific instance configurations for ANYSCALE_MANAGED pools.",
										NestedObject: schema.NestedBlockObject{
											Attributes: map[string]schema.Attribute{
												"instance_type": schema.StringAttribute{
													Required:            true,
													MarkdownDescription: "Cloud provider instance type (e.g., `m5.2xlarge` for AWS, `n1-standard-8` for GCP).",
												},
												"market_type": schema.StringAttribute{
													Required:            true,
													MarkdownDescription: "Provisioning model: `ON_DEMAND` or `SPOT`.",
													Validators: []validator.String{
														stringvalidator.OneOf("ON_DEMAND", "SPOT"),
													},
												},
												"zones": schema.ListAttribute{
													Optional:            true,
													ElementType:         types.StringType,
													MarkdownDescription: "Availability zones for instance launching (e.g., `us-west-2a`).",
												},
												"advanced_instance_config": schema.MapAttribute{
													Optional:            true,
													ElementType:         types.StringType,
													MarkdownDescription: "Cloud-specific advanced settings (capacity reservations, etc.).",
												},
											},
										},
									},
									"recycle_policy": schema.ListNestedBlock{
										MarkdownDescription: "Instance rotation policy. Only one recycle_policy block is allowed per machine_type.",
										NestedObject: schema.NestedBlockObject{
											Attributes: map[string]schema.Attribute{
												"max_workloads": schema.Int64Attribute{
													Optional:            true,
													MarkdownDescription: "Rotate instances after this number of workloads complete.",
												},
												"rotation_interval": schema.StringAttribute{
													Optional:            true,
													MarkdownDescription: "Rotate instances after this duration (e.g., `24h`, `60m`).",
												},
												"max_idle_duration": schema.StringAttribute{
													Optional:            true,
													MarkdownDescription: "Terminate idle instances after this duration (e.g., `60m`).",
												},
											},
										},
									},
									"partition": schema.ListNestedBlock{
										MarkdownDescription: "Resource allocation groups with scheduling rules.",
										NestedObject: schema.NestedBlockObject{
											Attributes: map[string]schema.Attribute{
												"name": schema.StringAttribute{
													Required:            true,
													MarkdownDescription: "Unique partition identifier within the machine type.",
												},
												"size": schema.Int64Attribute{
													Required:            true,
													MarkdownDescription: "Total number of machines this partition can acquire.",
												},
											},
											Blocks: map[string]schema.Block{
												"rule": schema.ListNestedBlock{
													MarkdownDescription: "Scheduling rules evaluated in order; first match applies.",
													NestedObject: schema.NestedBlockObject{
														Attributes: map[string]schema.Attribute{
															"selector": schema.StringAttribute{
																Required:            true,
																MarkdownDescription: "Kubernetes-style label selector (e.g., `workload-type in (job)`).",
															},
															"priority": schema.Int64Attribute{
																Optional:            true,
																MarkdownDescription: "Higher values equal higher priority. Zero-priority workloads cannot access partition resources.",
															},
															"quota": schema.Int64Attribute{
																Optional:            true,
																MarkdownDescription: "Maximum machines allocable to matching workloads. Unlimited if unspecified.",
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *GlobalResourceSchedulerResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics, "Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *GlobalResourceSchedulerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GlobalResourceSchedulerResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating global resource scheduler", map[string]any{
		"name": plan.Name.ValueString(),
	})

	// Build create request
	createReq := CreateMachinePoolRequest{
		MachinePoolName:               plan.Name.ValueString(),
		EnableRootlessDataplaneConfig: plan.EnableRootlessDataplaneConfig.ValueBool(),
	}

	// Marshal request to JSON
	reqBody, err := MarshalRequestBody(createReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "global resource scheduler create request", err)
		return
	}

	// Create global resource scheduler
	createResp, err := DoRequestAndParse[CreateMachinePoolResponse](
		ctx,
		r.client,
		"POST",
		"/api/v2/machine_pools/create",
		reqBody,
		http.StatusOK,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "create global resource scheduler", err)
		return
	}

	schedulerID := createResp.Result.MachinePool.MachinePoolID
	plan.ID = types.StringValue(schedulerID)
	plan.OrganizationID = types.StringValue(createResp.Result.MachinePool.OrganizationID)

	tflog.Info(ctx, "Global resource scheduler created", map[string]any{
		"id":   schedulerID,
		"name": plan.Name.ValueString(),
	})

	// Attach to clouds FIRST (required before updating spec for ANYSCALE_MANAGED pools)
	for _, attachment := range plan.CloudAttachments {
		if err := r.attachToCloud(ctx, plan.Name.ValueString(), attachment); err != nil {
			AddAPIError(&resp.Diagnostics, "attach global resource scheduler to cloud", err)
			// Continue to save state even if attachment fails
		}
	}

	// Update spec AFTER attaching to clouds (API requires cloud attachment first for ANYSCALE_MANAGED)
	if len(plan.Spec) > 0 {
		if err := r.updateSpec(ctx, plan.Name.ValueString(), plan.Spec); err != nil {
			AddAPIError(&resp.Diagnostics, "update global resource scheduler spec", err)
			return
		}
	}

	// Read back full state
	if err := r.readMachinePool(ctx, plan.Name.ValueString(), &plan); err != nil {
		AddAPIError(&resp.Diagnostics, "read global resource scheduler after create", err)
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *GlobalResourceSchedulerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GlobalResourceSchedulerResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	schedulerName := state.Name.ValueString()

	tflog.Debug(ctx, "Reading global resource scheduler", map[string]any{
		"name": schedulerName,
	})

	// Read global resource scheduler
	if err := r.readMachinePool(ctx, schedulerName, &state); err != nil {
		if strings.Contains(err.Error(), "not found") {
			tflog.Warn(ctx, "Global resource scheduler not found, removing from state", map[string]any{"name": schedulerName})
			resp.State.RemoveResource(ctx)
			return
		}

		AddAPIError(&resp.Diagnostics, "read global resource scheduler", err)
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *GlobalResourceSchedulerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GlobalResourceSchedulerResourceModel

	// Read Terraform plan and state data into the models
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	schedulerName := state.Name.ValueString()

	tflog.Info(ctx, "Updating global resource scheduler", map[string]any{"name": schedulerName})

	// Update spec if changed
	if len(plan.Spec) > 0 {
		if err := r.updateSpec(ctx, schedulerName, plan.Spec); err != nil {
			AddAPIError(&resp.Diagnostics, "update global resource scheduler spec", err)
			return
		}
	} else if len(state.Spec) > 0 {
		// Clear spec if removed
		if err := r.updateSpec(ctx, schedulerName, nil); err != nil {
			AddAPIError(&resp.Diagnostics, "clear global resource scheduler spec", err)
			return
		}
	}

	// Sync cloud attachments
	if err := r.syncCloudAttachments(ctx, schedulerName, plan.CloudAttachments, state.CloudAttachments); err != nil {
		AddAPIError(&resp.Diagnostics, "sync cloud attachments", err)
		return
	}

	// Read back full state
	if err := r.readMachinePool(ctx, schedulerName, &plan); err != nil {
		AddAPIError(&resp.Diagnostics, "read global resource scheduler after update", err)
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *GlobalResourceSchedulerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GlobalResourceSchedulerResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	schedulerName := state.Name.ValueString()

	tflog.Info(ctx, "Deleting global resource scheduler", map[string]any{"name": schedulerName})

	// Detach from all clouds first
	for _, attachment := range state.CloudAttachments {
		if err := r.detachFromCloud(ctx, schedulerName, attachment); err != nil {
			tflog.Warn(ctx, "Failed to detach from cloud during delete", map[string]any{
				"name":  schedulerName,
				"error": err.Error(),
			})
			// Continue with deletion even if detach fails
		}
	}

	// Build delete request
	deleteReq := DeleteMachinePoolRequest{
		MachinePoolName: schedulerName,
	}

	reqBody, err := MarshalRequestBody(deleteReq)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "global resource scheduler delete request", err)
		return
	}

	// Delete global resource scheduler
	_, err = DoRequestRaw(
		ctx,
		r.client,
		"POST",
		"/api/v2/machine_pools/delete",
		reqBody,
		http.StatusOK,
		http.StatusNotFound,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "delete global resource scheduler", err)
		return
	}

	tflog.Info(ctx, "Global resource scheduler deleted", map[string]any{"name": schedulerName})
}

// ImportState imports the resource into Terraform state.
func (r *GlobalResourceSchedulerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by global resource scheduler name
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// Helper functions

// readMachinePool reads a global resource scheduler's details into the model.
func (r *GlobalResourceSchedulerResource) readMachinePool(ctx context.Context, schedulerName string, model *GlobalResourceSchedulerResourceModel) error {
	tflog.Debug(ctx, "Reading global resource scheduler details", map[string]any{"name": schedulerName})

	// List all global resource schedulers and find by name
	listResp, err := DoRequestAndParse[ListMachinePoolsResponse](
		ctx,
		r.client,
		"GET",
		"/api/v2/machine_pools/",
		nil,
		http.StatusOK,
	)
	if err != nil {
		return fmt.Errorf("failed to list global resource schedulers: %w", err)
	}

	// Find the pool by name
	var foundScheduler *MachinePoolResult
	for _, pool := range listResp.Result.MachinePools {
		if pool.MachinePoolName == schedulerName {
			foundScheduler = &pool
			break
		}
	}

	if foundScheduler == nil {
		return fmt.Errorf("global resource scheduler not found: %s", schedulerName)
	}

	// Map to model
	model.ID = types.StringValue(foundScheduler.MachinePoolID)
	model.Name = types.StringValue(foundScheduler.MachinePoolName)
	model.OrganizationID = types.StringValue(foundScheduler.OrganizationID)
	model.EnableRootlessDataplaneConfig = types.BoolValue(foundScheduler.EnableRootlessDataplaneConfig)

	// Convert cloud IDs to list
	cloudIDs := make([]string, len(foundScheduler.CloudIDs))
	copy(cloudIDs, foundScheduler.CloudIDs)
	cloudIDsList, diags := types.ListValueFrom(ctx, types.StringType, cloudIDs)
	if diags.HasError() {
		return fmt.Errorf("failed to convert cloud IDs to list")
	}
	model.CloudIDs = cloudIDsList

	// Convert spec from API response to model
	if len(foundScheduler.Spec) > 0 {
		specModel, err := r.specFromAPI(ctx, foundScheduler.Spec)
		if err != nil {
			tflog.Warn(ctx, "Failed to convert spec from API", map[string]any{
				"error": err.Error(),
			})
			// Keep existing spec in model
		} else {
			model.Spec = specModel
		}
	}

	return nil
}

// updateSpec updates the global resource scheduler spec.
func (r *GlobalResourceSchedulerResource) updateSpec(ctx context.Context, schedulerName string, specModel []GlobalResourceSchedulerSpecModel) error {
	tflog.Debug(ctx, "Updating global resource scheduler spec", map[string]any{"name": schedulerName})

	// Convert model to API spec
	spec := r.specToAPI(ctx, specModel)

	// Build update request
	updateReq := UpdateMachinePoolRequest{
		MachinePoolName: schedulerName,
		Spec:            spec,
	}

	reqBody, err := MarshalRequestBody(updateReq)
	if err != nil {
		return fmt.Errorf("failed to marshal update request: %w", err)
	}

	_, err = DoRequestRaw(
		ctx,
		r.client,
		"POST",
		"/api/v2/machine_pools/update",
		reqBody,
		http.StatusOK,
	)
	if err != nil {
		return fmt.Errorf("failed to update spec: %w", err)
	}

	return nil
}

// attachToCloud attaches the global resource scheduler to a cloud.
func (r *GlobalResourceSchedulerResource) attachToCloud(ctx context.Context, schedulerName string, attachment CloudAttachmentModel) error {
	// Resolve cloud ID
	cloudID := attachment.CloudID.ValueString()
	if cloudID == "" && !attachment.CloudName.IsNull() {
		resolvedID, err := ResolveCloudNameToID(ctx, r.client, attachment.CloudName.ValueString())
		if err != nil {
			return fmt.Errorf("failed to resolve cloud name: %w", err)
		}
		cloudID = resolvedID
	}

	if cloudID == "" {
		return fmt.Errorf("cloud_id or cloud_name must be specified")
	}

	tflog.Info(ctx, "Attaching global resource scheduler to cloud", map[string]any{
		"pool":     schedulerName,
		"cloud_id": cloudID,
	})

	// Build attach request
	attachReq := AttachMachinePoolToCloudRequest{
		MachinePoolName: schedulerName,
		CloudID:         cloudID,
	}

	if !attachment.CloudResourceID.IsNull() {
		resourceID := attachment.CloudResourceID.ValueString()
		attachReq.CloudResourceID = &resourceID
	}

	reqBody, err := MarshalRequestBody(attachReq)
	if err != nil {
		return fmt.Errorf("failed to marshal attach request: %w", err)
	}

	_, err = DoRequestRaw(
		ctx,
		r.client,
		"POST",
		"/api/v2/machine_pools/attach",
		reqBody,
		http.StatusOK,
	)
	if err != nil {
		return fmt.Errorf("failed to attach to cloud: %w", err)
	}

	return nil
}

// detachFromCloud detaches the global resource scheduler from a cloud.
func (r *GlobalResourceSchedulerResource) detachFromCloud(ctx context.Context, schedulerName string, attachment CloudAttachmentModel) error {
	// Resolve cloud ID
	cloudID := attachment.CloudID.ValueString()
	if cloudID == "" && !attachment.CloudName.IsNull() {
		resolvedID, err := ResolveCloudNameToID(ctx, r.client, attachment.CloudName.ValueString())
		if err != nil {
			return fmt.Errorf("failed to resolve cloud name: %w", err)
		}
		cloudID = resolvedID
	}

	if cloudID == "" {
		return fmt.Errorf("cloud_id or cloud_name must be specified")
	}

	tflog.Info(ctx, "Detaching global resource scheduler from cloud", map[string]any{
		"pool":     schedulerName,
		"cloud_id": cloudID,
	})

	// Build detach request
	detachReq := DetachMachinePoolFromCloudRequest{
		MachinePoolName: schedulerName,
		CloudID:         cloudID,
	}

	if !attachment.CloudResourceID.IsNull() {
		resourceID := attachment.CloudResourceID.ValueString()
		detachReq.CloudResourceID = &resourceID
	}

	reqBody, err := MarshalRequestBody(detachReq)
	if err != nil {
		return fmt.Errorf("failed to marshal detach request: %w", err)
	}

	_, err = DoRequestRaw(
		ctx,
		r.client,
		"POST",
		"/api/v2/machine_pools/detach",
		reqBody,
		http.StatusOK,
		http.StatusNotFound,
	)
	if err != nil {
		return fmt.Errorf("failed to detach from cloud: %w", err)
	}

	return nil
}

// syncCloudAttachments reconciles cloud attachment changes.
func (r *GlobalResourceSchedulerResource) syncCloudAttachments(ctx context.Context, schedulerName string, planned, current []CloudAttachmentModel) error {
	// Build maps for comparison using resolved cloud IDs
	planMap := make(map[string]CloudAttachmentModel)
	for _, attachment := range planned {
		cloudID := attachment.CloudID.ValueString()
		if cloudID == "" && !attachment.CloudName.IsNull() {
			resolvedID, err := ResolveCloudNameToID(ctx, r.client, attachment.CloudName.ValueString())
			if err != nil {
				return fmt.Errorf("failed to resolve cloud name: %w", err)
			}
			cloudID = resolvedID
		}
		planMap[cloudID] = attachment
	}

	currentMap := make(map[string]CloudAttachmentModel)
	for _, attachment := range current {
		cloudID := attachment.CloudID.ValueString()
		if cloudID == "" && !attachment.CloudName.IsNull() {
			resolvedID, err := ResolveCloudNameToID(ctx, r.client, attachment.CloudName.ValueString())
			if err != nil {
				return fmt.Errorf("failed to resolve cloud name: %w", err)
			}
			cloudID = resolvedID
		}
		currentMap[cloudID] = attachment
	}

	// Find clouds to attach
	for cloudID, attachment := range planMap {
		if _, exists := currentMap[cloudID]; !exists {
			if err := r.attachToCloud(ctx, schedulerName, attachment); err != nil {
				return fmt.Errorf("failed to attach to cloud %s: %w", cloudID, err)
			}
		}
	}

	// Find clouds to detach
	for cloudID, attachment := range currentMap {
		if _, exists := planMap[cloudID]; !exists {
			if err := r.detachFromCloud(ctx, schedulerName, attachment); err != nil {
				return fmt.Errorf("failed to detach from cloud %s: %w", cloudID, err)
			}
		}
	}

	return nil
}

// specToAPI converts the Terraform model spec to API format.
func (r *GlobalResourceSchedulerResource) specToAPI(ctx context.Context, specModel []GlobalResourceSchedulerSpecModel) map[string]any {
	if len(specModel) == 0 {
		return nil
	}

	spec := specModel[0]
	result := map[string]any{
		"kind": "ANYSCALE_MANAGED", // All global resource schedulers are Anyscale-managed
	}

	if len(spec.MachineTypes) > 0 {
		machineTypes := make([]map[string]any, 0, len(spec.MachineTypes))

		for _, mt := range spec.MachineTypes {
			machineType := map[string]any{
				"machine_type": mt.Name.ValueString(),
			}

			// Launch templates
			if len(mt.LaunchTemplates) > 0 {
				templates := make([]map[string]any, 0, len(mt.LaunchTemplates))
				for _, lt := range mt.LaunchTemplates {
					template := map[string]any{
						"instance_type": lt.InstanceType.ValueString(),
						"market_type":   lt.MarketType.ValueString(),
					}

					if !lt.Zones.IsNull() {
						var zones []string
						lt.Zones.ElementsAs(ctx, &zones, false)
						template["zones"] = zones
					}

					if !lt.AdvancedInstanceConfig.IsNull() {
						var advConfig map[string]string
						lt.AdvancedInstanceConfig.ElementsAs(ctx, &advConfig, false)
						template["advanced_instance_config"] = advConfig
					}

					templates = append(templates, template)
				}
				machineType["launch_templates"] = templates
			}

			// Recycle policy
			if len(mt.RecyclePolicy) > 0 {
				rp := mt.RecyclePolicy[0]
				recyclePolicy := map[string]any{}

				if !rp.MaxWorkloads.IsNull() {
					recyclePolicy["max_workloads"] = rp.MaxWorkloads.ValueInt64()
				}
				if !rp.RotationInterval.IsNull() {
					recyclePolicy["rotation_interval"] = rp.RotationInterval.ValueString()
				}
				if !rp.MaxIdleDuration.IsNull() {
					recyclePolicy["max_idle_duration"] = rp.MaxIdleDuration.ValueString()
				}

				if len(recyclePolicy) > 0 {
					machineType["recycle_policy"] = recyclePolicy
				}
			}

			// Partitions
			if len(mt.Partitions) > 0 {
				partitions := make([]map[string]any, 0, len(mt.Partitions))
				for _, p := range mt.Partitions {
					partition := map[string]any{
						"name": p.Name.ValueString(),
						"size": p.Size.ValueInt64(),
					}

					if len(p.Rules) > 0 {
						rules := make([]map[string]any, 0, len(p.Rules))
						for _, rule := range p.Rules {
							ruleMap := map[string]any{
								"selector": rule.Selector.ValueString(),
							}
							if !rule.Priority.IsNull() {
								ruleMap["priority"] = rule.Priority.ValueInt64()
							}
							if !rule.Quota.IsNull() {
								ruleMap["quota"] = rule.Quota.ValueInt64()
							}
							rules = append(rules, ruleMap)
						}
						partition["rules"] = rules
					}

					partitions = append(partitions, partition)
				}
				machineType["partitions"] = partitions
			}

			machineTypes = append(machineTypes, machineType)
		}
		result["machine_types"] = machineTypes
	}

	return result
}

// specFromAPI converts the API spec to Terraform model format.
func (r *GlobalResourceSchedulerResource) specFromAPI(ctx context.Context, apiSpec map[string]any) ([]GlobalResourceSchedulerSpecModel, error) {
	if len(apiSpec) == 0 {
		return nil, nil
	}

	specModel := GlobalResourceSchedulerSpecModel{}

	// Machine types
	if machineTypesRaw, ok := apiSpec["machine_types"].([]any); ok {
		machineTypes := make([]MachineTypeModel, 0, len(machineTypesRaw))

		for _, mtRaw := range machineTypesRaw {
			mt, ok := mtRaw.(map[string]any)
			if !ok {
				continue
			}

			machineType := MachineTypeModel{}

			if name, ok := mt["machine_type"].(string); ok {
				machineType.Name = types.StringValue(name)
			}

			// Launch templates
			if templatesRaw, ok := mt["launch_templates"].([]any); ok {
				templates := make([]LaunchTemplateModel, 0, len(templatesRaw))
				for _, ltRaw := range templatesRaw {
					lt, ok := ltRaw.(map[string]any)
					if !ok {
						continue
					}

					template := LaunchTemplateModel{}
					if instType, ok := lt["instance_type"].(string); ok {
						template.InstanceType = types.StringValue(instType)
					}
					if marketType, ok := lt["market_type"].(string); ok {
						template.MarketType = types.StringValue(marketType)
					}
					if zonesRaw, ok := lt["zones"].([]any); ok {
						zones := make([]string, len(zonesRaw))
						for i, z := range zonesRaw {
							if zStr, ok := z.(string); ok {
								zones[i] = zStr
							}
						}
						zonesList, _ := types.ListValueFrom(ctx, types.StringType, zones)
						template.Zones = zonesList
					} else {
						template.Zones = types.ListNull(types.StringType)
					}
					template.AdvancedInstanceConfig = types.MapNull(types.StringType)

					templates = append(templates, template)
				}
				machineType.LaunchTemplates = templates
			}

			// Recycle policy
			if rpRaw, ok := mt["recycle_policy"].(map[string]any); ok {
				rp := RecyclePolicyModel{}
				if maxWorkloads, ok := rpRaw["max_workloads"].(float64); ok {
					rp.MaxWorkloads = types.Int64Value(int64(maxWorkloads))
				} else {
					rp.MaxWorkloads = types.Int64Null()
				}
				if rotationInterval, ok := rpRaw["rotation_interval"].(string); ok {
					rp.RotationInterval = types.StringValue(rotationInterval)
				} else {
					rp.RotationInterval = types.StringNull()
				}
				if maxIdleDuration, ok := rpRaw["max_idle_duration"].(string); ok {
					rp.MaxIdleDuration = types.StringValue(maxIdleDuration)
				} else {
					rp.MaxIdleDuration = types.StringNull()
				}
				machineType.RecyclePolicy = []RecyclePolicyModel{rp}
			}

			// Partitions
			if partitionsRaw, ok := mt["partitions"].([]any); ok {
				partitions := make([]PartitionModel, 0, len(partitionsRaw))
				for _, pRaw := range partitionsRaw {
					p, ok := pRaw.(map[string]any)
					if !ok {
						continue
					}

					partition := PartitionModel{}
					if name, ok := p["name"].(string); ok {
						partition.Name = types.StringValue(name)
					}
					if size, ok := p["size"].(float64); ok {
						partition.Size = types.Int64Value(int64(size))
					}

					// Rules
					if rulesRaw, ok := p["rules"].([]any); ok {
						rules := make([]RuleModel, 0, len(rulesRaw))
						for _, rRaw := range rulesRaw {
							rule, ok := rRaw.(map[string]any)
							if !ok {
								continue
							}

							ruleModel := RuleModel{}
							if selector, ok := rule["selector"].(string); ok {
								ruleModel.Selector = types.StringValue(selector)
							}
							if priority, ok := rule["priority"].(float64); ok {
								ruleModel.Priority = types.Int64Value(int64(priority))
							} else {
								ruleModel.Priority = types.Int64Null()
							}
							if quota, ok := rule["quota"].(float64); ok {
								ruleModel.Quota = types.Int64Value(int64(quota))
							} else {
								ruleModel.Quota = types.Int64Null()
							}
							rules = append(rules, ruleModel)
						}
						partition.Rules = rules
					}

					partitions = append(partitions, partition)
				}
				machineType.Partitions = partitions
			}

			machineTypes = append(machineTypes, machineType)
		}
		specModel.MachineTypes = machineTypes
	}

	return []GlobalResourceSchedulerSpecModel{specModel}, nil
}
