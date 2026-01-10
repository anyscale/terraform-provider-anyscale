package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &ComputeConfigResource{}
var _ resource.ResourceWithImportState = &ComputeConfigResource{}

func NewComputeConfigResource() resource.Resource {
	return &ComputeConfigResource{}
}

// ComputeConfigResource defines the resource implementation.
type ComputeConfigResource struct {
	client *Client
}

// ComputeConfigResourceModel describes the resource data model.
type ComputeConfigResourceModel struct {
	ID                     types.String  `tfsdk:"id"`           // Terraform resource ID (same as name for stability across versions)
	ConfigID               types.String  `tfsdk:"config_id"`    // Version-specific API ID (changes with each version)
	NameVersion            types.String  `tfsdk:"name_version"` // Formatted as "name:version" for use with Anyscale APIs
	Name                   types.String  `tfsdk:"name"`
	ProjectID              types.String  `tfsdk:"project_id"`
	CloudID                types.String  `tfsdk:"cloud_id"`
	CloudName              types.String  `tfsdk:"cloud_name"`
	Region                 types.String  `tfsdk:"region"`
	IdleTerminationMinutes types.Int64   `tfsdk:"idle_termination_minutes"`
	MaximumUptimeMinutes   types.Int64   `tfsdk:"maximum_uptime_minutes"`
	AllowedAZs             types.List    `tfsdk:"allowed_azs"`   // List of String
	MinResources           types.Map     `tfsdk:"min_resources"` // Map of Float64
	MaxResources           types.Map     `tfsdk:"max_resources"` // Map of Float64
	EnableCrossZoneScaling types.Bool    `tfsdk:"enable_cross_zone_scaling"`
	AdvancedInstanceConfig types.Dynamic `tfsdk:"advanced_instance_config"` // Dynamic (supports nested objects with mixed types)
	AutoSelectWorkerConfig types.Bool    `tfsdk:"auto_select_worker_config"`
	Flags                  types.Dynamic `tfsdk:"flags"` // Dynamic (supports mixed value types) - KEY FEATURE!
	Version                types.Int64   `tfsdk:"version"`
	CreatedAt              types.String  `tfsdk:"created_at"`
	LastModifiedAt         types.String  `tfsdk:"last_modified_at"`
	HeadNode               types.Object  `tfsdk:"head_node"`    // Single NodeConfigModel
	WorkerNodes            types.List    `tfsdk:"worker_nodes"` // List of WorkerNodeConfigModel
}

// NodeConfigModel describes a node configuration.
type NodeConfigModel struct {
	InstanceType           types.String `tfsdk:"instance_type"`
	Resources              types.Map    `tfsdk:"resources"`                // Map of Float64
	RequiredResources      types.Object `tfsdk:"required_resources"`       // RequiredResourcesModel
	Labels                 types.Map    `tfsdk:"labels"`                   // Map of String
	RequiredLabels         types.Map    `tfsdk:"required_labels"`          // Map of String
	AdvancedInstanceConfig types.String `tfsdk:"advanced_instance_config"` // JSON string
	Flags                  types.String `tfsdk:"flags"`                    // JSON string
	CloudDeployment        types.Object `tfsdk:"cloud_deployment"`         // CloudDeploymentModel
}

// RequiredResourcesModel describes required resources for custom instances.
type RequiredResourcesModel struct {
	CPU         types.Int64  `tfsdk:"cpu"`
	Memory      types.String `tfsdk:"memory"`
	GPU         types.Int64  `tfsdk:"gpu"`
	Accelerator types.String `tfsdk:"accelerator"`
	TPU         types.Int64  `tfsdk:"tpu"`
	TPUHosts    types.Int64  `tfsdk:"tpu_hosts"`
}

// CloudDeploymentModel describes cloud deployment selector.
type CloudDeploymentModel struct {
	Provider    types.String `tfsdk:"provider"`
	Region      types.String `tfsdk:"region"`
	MachinePool types.String `tfsdk:"machine_pool"`
	ID          types.String `tfsdk:"id"`
}

// WorkerNodeConfigModel extends NodeConfigModel with worker-specific fields.
type WorkerNodeConfigModel struct {
	Name                   types.String `tfsdk:"name"`
	MinNodes               types.Int64  `tfsdk:"min_nodes"`
	MaxNodes               types.Int64  `tfsdk:"max_nodes"`
	MarketType             types.String `tfsdk:"market_type"`
	InstanceType           types.String `tfsdk:"instance_type"`
	Resources              types.Map    `tfsdk:"resources"`
	RequiredResources      types.Object `tfsdk:"required_resources"`
	Labels                 types.Map    `tfsdk:"labels"`
	RequiredLabels         types.Map    `tfsdk:"required_labels"`
	AdvancedInstanceConfig types.String `tfsdk:"advanced_instance_config"` // JSON string
	Flags                  types.String `tfsdk:"flags"`                    // JSON string
	CloudDeployment        types.Object `tfsdk:"cloud_deployment"`
}

func (r *ComputeConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_config"
}

func (r *ComputeConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale compute configuration for Ray clusters.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				Description:         "The unique identifier of the compute config (same as name, stable across versions).",
				MarkdownDescription: "The unique identifier of the compute config (same as name, stable across versions).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"config_id": schema.StringAttribute{
				Computed:            true,
				Description:         "The version-specific API ID of the compute config. Changes with each version update.",
				MarkdownDescription: "The version-specific API ID of the compute config. Changes with each version update.",
			},
			"name_version": schema.StringAttribute{
				Computed:            true,
				Description:         "The compute config name and version formatted as 'name:version' for use with Anyscale APIs.",
				MarkdownDescription: "The compute config name and version formatted as `name:version` for use with Anyscale APIs.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				Description:         "The name of the compute config.",
				MarkdownDescription: "The name of the compute config.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				Description:         "The project ID to associate the compute config with.",
				MarkdownDescription: "The project ID to associate the compute config with.",
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Description:         "The ID of the Anyscale cloud to use for launching clusters. Either cloud_id or cloud_name must be specified.",
				MarkdownDescription: "The ID of the Anyscale cloud to use for launching clusters. Either `cloud_id` or `cloud_name` must be specified.",
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				Description:         "The name of the Anyscale cloud to use for launching clusters. Either cloud_id or cloud_name must be specified. If provided, will be resolved to cloud_id.",
				MarkdownDescription: "The name of the Anyscale cloud to use for launching clusters. Either `cloud_id` or `cloud_name` must be specified. If provided, will be resolved to cloud_id.",
			},
			"region": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("USE_CLOUD"),
				Description:         "The region to launch clusters in. Defaults to USE_CLOUD which uses the cloud's default region.",
				MarkdownDescription: "The region to launch clusters in. Defaults to `USE_CLOUD` which uses the cloud's default region.",
			},
			"idle_termination_minutes": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(120),
				Description:         "If set to a positive number, Anyscale will terminate the cluster this many minutes after the cluster is idle. Set to 0 to disable. Defaults to 120 minutes.",
				MarkdownDescription: "If set to a positive number, Anyscale will terminate the cluster this many minutes after the cluster is idle. Set to 0 to disable. Defaults to 120 minutes.",
			},
			"maximum_uptime_minutes": schema.Int64Attribute{
				Optional:            true,
				Description:         "If set to a positive number, Anyscale will terminate the cluster this many minutes after cluster start.",
				MarkdownDescription: "If set to a positive number, Anyscale will terminate the cluster this many minutes after cluster start.",
			},
			"allowed_azs": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "The availability zones that sessions are allowed to be launched in. If not specified, any AZ may be used.",
				MarkdownDescription: "The availability zones that sessions are allowed to be launched in. If not specified, any AZ may be used.",
			},
			"min_resources": schema.MapAttribute{
				ElementType:         types.Float64Type,
				Optional:            true,
				Description:         "Total minimum logical resources across all nodes in the cluster (e.g., {\"CPU\": 4, \"GPU\": 1})",
				MarkdownDescription: "Total minimum logical resources across all nodes in the cluster (e.g., `{\"CPU\": 4, \"GPU\": 1}`)",
			},
			"max_resources": schema.MapAttribute{
				ElementType:         types.Float64Type,
				Optional:            true,
				Description:         "Total maximum logical resources across all nodes in the cluster (e.g., {\"CPU\": 100, \"GPU\": 8})",
				MarkdownDescription: "Total maximum logical resources across all nodes in the cluster (e.g., `{\"CPU\": 100, \"GPU\": 8}`)",
			},
			"enable_cross_zone_scaling": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				Description:         "Allow instances in the cluster to be run across multiple zones. Recommended for production services.",
				MarkdownDescription: "Allow instances in the cluster to be run across multiple zones. Recommended for production services.",
			},
			"advanced_instance_config": schema.DynamicAttribute{
				Optional:            true,
				Description:         "Advanced instance configurations for this compute config to pass to the cloud provider when launching instances. Supports nested objects and mixed types.",
				MarkdownDescription: "Advanced instance configurations for this compute config to pass to the cloud provider when launching instances. Supports nested objects and mixed types.",
			},
			"auto_select_worker_config": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				Description:         "If set to true, worker node groups will automatically be selected based on workload.",
				MarkdownDescription: "If set to true, worker node groups will automatically be selected based on workload.",
			},
			"flags": schema.DynamicAttribute{
				Optional:            true,
				Description:         "A set of advanced cluster-level flags that can be used to configure a particular workload. Supports strings, numbers, and booleans.",
				MarkdownDescription: "A set of advanced cluster-level flags that can be used to configure a particular workload. Supports strings, numbers, and booleans.",
			},
			"version": schema.Int64Attribute{
				Computed:            true,
				Description:         "The version number of this compute config.",
				MarkdownDescription: "The version number of this compute config.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				Description:         "The timestamp when the compute config was created.",
				MarkdownDescription: "The timestamp when the compute config was created.",
			},
			"last_modified_at": schema.StringAttribute{
				Computed:            true,
				Description:         "The timestamp when the compute config was last modified.",
				MarkdownDescription: "The timestamp when the compute config was last modified.",
			},
			"head_node": schema.SingleNestedAttribute{
				Required:            true,
				Description:         "Configuration for the head node of the cluster.",
				MarkdownDescription: "Configuration for the head node of the cluster.",
				Attributes:          nodeConfigAttributes(),
			},
			"worker_nodes": schema.ListNestedAttribute{
				Optional:            true,
				Description:         "Configuration for the worker nodes of the cluster. If not provided, worker nodes will be automatically selected based on logical resource requests.",
				MarkdownDescription: "Configuration for the worker nodes of the cluster. If not provided, worker nodes will be automatically selected based on logical resource requests.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: workerNodeConfigAttributes(),
				},
			},
		},
	}
}

// nodeConfigAttributes returns the schema attributes for a node configuration.
func nodeConfigAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"instance_type": schema.StringAttribute{
			Required:            true,
			Description:         "Cloud provider instance type (e.g., m5.2xlarge on AWS, n2-standard-8 on GCP). Use 'custom' when required_resources is provided.",
			MarkdownDescription: "Cloud provider instance type (e.g., `m5.2xlarge` on AWS, `n2-standard-8` on GCP). Use `custom` when `required_resources` is provided.",
		},
		"resources": schema.MapAttribute{
			ElementType:         types.Float64Type,
			Optional:            true,
			Description:         "Logical resources that will be available on this node. Defaults to match the physical resources of the instance type.",
			MarkdownDescription: "Logical resources that will be available on this node. Defaults to match the physical resources of the instance type.",
		},
		"required_resources": schema.SingleNestedAttribute{
			Optional:            true,
			Description:         "Physical resources for custom instance types (free pod shapes). Explicitly defines CPU, memory, and GPU resources.",
			MarkdownDescription: "Physical resources for custom instance types (free pod shapes). Explicitly defines CPU, memory, and GPU resources.",
			Attributes: map[string]schema.Attribute{
				"cpu": schema.Int64Attribute{
					Optional:            true,
					Description:         "Number of CPUs to allocate.",
					MarkdownDescription: "Number of CPUs to allocate.",
				},
				"memory": schema.StringAttribute{
					Optional:            true,
					Description:         "Amount of memory to allocate. Can be specified as bytes (int) or as a string with units (e.g., '4Gi', '1024Mi').",
					MarkdownDescription: "Amount of memory to allocate. Can be specified as bytes (int) or as a string with units (e.g., `4Gi`, `1024Mi`).",
				},
				"gpu": schema.Int64Attribute{
					Optional:            true,
					Description:         "Number of GPUs to allocate.",
					MarkdownDescription: "Number of GPUs to allocate.",
				},
				"accelerator": schema.StringAttribute{
					Optional:            true,
					Description:         "Type of accelerator (e.g., 'T4', 'L4', 'A100', 'H100', 'TPU-V6E').",
					MarkdownDescription: "Type of accelerator (e.g., `T4`, `L4`, `A100`, `H100`, `TPU-V6E`).",
				},
				"tpu": schema.Int64Attribute{
					Optional:            true,
					Description:         "Number of TPUs to allocate.",
					MarkdownDescription: "Number of TPUs to allocate.",
				},
				"tpu_hosts": schema.Int64Attribute{
					Optional:            true,
					Description:         "Number of TPU hosts (for anyscale/tpu_hosts custom resource).",
					MarkdownDescription: "Number of TPU hosts (for `anyscale/tpu_hosts` custom resource).",
				},
			},
		},
		"labels": schema.MapAttribute{
			ElementType:         types.StringType,
			Optional:            true,
			Description:         "Labels to associate the node with for scheduling purposes.",
			MarkdownDescription: "Labels to associate the node with for scheduling purposes.",
		},
		"required_labels": schema.MapAttribute{
			ElementType:         types.StringType,
			Optional:            true,
			Description:         "Required labels that must be present on the node for scheduling purposes.",
			MarkdownDescription: "Required labels that must be present on the node for scheduling purposes.",
		},
		"advanced_instance_config": schema.StringAttribute{
			Optional:            true,
			Description:         "Advanced instance configurations that will be passed through to the cloud provider as a JSON string. Use jsonencode() for HCL objects.",
			MarkdownDescription: "Advanced instance configurations that will be passed through to the cloud provider as a JSON string. Use `jsonencode()` for HCL objects.",
		},
		"flags": schema.StringAttribute{
			Optional:            true,
			Description:         "Node-level flags specifying advanced or experimental options as a JSON string. Use jsonencode() for HCL objects.",
			MarkdownDescription: "Node-level flags specifying advanced or experimental options as a JSON string. Use `jsonencode()` for HCL objects.",
		},
		"cloud_deployment": schema.SingleNestedAttribute{
			Optional:            true,
			Description:         "Cloud deployment selectors for this node; one or more selectors may be passed to target a specific deployment.",
			MarkdownDescription: "Cloud deployment selectors for this node; one or more selectors may be passed to target a specific deployment.",
			Attributes: map[string]schema.Attribute{
				"provider": schema.StringAttribute{
					Optional:            true,
					Description:         "Cloud provider name, e.g., aws or gcp.",
					MarkdownDescription: "Cloud provider name, e.g., `aws` or `gcp`.",
				},
				"region": schema.StringAttribute{
					Optional:            true,
					Description:         "Cloud provider region, e.g., us-west-2.",
					MarkdownDescription: "Cloud provider region, e.g., `us-west-2`.",
				},
				"machine_pool": schema.StringAttribute{
					Optional:            true,
					Description:         "Machine pool name.",
					MarkdownDescription: "Machine pool name.",
				},
				"id": schema.StringAttribute{
					Optional:            true,
					Description:         "Cloud deployment ID from cloud setup.",
					MarkdownDescription: "Cloud deployment ID from cloud setup.",
				},
			},
		},
	}
}

// workerNodeConfigAttributes returns the schema attributes for a worker node configuration.
func workerNodeConfigAttributes() map[string]schema.Attribute {
	attrs := nodeConfigAttributes()

	// Add worker-specific fields
	attrs["name"] = schema.StringAttribute{
		Optional:            true,
		Description:         "Unique name of this worker group. Defaults to a human-friendly representation of the instance type.",
		MarkdownDescription: "Unique name of this worker group. Defaults to a human-friendly representation of the instance type.",
	}
	attrs["min_nodes"] = schema.Int64Attribute{
		Optional:            true,
		Computed:            true,
		Default:             int64default.StaticInt64(0),
		Description:         "Minimum number of nodes of this type that will be kept running in the cluster.",
		MarkdownDescription: "Minimum number of nodes of this type that will be kept running in the cluster.",
	}
	attrs["max_nodes"] = schema.Int64Attribute{
		Optional:            true,
		Computed:            true,
		Default:             int64default.StaticInt64(10),
		Description:         "Maximum number of nodes of this type that can be running in the cluster.",
		MarkdownDescription: "Maximum number of nodes of this type that can be running in the cluster.",
	}
	attrs["market_type"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		Default:             stringdefault.StaticString("ON_DEMAND"),
		Description:         "The type of instances to use: ON_DEMAND (standard pricing), SPOT (discounted, interruptible), or PREFER_SPOT (prefer spot with on-demand fallback).",
		MarkdownDescription: "The type of instances to use: `ON_DEMAND` (standard pricing), `SPOT` (discounted, interruptible), or `PREFER_SPOT` (prefer spot with on-demand fallback).",
	}

	return attrs
}

func (r *ComputeConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)

	if !ok {
		AddConfigError(
			&resp.Diagnostics,
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// buildComputeConfigRequest builds the API request body for creating/updating a compute config.
// Returns the request body and cloud_id, or an error via diagnostics.
func (r *ComputeConfigResource) buildComputeConfigRequest(
	ctx context.Context,
	plan *ComputeConfigResourceModel,
	diags *diag.Diagnostics,
) (map[string]interface{}, string) {
	// Validate that either cloud_id or cloud_name is provided
	if plan.CloudID.IsNull() && plan.CloudName.IsNull() {
		AddConfigError(
			diags,
			"Missing Required Attribute",
			"Either 'cloud_id' or 'cloud_name' must be specified.",
		)
		return nil, ""
	}

	// Resolve cloud_name to cloud_id if needed
	cloudID := plan.CloudID.ValueString()
	if (plan.CloudID.IsNull() || plan.CloudID.IsUnknown()) && !plan.CloudName.IsNull() {
		cloudName := plan.CloudName.ValueString()
		tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": cloudName})

		resolvedID, err := ResolveCloudNameToID(ctx, r.client, cloudName)
		if err != nil {
			AddConfigError(
				diags,
				"Cloud Name Resolution Failed",
				fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
			)
			return nil, ""
		}
		cloudID = resolvedID
		plan.CloudID = types.StringValue(cloudID)
	}

	// Build the API request
	tflog.Debug(ctx, "Building compute config request", map[string]any{
		"cloud_id": cloudID,
		"name":     plan.Name.ValueString(),
	})

	createRequest := map[string]interface{}{
		"name":        plan.Name.ValueString(),
		"anonymous":   false, // Always false since name is required for Terraform-managed resources
		"new_version": true,  // Always create a new version (required for updates to work)
		"config": map[string]interface{}{
			"cloud_id": cloudID,
		},
	}

	// Add optional project_id
	if !plan.ProjectID.IsNull() {
		createRequest["project_id"] = plan.ProjectID.ValueString()
	}

	config := createRequest["config"].(map[string]interface{})

	// Add region
	if !plan.Region.IsNull() {
		config["region"] = plan.Region.ValueString()
	}

	// Add idle_termination_minutes
	if !plan.IdleTerminationMinutes.IsNull() {
		config["idle_termination_minutes"] = plan.IdleTerminationMinutes.ValueInt64()
	}

	// Add maximum_uptime_minutes
	if !plan.MaximumUptimeMinutes.IsNull() {
		config["maximum_uptime_minutes"] = plan.MaximumUptimeMinutes.ValueInt64()
	}

	// Add allowed_azs
	if !plan.AllowedAZs.IsNull() {
		allowedAzs, azDiags := StringListToInterface(ctx, plan.AllowedAZs)
		diags.Append(azDiags...)
		if diags.HasError() {
			return nil, ""
		}
		config["allowed_azs"] = allowedAzs
	}

	// Add auto_select_worker_config
	if !plan.AutoSelectWorkerConfig.IsNull() {
		config["auto_select_worker_config"] = plan.AutoSelectWorkerConfig.ValueBool()
	}

	// Add advanced_instance_config (now a Dynamic value!)
	if !plan.AdvancedInstanceConfig.IsNull() {
		advancedConfig, err := DynamicToInterface(ctx, plan.AdvancedInstanceConfig)
		if err != nil {
			AddConfigError(diags, "Failed to Convert Advanced Instance Config", err.Error())
			return nil, ""
		}
		config["advanced_configurations_json"] = advancedConfig
	}

	// Add flags (Dynamic value - THE KEY FEATURE!)
	// Also translate enable_cross_zone_scaling to flags
	var flags map[string]interface{}
	if !plan.Flags.IsNull() {
		var err error
		flags, err = DynamicToInterface(ctx, plan.Flags)
		if err != nil {
			AddConfigError(diags, "Failed to Convert Flags", err.Error())
			return nil, ""
		}
		if flags == nil {
			flags = make(map[string]interface{})
		}
	} else {
		flags = make(map[string]interface{})
	}

	// Translate enable_cross_zone_scaling to flag (per CLI behavior)
	if !plan.EnableCrossZoneScaling.IsNull() {
		flags["allow-cross-zone-autoscaling"] = plan.EnableCrossZoneScaling.ValueBool()
	}

	// Add min_resources to flags (per CLI behavior)
	if !plan.MinResources.IsNull() {
		minResourcesMap := make(map[string]interface{})
		for key, value := range plan.MinResources.Elements() {
			if float64Val, ok := value.(types.Float64); ok && !float64Val.IsNull() {
				minResourcesMap[key] = float64Val.ValueFloat64()
			}
		}
		if len(minResourcesMap) > 0 {
			flags["min_resources"] = minResourcesMap
		}
	}

	// Add max_resources to flags (per CLI behavior)
	if !plan.MaxResources.IsNull() {
		maxResourcesMap := make(map[string]interface{})
		for key, value := range plan.MaxResources.Elements() {
			if float64Val, ok := value.(types.Float64); ok && !float64Val.IsNull() {
				maxResourcesMap[key] = float64Val.ValueFloat64()
			}
		}
		if len(maxResourcesMap) > 0 {
			flags["max_resources"] = maxResourcesMap
		}
	}

	if len(flags) > 0 {
		config["flags"] = flags
	}

	// Add head_node
	if !plan.HeadNode.IsNull() {
		headNodeConfig, err := nodeConfigToAPI(ctx, plan.HeadNode)
		if err != nil {
			AddConfigError(diags, "Failed to Convert Head Node", err.Error())
			return nil, ""
		}
		if headNodeConfig != nil {
			config["head_node_type"] = headNodeConfig
		}
	}

	// Add worker_nodes
	if !plan.WorkerNodes.IsNull() {
		workerNodeElements := plan.WorkerNodes.Elements()
		workerConfigs := make([]map[string]interface{}, 0, len(workerNodeElements))

		for _, workerNodeValue := range workerNodeElements {
			workerNodeObj, ok := workerNodeValue.(types.Object)
			if !ok {
				AddConfigError(diags, "Invalid Worker Node", "Expected types.Object for worker node")
				return nil, ""
			}

			workerConfig, err := workerNodeConfigToAPI(ctx, workerNodeObj)
			if err != nil {
				AddConfigError(diags, "Failed to Convert Worker Node", err.Error())
				return nil, ""
			}
			if workerConfig != nil {
				workerConfigs = append(workerConfigs, workerConfig)
			}
		}

		if len(workerConfigs) > 0 {
			config["worker_node_types"] = workerConfigs
		}
	}

	return createRequest, cloudID
}

func (r *ComputeConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ComputeConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build the request
	createRequest, _ := r.buildComputeConfigRequest(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Make API call to create compute config
	reqBody, err := MarshalRequestBody(createRequest)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "compute config request", err)
		return
	}

	log.Printf("[DEBUG] POST /ext/v0/cluster_computes/ - Creating compute config")

	apiResult, err := DoRequestAndParse[map[string]interface{}](
		ctx, r.client, "POST", "/ext/v0/cluster_computes/", reqBody,
		http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "create compute config", err)
		return
	}

	// Extract result from response
	resultData, ok := (*apiResult)["result"].(map[string]interface{})
	if !ok {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return expected result structure")
		return
	}

	// Set the IDs:
	// - ID = name (stable across versions, used as Terraform resource identifier)
	// - ConfigID = API ID (version-specific, changes with each update)
	configID, ok := resultData["id"].(string)
	if !ok || configID == "" {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return an ID")
		return
	}
	plan.ID = types.StringValue(plan.Name.ValueString()) // Use name as stable Terraform ID
	plan.ConfigID = types.StringValue(configID)          // Store version-specific ID
	log.Printf("[INFO] Created compute config: name=%s, config_id=%s", plan.Name.ValueString(), configID)

	// Extract computed fields from create response
	if version, ok := resultData["version"].(float64); ok {
		plan.Version = types.Int64Value(int64(version))
		// Set name_version formatted as "name:version" for use with Anyscale APIs
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", plan.Name.ValueString(), int64(version)))
	}
	if createdAt, ok := resultData["created_at"].(string); ok {
		plan.CreatedAt = types.StringValue(createdAt)
	}
	if lastModifiedAt, ok := resultData["last_modified_at"].(string); ok {
		plan.LastModifiedAt = types.StringValue(lastModifiedAt)
	}

	// Set state with all fields populated
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *ComputeConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ComputeConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Use ConfigID for API lookup (version-specific ID)
	// Fall back to ID if ConfigID is not set (for backwards compatibility or import)
	lookupID := state.ConfigID.ValueString()
	if lookupID == "" {
		lookupID = state.ID.ValueString()
	}

	// Make API call to get compute config
	apiResult, err := DoRequestAndParse[map[string]interface{}](
		ctx, r.client, "GET", fmt.Sprintf("/ext/v0/cluster_computes/%s", lookupID), nil,
		http.StatusOK, http.StatusNotFound,
	)
	if err != nil {
		// Check if it's a 404 - resource was deleted
		if apiResult == nil {
			log.Printf("[WARN] Compute config not found, removing from state: config_id=%s", lookupID)
			resp.State.RemoveResource(ctx)
			return
		}
		AddAPIError(&resp.Diagnostics, "read compute config", err)
		return
	}

	// Extract result from response
	resultData, ok := (*apiResult)["result"].(map[string]interface{})
	if !ok {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return expected result structure")
		return
	}

	// Update ConfigID from API response (in case it changed)
	if apiID, ok := resultData["id"].(string); ok {
		state.ConfigID = types.StringValue(apiID)
	}

	// Update state with response
	if name, ok := resultData["name"].(string); ok {
		state.Name = types.StringValue(name)
		// ID should match name (stable identifier)
		state.ID = types.StringValue(name)
	}

	if version, ok := resultData["version"].(float64); ok {
		state.Version = types.Int64Value(int64(version))
		// Set name_version formatted as "name:version" for use with Anyscale APIs
		state.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", state.Name.ValueString(), int64(version)))
	}

	if createdAt, ok := resultData["created_at"].(string); ok {
		state.CreatedAt = types.StringValue(createdAt)
	}

	if lastModifiedAt, ok := resultData["last_modified_at"].(string); ok {
		state.LastModifiedAt = types.StringValue(lastModifiedAt)
	}

	if projectID, ok := resultData["project_id"].(string); ok {
		state.ProjectID = types.StringValue(projectID)
	} else {
		state.ProjectID = types.StringNull()
	}

	// Extract config object
	if configData, ok := resultData["config"].(map[string]interface{}); ok {
		if cloudID, ok := configData["cloud_id"].(string); ok {
			state.CloudID = types.StringValue(cloudID)
		}

		if region, ok := configData["region"].(string); ok {
			state.Region = types.StringValue(region)
		}

		if idleTermination, ok := configData["idle_termination_minutes"].(float64); ok {
			state.IdleTerminationMinutes = types.Int64Value(int64(idleTermination))
		}

		if maximumUptime, ok := configData["maximum_uptime_minutes"].(float64); ok {
			state.MaximumUptimeMinutes = types.Int64Value(int64(maximumUptime))
		}

		if allowedAzs, ok := configData["allowed_azs"].([]interface{}); ok {
			allowedAzsList, diags := InterfaceListToString(ctx, allowedAzs)
			resp.Diagnostics.Append(diags...)
			state.AllowedAZs = allowedAzsList
		}

		if autoSelect, ok := configData["auto_select_worker_config"].(bool); ok {
			state.AutoSelectWorkerConfig = types.BoolValue(autoSelect)
		}

		// Parse min_resources, max_resources, and enable_cross_zone_scaling from flags
		// (per CLI behavior, these are stored in flags when sent to the API)
		if flags, ok := configData["flags"].(map[string]interface{}); ok {
			if minResources, ok := flags["min_resources"].(map[string]interface{}); ok {
				minResourcesMap, diags := InterfaceMapToFloat64(ctx, minResources)
				resp.Diagnostics.Append(diags...)
				state.MinResources = minResourcesMap
			}

			if maxResources, ok := flags["max_resources"].(map[string]interface{}); ok {
				maxResourcesMap, diags := InterfaceMapToFloat64(ctx, maxResources)
				resp.Diagnostics.Append(diags...)
				state.MaxResources = maxResourcesMap
			}

			if enableCrossZone, ok := flags["allow-cross-zone-autoscaling"].(bool); ok {
				state.EnableCrossZoneScaling = types.BoolValue(enableCrossZone)
			}
		}

		// NOTE: We intentionally do NOT read advanced_instance_config from the API response
		// The API's representation may differ from our config's representation (e.g., null vs empty arrays)
		// This would cause perpetual drift. We preserve what the user configured.

		// NOTE: We intentionally do NOT read user-defined flags from the API response
		// The flags field should only reflect what's in the user's configuration
		// We extract special flags (min_resources, max_resources, allow-cross-zone-autoscaling) above,
		// but user's custom flags are preserved as-is from their configuration.

		// Parse head_node from API response
		if headNodeType, ok := configData["head_node_type"].(map[string]interface{}); ok {
			headNodeObj, headNodeDiags := apiNodeTypeToTerraform(ctx, headNodeType)
			resp.Diagnostics.Append(headNodeDiags...)
			if !resp.Diagnostics.HasError() {
				state.HeadNode = headNodeObj
			}
		}

		// Parse worker_nodes from API response
		if workerNodeTypes, ok := configData["worker_node_types"].([]interface{}); ok {
			workerNodesList, workerNodesDiags := apiWorkerNodeTypesToTerraform(ctx, workerNodeTypes)
			resp.Diagnostics.Append(workerNodesDiags...)
			if !resp.Diagnostics.HasError() {
				state.WorkerNodes = workerNodesList
			}
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ComputeConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ComputeConfigResourceModel
	var state ComputeConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Anyscale compute configs support versioning - updates create a new version with the same name.
	// The API handles this via POST with new_version=true and the same name.
	// This gives us a new ID and incremented version number.

	tflog.Info(ctx, "Updating compute config by creating new version", map[string]any{
		"name":          plan.Name.ValueString(),
		"old_config_id": state.ConfigID.ValueString(),
		"old_version":   state.Version.ValueInt64(),
	})

	// Build the request using the same helper as Create
	updateRequest, _ := r.buildComputeConfigRequest(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Make API call to create new version
	reqBody, err := MarshalRequestBody(updateRequest)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "compute config update request", err)
		return
	}

	log.Printf("[DEBUG] POST /ext/v0/cluster_computes/ - Creating new version of compute config")

	apiResult, err := DoRequestAndParse[map[string]interface{}](
		ctx, r.client, "POST", "/ext/v0/cluster_computes/", reqBody,
		http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "update compute config (create new version)", err)
		return
	}

	resultData, ok := (*apiResult)["result"].(map[string]interface{})
	if !ok {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return expected result structure")
		return
	}

	// Extract the new config ID - this changes with each version
	newConfigID, ok := resultData["id"].(string)
	if !ok || newConfigID == "" {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return an ID for the new version")
		return
	}

	tflog.Info(ctx, "Created new compute config version", map[string]any{
		"name":          plan.Name.ValueString(),
		"new_config_id": newConfigID,
		"new_version":   resultData["version"],
	})

	// Update state with new computed values
	// ID stays the same (name), ConfigID changes to new version-specific ID
	plan.ID = types.StringValue(plan.Name.ValueString()) // Keep ID = name (stable)
	plan.ConfigID = types.StringValue(newConfigID)       // Update version-specific ID
	if version, ok := resultData["version"].(float64); ok {
		plan.Version = types.Int64Value(int64(version))
		// Set name_version formatted as "name:version" for use with Anyscale APIs
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", plan.Name.ValueString(), int64(version)))
	}
	if createdAt, ok := resultData["created_at"].(string); ok {
		plan.CreatedAt = types.StringValue(createdAt)
	}
	if lastModifiedAt, ok := resultData["last_modified_at"].(string); ok {
		plan.LastModifiedAt = types.StringValue(lastModifiedAt)
	}

	// Set updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ComputeConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ComputeConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Use ConfigID for the archive API call (version-specific ID)
	configID := state.ConfigID.ValueString()
	if configID == "" {
		// Fallback to ID for backwards compatibility
		configID = state.ID.ValueString()
	}

	log.Printf("[INFO] Deleting compute config: name=%s, config_id=%s", state.Name.ValueString(), configID)

	// Make API call to delete compute config
	_, err := DoRequestRaw(
		ctx, r.client, "DELETE", fmt.Sprintf("/ext/v0/cluster_computes/%s", configID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "delete compute config", err)
		return
	}

	log.Printf("[INFO] Deleted compute config successfully")
}

func (r *ComputeConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import accepts the version-specific config ID (e.g., "cpt_xxx")
	// We set it as config_id, and Read will populate id (name) from the API response
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("config_id"), req.ID)...)
}

// Helper functions for converting nested objects

// nodeConfigToAPI converts a head_node or worker_node object to API format
func nodeConfigToAPI(ctx context.Context, nodeObj types.Object) (map[string]interface{}, error) {
	if nodeObj.IsNull() || nodeObj.IsUnknown() {
		return nil, nil
	}

	var node NodeConfigModel
	diags := nodeObj.As(ctx, &node, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert node config: %v", diags)
	}

	config := map[string]interface{}{
		"name":          "head", // API requires name field
		"instance_type": node.InstanceType.ValueString(),
	}

	// Add resources
	if !node.Resources.IsNull() {
		resourcesMap := make(map[string]interface{})
		elements := node.Resources.Elements()
		for key, value := range elements {
			if float64Val, ok := value.(types.Float64); ok && !float64Val.IsNull() {
				resourcesMap[key] = float64Val.ValueFloat64()
			}
		}
		if len(resourcesMap) > 0 {
			config["resources"] = resourcesMap
		}
	}

	// Add required_resources
	if !node.RequiredResources.IsNull() {
		var reqRes RequiredResourcesModel
		diags := node.RequiredResources.As(ctx, &reqRes, basetypes.ObjectAsOptions{})
		if !diags.HasError() {
			reqResourcesMap := make(map[string]interface{})

			if !reqRes.CPU.IsNull() {
				reqResourcesMap["cpu"] = reqRes.CPU.ValueInt64()
			}
			if !reqRes.Memory.IsNull() {
				reqResourcesMap["memory"] = reqRes.Memory.ValueString()
			}
			if !reqRes.GPU.IsNull() {
				reqResourcesMap["gpu"] = reqRes.GPU.ValueInt64()
			}
			if !reqRes.Accelerator.IsNull() {
				reqResourcesMap["accelerator"] = reqRes.Accelerator.ValueString()
			}
			if !reqRes.TPU.IsNull() {
				reqResourcesMap["tpu"] = reqRes.TPU.ValueInt64()
			}
			if !reqRes.TPUHosts.IsNull() {
				reqResourcesMap["anyscale/tpu_hosts"] = reqRes.TPUHosts.ValueInt64()
			}

			if len(reqResourcesMap) > 0 {
				config["physical_resources"] = reqResourcesMap
			}
		}
	}

	// Add labels
	if !node.Labels.IsNull() {
		labelsMap := make(map[string]interface{})
		elements := node.Labels.Elements()
		for key, value := range elements {
			if strVal, ok := value.(types.String); ok && !strVal.IsNull() {
				labelsMap[key] = strVal.ValueString()
			}
		}
		if len(labelsMap) > 0 {
			config["labels"] = labelsMap
		}
	}

	// Add required_labels
	if !node.RequiredLabels.IsNull() {
		reqLabelsMap := make(map[string]interface{})
		elements := node.RequiredLabels.Elements()
		for key, value := range elements {
			if strVal, ok := value.(types.String); ok && !strVal.IsNull() {
				reqLabelsMap[key] = strVal.ValueString()
			}
		}
		if len(reqLabelsMap) > 0 {
			config["required_labels"] = reqLabelsMap
		}
	}

	// Add advanced_instance_config (JSON string) - map to API field name
	if !node.AdvancedInstanceConfig.IsNull() && node.AdvancedInstanceConfig.ValueString() != "" {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(node.AdvancedInstanceConfig.ValueString()), &advancedConfig); err == nil {
			config["advanced_configurations_json"] = advancedConfig
		}
	}

	// Add flags (JSON string)
	if !node.Flags.IsNull() && node.Flags.ValueString() != "" {
		var flags map[string]interface{}
		if err := json.Unmarshal([]byte(node.Flags.ValueString()), &flags); err == nil {
			config["flags"] = flags
		}
	}

	// Add cloud_deployment
	if !node.CloudDeployment.IsNull() {
		var cloudDep CloudDeploymentModel
		diags := node.CloudDeployment.As(ctx, &cloudDep, basetypes.ObjectAsOptions{})
		if !diags.HasError() {
			cloudDepMap := make(map[string]interface{})

			if !cloudDep.Provider.IsNull() {
				cloudDepMap["provider"] = cloudDep.Provider.ValueString()
			}
			if !cloudDep.Region.IsNull() {
				cloudDepMap["region"] = cloudDep.Region.ValueString()
			}
			if !cloudDep.MachinePool.IsNull() {
				cloudDepMap["machine_pool"] = cloudDep.MachinePool.ValueString()
			}
			if !cloudDep.ID.IsNull() {
				cloudDepMap["id"] = cloudDep.ID.ValueString()
			}

			if len(cloudDepMap) > 0 {
				config["cloud_deployment"] = cloudDepMap
			}
		}
	}

	return config, nil
}

// workerNodeConfigToAPI converts a worker_node object to API format
func workerNodeConfigToAPI(ctx context.Context, workerObj types.Object) (map[string]interface{}, error) {
	if workerObj.IsNull() || workerObj.IsUnknown() {
		return nil, nil
	}

	var worker WorkerNodeConfigModel
	diags := workerObj.As(ctx, &worker, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, fmt.Errorf("failed to convert worker node config: %v", diags)
	}

	// Start with base node config
	instanceType := worker.InstanceType.ValueString()
	config := map[string]interface{}{
		"instance_type": instanceType,
	}

	// Add worker-specific fields with API translations
	// Name: Default to instance type if not provided (per CLI behavior)
	if !worker.Name.IsNull() {
		config["name"] = worker.Name.ValueString()
	} else {
		config["name"] = instanceType
	}

	// Translate min_nodes → min_workers (per API schema)
	if !worker.MinNodes.IsNull() {
		config["min_workers"] = worker.MinNodes.ValueInt64()
	}

	// Translate max_nodes → max_workers (per API schema)
	if !worker.MaxNodes.IsNull() {
		config["max_workers"] = worker.MaxNodes.ValueInt64()
	}

	// Translate market_type → use_spot + fallback_to_ondemand (per CLI behavior)
	if !worker.MarketType.IsNull() {
		marketType := worker.MarketType.ValueString()
		switch marketType {
		case "SPOT":
			config["use_spot"] = true
			config["fallback_to_ondemand"] = false
		case "PREFER_SPOT":
			config["use_spot"] = true
			config["fallback_to_ondemand"] = true
		case "ON_DEMAND":
			config["use_spot"] = false
			config["fallback_to_ondemand"] = false
		}
	} else {
		// Default to ON_DEMAND
		config["use_spot"] = false
		config["fallback_to_ondemand"] = false
	}

	// Add resources
	if !worker.Resources.IsNull() {
		resourcesMap := make(map[string]interface{})
		elements := worker.Resources.Elements()
		for key, value := range elements {
			if float64Val, ok := value.(types.Float64); ok && !float64Val.IsNull() {
				resourcesMap[key] = float64Val.ValueFloat64()
			}
		}
		if len(resourcesMap) > 0 {
			config["resources"] = resourcesMap
		}
	}

	// Add required_resources
	if !worker.RequiredResources.IsNull() {
		var reqRes RequiredResourcesModel
		diags := worker.RequiredResources.As(ctx, &reqRes, basetypes.ObjectAsOptions{})
		if !diags.HasError() {
			reqResourcesMap := make(map[string]interface{})

			if !reqRes.CPU.IsNull() {
				reqResourcesMap["cpu"] = reqRes.CPU.ValueInt64()
			}
			if !reqRes.Memory.IsNull() {
				reqResourcesMap["memory"] = reqRes.Memory.ValueString()
			}
			if !reqRes.GPU.IsNull() {
				reqResourcesMap["gpu"] = reqRes.GPU.ValueInt64()
			}
			if !reqRes.Accelerator.IsNull() {
				reqResourcesMap["accelerator"] = reqRes.Accelerator.ValueString()
			}
			if !reqRes.TPU.IsNull() {
				reqResourcesMap["tpu"] = reqRes.TPU.ValueInt64()
			}
			if !reqRes.TPUHosts.IsNull() {
				reqResourcesMap["anyscale/tpu_hosts"] = reqRes.TPUHosts.ValueInt64()
			}

			if len(reqResourcesMap) > 0 {
				config["physical_resources"] = reqResourcesMap
			}
		}
	}

	// Add labels
	if !worker.Labels.IsNull() {
		labelsMap := make(map[string]interface{})
		elements := worker.Labels.Elements()
		for key, value := range elements {
			if strVal, ok := value.(types.String); ok && !strVal.IsNull() {
				labelsMap[key] = strVal.ValueString()
			}
		}
		if len(labelsMap) > 0 {
			config["labels"] = labelsMap
		}
	}

	// Add required_labels
	if !worker.RequiredLabels.IsNull() {
		reqLabelsMap := make(map[string]interface{})
		elements := worker.RequiredLabels.Elements()
		for key, value := range elements {
			if strVal, ok := value.(types.String); ok && !strVal.IsNull() {
				reqLabelsMap[key] = strVal.ValueString()
			}
		}
		if len(reqLabelsMap) > 0 {
			config["required_labels"] = reqLabelsMap
		}
	}

	// Add advanced_instance_config (JSON string) - map to API field name
	if !worker.AdvancedInstanceConfig.IsNull() && worker.AdvancedInstanceConfig.ValueString() != "" {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(worker.AdvancedInstanceConfig.ValueString()), &advancedConfig); err == nil {
			config["advanced_configurations_json"] = advancedConfig
		}
	}

	// Add flags (JSON string)
	if !worker.Flags.IsNull() && worker.Flags.ValueString() != "" {
		var flags map[string]interface{}
		if err := json.Unmarshal([]byte(worker.Flags.ValueString()), &flags); err == nil {
			config["flags"] = flags
		}
	}

	// Add cloud_deployment
	if !worker.CloudDeployment.IsNull() {
		var cloudDep CloudDeploymentModel
		diags := worker.CloudDeployment.As(ctx, &cloudDep, basetypes.ObjectAsOptions{})
		if !diags.HasError() {
			cloudDepMap := make(map[string]interface{})

			if !cloudDep.Provider.IsNull() {
				cloudDepMap["provider"] = cloudDep.Provider.ValueString()
			}
			if !cloudDep.Region.IsNull() {
				cloudDepMap["region"] = cloudDep.Region.ValueString()
			}
			if !cloudDep.MachinePool.IsNull() {
				cloudDepMap["machine_pool"] = cloudDep.MachinePool.ValueString()
			}
			if !cloudDep.ID.IsNull() {
				cloudDepMap["id"] = cloudDep.ID.ValueString()
			}

			if len(cloudDepMap) > 0 {
				config["cloud_deployment"] = cloudDepMap
			}
		}
	}

	return config, nil
}

// apiNodeTypeToTerraform converts an API head_node_type response to a Terraform types.Object
func apiNodeTypeToTerraform(ctx context.Context, apiNode map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	// Define the attribute types for NodeConfigModel
	nodeAttrTypes := map[string]attr.Type{
		"instance_type":            types.StringType,
		"resources":                types.MapType{ElemType: types.Float64Type},
		"required_resources":       requiredResourcesObjectType(),
		"labels":                   types.MapType{ElemType: types.StringType},
		"required_labels":          types.MapType{ElemType: types.StringType},
		"advanced_instance_config": types.StringType,
		"flags":                    types.StringType,
		"cloud_deployment":         cloudDeploymentObjectType(),
	}

	// Extract instance_type
	instanceType := types.StringNull()
	if it, ok := apiNode["instance_type"].(string); ok {
		instanceType = types.StringValue(it)
	}

	// Extract resources (logical resources)
	resources := types.MapNull(types.Float64Type)
	if res, ok := apiNode["resources"].(map[string]interface{}); ok {
		resourcesMap, resourcesDiags := apiResourcesToTerraformMap(ctx, res)
		diags.Append(resourcesDiags...)
		resources = resourcesMap
	}

	// Extract physical_resources (required_resources in Terraform)
	requiredResources := types.ObjectNull(requiredResourcesAttrTypes())
	if pr, ok := apiNode["physical_resources"].(map[string]interface{}); ok {
		reqResObj, reqResDiags := apiPhysicalResourcesToTerraform(ctx, pr)
		diags.Append(reqResDiags...)
		requiredResources = reqResObj
	}

	// Extract labels
	labels := types.MapNull(types.StringType)
	if lbl, ok := apiNode["labels"].(map[string]interface{}); ok {
		labelsMap, labelsDiags := InterfaceMapToString(ctx, lbl)
		diags.Append(labelsDiags...)
		labels = labelsMap
	}

	// Extract required_labels (if present in API response)
	requiredLabels := types.MapNull(types.StringType)
	if reqLbl, ok := apiNode["required_labels"].(map[string]interface{}); ok {
		reqLabelsMap, reqLabelsDiags := InterfaceMapToString(ctx, reqLbl)
		diags.Append(reqLabelsDiags...)
		requiredLabels = reqLabelsMap
	}

	// Extract advanced_instance_config from advanced_configurations_json
	advancedInstanceConfig := types.StringNull()
	if advConfig := getAdvancedConfigJSON(apiNode); advConfig != nil {
		if jsonBytes, err := json.Marshal(advConfig); err == nil {
			advancedInstanceConfig = types.StringValue(string(jsonBytes))
		}
	}

	// Extract flags (excluding cloud_deployment which is handled separately)
	flagsStr := types.StringNull()
	if flagsMap, ok := apiNode["flags"].(map[string]interface{}); ok {
		// Remove cloud_deployment from flags for separate handling
		flagsCopy := make(map[string]interface{})
		for k, v := range flagsMap {
			if k != "cloud_deployment" {
				flagsCopy[k] = v
			}
		}
		if len(flagsCopy) > 0 {
			if jsonBytes, err := json.Marshal(flagsCopy); err == nil {
				flagsStr = types.StringValue(string(jsonBytes))
			}
		}
	}

	// Extract cloud_deployment from flags (per CLI behavior)
	cloudDeployment := types.ObjectNull(cloudDeploymentAttrTypes())
	if flagsMap, ok := apiNode["flags"].(map[string]interface{}); ok {
		if cdMap, ok := flagsMap["cloud_deployment"].(map[string]interface{}); ok {
			cdObj, cdDiags := apiCloudDeploymentToTerraform(ctx, cdMap)
			diags.Append(cdDiags...)
			cloudDeployment = cdObj
		}
	}

	// Build the object
	nodeAttrs := map[string]attr.Value{
		"instance_type":            instanceType,
		"resources":                resources,
		"required_resources":       requiredResources,
		"labels":                   labels,
		"required_labels":          requiredLabels,
		"advanced_instance_config": advancedInstanceConfig,
		"flags":                    flagsStr,
		"cloud_deployment":         cloudDeployment,
	}

	nodeObj, objDiags := types.ObjectValue(nodeAttrTypes, nodeAttrs)
	diags.Append(objDiags...)

	return nodeObj, diags
}

// apiWorkerNodeTypesToTerraform converts API worker_node_types to a Terraform types.List
func apiWorkerNodeTypesToTerraform(ctx context.Context, apiWorkers []interface{}) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics

	workerAttrTypes := workerNodeConfigAttrTypes()

	if len(apiWorkers) == 0 {
		return types.ListNull(types.ObjectType{AttrTypes: workerAttrTypes}), diags
	}

	workerObjs := make([]attr.Value, 0, len(apiWorkers))

	for _, w := range apiWorkers {
		workerMap, ok := w.(map[string]interface{})
		if !ok {
			continue
		}

		workerObj, workerDiags := apiWorkerNodeTypeToTerraform(ctx, workerMap)
		diags.Append(workerDiags...)
		if !diags.HasError() {
			workerObjs = append(workerObjs, workerObj)
		}
	}

	workerList, listDiags := types.ListValue(types.ObjectType{AttrTypes: workerAttrTypes}, workerObjs)
	diags.Append(listDiags...)

	return workerList, diags
}

// apiWorkerNodeTypeToTerraform converts a single API worker_node_type to Terraform types.Object
func apiWorkerNodeTypeToTerraform(ctx context.Context, apiWorker map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	workerAttrTypes := workerNodeConfigAttrTypes()

	// Extract worker-specific fields
	name := types.StringNull()
	if n, ok := apiWorker["name"].(string); ok {
		name = types.StringValue(n)
	}

	// min_workers → min_nodes
	minNodes := types.Int64Value(0) // Default
	if mw, ok := apiWorker["min_workers"].(float64); ok {
		minNodes = types.Int64Value(int64(mw))
	}

	// max_workers → max_nodes
	maxNodes := types.Int64Value(10) // Default
	if mw, ok := apiWorker["max_workers"].(float64); ok {
		maxNodes = types.Int64Value(int64(mw))
	}

	// use_spot + fallback_to_ondemand → market_type
	marketType := types.StringValue("ON_DEMAND") // Default
	useSpot, hasSpot := apiWorker["use_spot"].(bool)
	fallback, hasFallback := apiWorker["fallback_to_ondemand"].(bool)
	if hasSpot && useSpot {
		if hasFallback && fallback {
			marketType = types.StringValue("PREFER_SPOT")
		} else {
			marketType = types.StringValue("SPOT")
		}
	}

	// Extract common node fields
	instanceType := types.StringNull()
	if it, ok := apiWorker["instance_type"].(string); ok {
		instanceType = types.StringValue(it)
	}

	resources := types.MapNull(types.Float64Type)
	if res, ok := apiWorker["resources"].(map[string]interface{}); ok {
		resourcesMap, resourcesDiags := apiResourcesToTerraformMap(ctx, res)
		diags.Append(resourcesDiags...)
		resources = resourcesMap
	}

	requiredResources := types.ObjectNull(requiredResourcesAttrTypes())
	if pr, ok := apiWorker["physical_resources"].(map[string]interface{}); ok {
		reqResObj, reqResDiags := apiPhysicalResourcesToTerraform(ctx, pr)
		diags.Append(reqResDiags...)
		requiredResources = reqResObj
	}

	labels := types.MapNull(types.StringType)
	if lbl, ok := apiWorker["labels"].(map[string]interface{}); ok {
		labelsMap, labelsDiags := InterfaceMapToString(ctx, lbl)
		diags.Append(labelsDiags...)
		labels = labelsMap
	}

	requiredLabels := types.MapNull(types.StringType)
	if reqLbl, ok := apiWorker["required_labels"].(map[string]interface{}); ok {
		reqLabelsMap, reqLabelsDiags := InterfaceMapToString(ctx, reqLbl)
		diags.Append(reqLabelsDiags...)
		requiredLabels = reqLabelsMap
	}

	advancedInstanceConfig := types.StringNull()
	if advConfig := getAdvancedConfigJSON(apiWorker); advConfig != nil {
		if jsonBytes, err := json.Marshal(advConfig); err == nil {
			advancedInstanceConfig = types.StringValue(string(jsonBytes))
		}
	}

	flagsStr := types.StringNull()
	if flagsMap, ok := apiWorker["flags"].(map[string]interface{}); ok {
		flagsCopy := make(map[string]interface{})
		for k, v := range flagsMap {
			if k != "cloud_deployment" {
				flagsCopy[k] = v
			}
		}
		if len(flagsCopy) > 0 {
			if jsonBytes, err := json.Marshal(flagsCopy); err == nil {
				flagsStr = types.StringValue(string(jsonBytes))
			}
		}
	}

	cloudDeployment := types.ObjectNull(cloudDeploymentAttrTypes())
	if flagsMap, ok := apiWorker["flags"].(map[string]interface{}); ok {
		if cdMap, ok := flagsMap["cloud_deployment"].(map[string]interface{}); ok {
			cdObj, cdDiags := apiCloudDeploymentToTerraform(ctx, cdMap)
			diags.Append(cdDiags...)
			cloudDeployment = cdObj
		}
	}

	// Build the worker object with all fields
	workerAttrs := map[string]attr.Value{
		"name":                     name,
		"min_nodes":                minNodes,
		"max_nodes":                maxNodes,
		"market_type":              marketType,
		"instance_type":            instanceType,
		"resources":                resources,
		"required_resources":       requiredResources,
		"labels":                   labels,
		"required_labels":          requiredLabels,
		"advanced_instance_config": advancedInstanceConfig,
		"flags":                    flagsStr,
		"cloud_deployment":         cloudDeployment,
	}

	workerObj, objDiags := types.ObjectValue(workerAttrTypes, workerAttrs)
	diags.Append(objDiags...)

	return workerObj, diags
}

// Helper functions for type definitions

func requiredResourcesObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: requiredResourcesAttrTypes()}
}

func requiredResourcesAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"cpu":         types.Int64Type,
		"memory":      types.StringType,
		"gpu":         types.Int64Type,
		"accelerator": types.StringType,
		"tpu":         types.Int64Type,
		"tpu_hosts":   types.Int64Type,
	}
}

func cloudDeploymentObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: cloudDeploymentAttrTypes()}
}

func cloudDeploymentAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"provider":     types.StringType,
		"region":       types.StringType,
		"machine_pool": types.StringType,
		"id":           types.StringType,
	}
}

func workerNodeConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":                     types.StringType,
		"min_nodes":                types.Int64Type,
		"max_nodes":                types.Int64Type,
		"market_type":              types.StringType,
		"instance_type":            types.StringType,
		"resources":                types.MapType{ElemType: types.Float64Type},
		"required_resources":       requiredResourcesObjectType(),
		"labels":                   types.MapType{ElemType: types.StringType},
		"required_labels":          types.MapType{ElemType: types.StringType},
		"advanced_instance_config": types.StringType,
		"flags":                    types.StringType,
		"cloud_deployment":         cloudDeploymentObjectType(),
	}
}

// apiResourcesToTerraformMap converts API resources to Terraform Map of Float64
func apiResourcesToTerraformMap(ctx context.Context, apiRes map[string]interface{}) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics

	if len(apiRes) == 0 {
		return types.MapNull(types.Float64Type), diags
	}

	// Convert API resources format to flat map
	// API format: {cpu: X, gpu: Y, memory: Z, object_store_memory: W, custom_resources: {...}}
	// Terraform format: {"CPU": X, "GPU": Y, "memory": Z, "object_store_memory": W, ...custom}
	flatMap := make(map[string]interface{})

	if cpu, ok := apiRes["cpu"].(float64); ok {
		flatMap["CPU"] = cpu
	}
	if gpu, ok := apiRes["gpu"].(float64); ok {
		flatMap["GPU"] = gpu
	}
	if memory, ok := apiRes["memory"].(float64); ok {
		flatMap["memory"] = memory
	}
	if osm, ok := apiRes["object_store_memory"].(float64); ok {
		flatMap["object_store_memory"] = osm
	}
	if custom, ok := apiRes["custom_resources"].(map[string]interface{}); ok {
		for k, v := range custom {
			if fv, ok := v.(float64); ok {
				flatMap[k] = fv
			}
		}
	}

	return InterfaceMapToFloat64(ctx, flatMap)
}

// apiPhysicalResourcesToTerraform converts API physical_resources to Terraform RequiredResources object
func apiPhysicalResourcesToTerraform(ctx context.Context, apiPR map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	cpu := types.Int64Null()
	if c, ok := apiPR["cpu"].(float64); ok {
		cpu = types.Int64Value(int64(c))
	}

	memory := types.StringNull()
	if m, ok := apiPR["memory"].(float64); ok {
		// Convert bytes to string representation
		memory = types.StringValue(fmt.Sprintf("%d", int64(m)))
	} else if m, ok := apiPR["memory"].(string); ok {
		memory = types.StringValue(m)
	}

	gpu := types.Int64Null()
	if g, ok := apiPR["gpu"].(float64); ok {
		gpu = types.Int64Value(int64(g))
	}

	accelerator := types.StringNull()
	if a, ok := apiPR["accelerator"].(string); ok {
		accelerator = types.StringValue(a)
	}

	tpu := types.Int64Null()
	if t, ok := apiPR["tpu"].(float64); ok {
		tpu = types.Int64Value(int64(t))
	}

	tpuHosts := types.Int64Null()
	// Check both API field names
	if th, ok := apiPR["anyscale/tpu_hosts"].(float64); ok {
		tpuHosts = types.Int64Value(int64(th))
	} else if th, ok := apiPR["anyscale_tpu_hosts"].(float64); ok {
		tpuHosts = types.Int64Value(int64(th))
	}

	attrs := map[string]attr.Value{
		"cpu":         cpu,
		"memory":      memory,
		"gpu":         gpu,
		"accelerator": accelerator,
		"tpu":         tpu,
		"tpu_hosts":   tpuHosts,
	}

	obj, objDiags := types.ObjectValue(requiredResourcesAttrTypes(), attrs)
	diags.Append(objDiags...)

	return obj, diags
}

// apiCloudDeploymentToTerraform converts API cloud_deployment to Terraform object
func apiCloudDeploymentToTerraform(ctx context.Context, apiCD map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	provider := types.StringNull()
	if p, ok := apiCD["provider"].(string); ok {
		provider = types.StringValue(p)
	}

	region := types.StringNull()
	if r, ok := apiCD["region"].(string); ok {
		region = types.StringValue(r)
	}

	machinePool := types.StringNull()
	if mp, ok := apiCD["machine_pool"].(string); ok {
		machinePool = types.StringValue(mp)
	}

	id := types.StringNull()
	if i, ok := apiCD["id"].(string); ok {
		id = types.StringValue(i)
	}

	attrs := map[string]attr.Value{
		"provider":     provider,
		"region":       region,
		"machine_pool": machinePool,
		"id":           id,
	}

	obj, objDiags := types.ObjectValue(cloudDeploymentAttrTypes(), attrs)
	diags.Append(objDiags...)

	return obj, diags
}

// getAdvancedConfigJSON extracts advanced configurations from API response
func getAdvancedConfigJSON(apiNode map[string]interface{}) map[string]interface{} {
	// Check advanced_configurations_json first
	if ac, ok := apiNode["advanced_configurations_json"].(map[string]interface{}); ok {
		return ac
	}
	// Fall back to cloud-specific fields
	if ac, ok := apiNode["aws_advanced_configurations_json"].(map[string]interface{}); ok {
		return ac
	}
	if ac, ok := apiNode["gcp_advanced_configurations_json"].(map[string]interface{}); ok {
		return ac
	}
	return nil
}
