package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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
	CloudID                types.String  `tfsdk:"cloud_id"`
	CloudName              types.String  `tfsdk:"cloud_name"`
	CloudResource          types.String  `tfsdk:"cloud_resource"` // Target specific cloud resource within a cloud
	Zones                  types.List    `tfsdk:"zones"`          // List of String
	MinResources           types.Map     `tfsdk:"min_resources"`  // Map of Float64
	MaxResources           types.Map     `tfsdk:"max_resources"`  // Map of Float64
	EnableCrossZoneScaling types.Bool    `tfsdk:"enable_cross_zone_scaling"`
	IdleTerminationMinutes types.Int64   `tfsdk:"idle_termination_minutes"`
	MaximumUptimeMinutes   types.Int64   `tfsdk:"maximum_uptime_minutes"`
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
	AdvancedInstanceConfig types.String `tfsdk:"advanced_instance_config"` // JSON string
	Flags                  types.String `tfsdk:"flags"`                    // JSON string
	CloudDeployment        types.Object `tfsdk:"cloud_deployment"`         // CloudDeploymentModel
}

// RequiredResourcesModel describes explicit hardware requirements for custom instances.
// Named required_resources to match the Anyscale API; the API rejects the older
// physical_resources key outright (see resource_compute_config_upgrade.go for
// migrating prior state that still has it).
type RequiredResourcesModel struct {
	CPU             types.Int64  `tfsdk:"cpu"`
	Memory          types.String `tfsdk:"memory"`
	GPU             types.Int64  `tfsdk:"gpu"`
	Accelerator     types.String `tfsdk:"accelerator"`
	TPU             types.Int64  `tfsdk:"tpu"`
	TPUHosts        types.Int64  `tfsdk:"tpu_hosts"`
	CPUArchitecture types.String `tfsdk:"cpu_architecture"`
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
	AdvancedInstanceConfig types.String `tfsdk:"advanced_instance_config"` // JSON string
	Flags                  types.String `tfsdk:"flags"`                    // JSON string
	CloudDeployment        types.Object `tfsdk:"cloud_deployment"`
}

type computeTemplateRequest struct {
	Name       string                `json:"name"`
	ProjectID  string                `json:"project_id,omitempty"`
	Config     computeTemplateConfig `json:"config"`
	Anonymous  bool                  `json:"anonymous"`
	NewVersion bool                  `json:"new_version"`
}

type computeTemplateConfig struct {
	CloudID                    string                         `json:"cloud_id"`
	DeploymentConfigs          []cloudDeploymentComputeConfig `json:"deployment_configs,omitempty"`
	AllowedAZs                 []string                       `json:"allowed_azs,omitempty"`
	HeadNodeType               map[string]interface{}         `json:"head_node_type,omitempty"`
	WorkerNodeTypes            []map[string]interface{}       `json:"worker_node_types,omitempty"`
	AutoSelectWorkerConfig     bool                           `json:"auto_select_worker_config,omitempty"`
	Flags                      map[string]interface{}         `json:"flags,omitempty"`
	AdvancedConfigurationsJSON map[string]interface{}         `json:"advanced_configurations_json,omitempty"`
	AWSAdvancedConfigurations  map[string]interface{}         `json:"aws_advanced_configurations_json,omitempty"`
	GCPAdvancedConfigurations  map[string]interface{}         `json:"gcp_advanced_configurations_json,omitempty"`
	IdleTerminationMinutes     *int64                         `json:"idle_termination_minutes,omitempty"`
	MaximumUptimeMinutes       *int64                         `json:"maximum_uptime_minutes,omitempty"`
}

type cloudDeploymentComputeConfig struct {
	CloudDeployment            string                   `json:"cloud_deployment,omitempty"`
	CloudResourceID            string                   `json:"cloud_resource_id,omitempty"`
	AllowedAZs                 []string                 `json:"allowed_azs,omitempty"`
	HeadNodeType               map[string]interface{}   `json:"head_node_type,omitempty"`
	WorkerNodeTypes            []map[string]interface{} `json:"worker_node_types,omitempty"`
	AdvancedConfigurationsJSON map[string]interface{}   `json:"advanced_configurations_json,omitempty"`
	AutoSelectWorkerConfig     bool                     `json:"auto_select_worker_config,omitempty"`
	Flags                      map[string]interface{}   `json:"flags,omitempty"`
}

type computeTemplateResponse struct {
	Result computeTemplate `json:"result"`
}

type computeTemplate struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Version        int64                 `json:"version"`
	CreatedAt      string                `json:"created_at"`
	LastModifiedAt string                `json:"last_modified_at"`
	ArchivedAt     string                `json:"archived_at"`
	Config         computeTemplateConfig `json:"config"`
}

func (r *ComputeConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_config"
}

func (r *ComputeConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Anyscale compute configuration for Ray clusters.",
		// Version 1: physical_resources was renamed to required_resources (head_node and
		// worker_nodes) to match the field the Anyscale API actually accepts. See UpgradeState.
		Version: 1,
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
				Description:         "The name of the compute config. Changing this replaces the resource: Anyscale compute configs are looked up by name, so a rename cannot be applied to the existing config and must create a new one.",
				MarkdownDescription: "The name of the compute config. Changing this replaces the resource: Anyscale compute configs are looked up by name, so a rename cannot be applied to the existing config and must create a new one.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Description:         "The ID of the Anyscale cloud to use for launching clusters. Either cloud_id or cloud_name must be specified. The cloud is immutable once set: changing it to a genuinely different cloud is rejected at apply time (see Update), since this resource cannot detect that change from a cloud_name lookup at plan time without a network call.",
				MarkdownDescription: "The ID of the Anyscale cloud to use for launching clusters. Either `cloud_id` or `cloud_name` must be specified. The cloud is immutable once set: changing it to a genuinely different cloud is rejected at apply time (see Update), since this resource cannot detect that change from a `cloud_name` lookup at plan time without a network call.",
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				Description:         "The name of the Anyscale cloud to use for launching clusters. Either cloud_id or cloud_name must be specified. If provided, will be resolved to cloud_id. The cloud is immutable once set; see cloud_id.",
				MarkdownDescription: "The name of the Anyscale cloud to use for launching clusters. Either `cloud_id` or `cloud_name` must be specified. If provided, will be resolved to cloud_id. The cloud is immutable once set; see `cloud_id`.",
			},
			"cloud_resource": schema.StringAttribute{
				Optional:            true,
				Description:         "The cloud resource to use for this workload. Defaults to the primary cloud resource of the Cloud. Use this to target a specific deployment within a cloud that has multiple resources.",
				MarkdownDescription: "The cloud resource to use for this workload. Defaults to the primary cloud resource of the Cloud. Use this to target a specific deployment within a cloud that has multiple resources.",
			},

			"zones": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "Availability zones to consider for this cluster. Defaults to all zones in the cloud's region.",
				MarkdownDescription: "Availability zones to consider for this cluster. Defaults to all zones in the cloud's region.",
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
			"idle_termination_minutes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				// No static Default: the backend defaults this to 120 on
				// create, but a static Default here would silently force an
				// existing config's real value (e.g. imported, or set before
				// this attribute existed) back to 120 on the next apply
				// whenever the user's config omits it -- the same
				// silent-overwrite class CC12 fixes for flags. UseStateForUnknown
				// plus populating from the API response in Create/Update
				// (mirroring Read) is the correct idiom for a server-defaulted
				// value: it reflects whatever the backend actually set, once,
				// and then holds steady across plans that do not touch it.
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
				Validators: []validator.Int64{
					int64validator.AtLeast(0),
				},
				Description:         "Number of minutes after which idle clusters using this compute config will be terminated. 0 disables idle termination. Defaults to the backend's own default (120) when unset.",
				MarkdownDescription: "Number of minutes after which idle clusters using this compute config will be terminated. `0` disables idle termination. Defaults to the backend's own default (120) when unset.",
			},
			"maximum_uptime_minutes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
				Description:         "Maximum uptime in minutes before clusters using this compute config are forcibly terminated. Unset means no maximum.",
				MarkdownDescription: "Maximum uptime in minutes before clusters using this compute config are forcibly terminated. Unset means no maximum.",
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
			Computed:            true,
			Description:         "Logical resources that will be available on this node. Defaults to match the physical resources of the instance type.",
			MarkdownDescription: "Logical resources that will be available on this node. Defaults to match the physical resources of the instance type.",
			PlanModifiers: []planmodifier.Map{
				mapplanmodifier.UseStateForUnknown(),
			},
		},
		"required_resources": schema.SingleNestedAttribute{
			Optional:            true,
			Description:         "Explicit hardware requirements for custom instance types (free pod shapes). Explicitly defines CPU, memory, and GPU resources.",
			MarkdownDescription: "Explicit hardware requirements for custom instance types (free pod shapes). Explicitly defines CPU, memory, and GPU resources.",
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
				"cpu_architecture": schema.StringAttribute{
					Optional:            true,
					Description:         "CPU architecture to select, e.g. x86_64 or arm64. Defaults to x86_64 when unset.",
					MarkdownDescription: "CPU architecture to select, e.g. `x86_64` or `arm64`. Defaults to `x86_64` when unset.",
				},
			},
		},
		"labels": schema.MapAttribute{
			ElementType:         types.StringType,
			Optional:            true,
			Description:         "Labels to associate the node with for scheduling purposes.",
			MarkdownDescription: "Labels to associate the node with for scheduling purposes.",
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
		Computed:            true,
		Description:         "Unique name of this worker group. Defaults to a human-friendly representation of the instance type.",
		MarkdownDescription: "Unique name of this worker group. Defaults to a human-friendly representation of the instance type.",
		PlanModifiers: []planmodifier.String{
			// UseNonNullStateForUnknown, not UseStateForUnknown: name is an
			// attribute nested inside a list element (worker_nodes), and a
			// brand-new element added by this plan has no corresponding prior
			// state at its index. Plain UseStateForUnknown copies that missing
			// state's null straight into the plan, so a genuinely new worker
			// group's name plans as null instead of unknown - the API then
			// returns a real value and Terraform rejects the apply as
			// inconsistent. UseNonNullStateForUnknown leaves it unknown
			// instead when there is no non-null prior value to reuse.
			stringplanmodifier.UseNonNullStateForUnknown(),
		},
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
) (*computeTemplateRequest, string) {
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

	createConfig := computeTemplateConfig{
		CloudID: cloudID,
	}

	// CC2: top-level only, same as the Read side -- never per-deployment.
	// Guard on IsUnknown too, not just IsNull: both attributes are
	// Optional+Computed with UseStateForUnknown and no static Default, so an
	// omitted value is Unknown (not Null) whenever there is no prior state to
	// carry forward (i.e. on Create). Sending ValueInt64() of an Unknown value
	// would marshal a meaningless 0 instead of just omitting the field.
	if !plan.IdleTerminationMinutes.IsNull() && !plan.IdleTerminationMinutes.IsUnknown() {
		idleMinutes := plan.IdleTerminationMinutes.ValueInt64()
		createConfig.IdleTerminationMinutes = &idleMinutes
	}
	if !plan.MaximumUptimeMinutes.IsNull() && !plan.MaximumUptimeMinutes.IsUnknown() {
		maxUptimeMinutes := plan.MaximumUptimeMinutes.ValueInt64()
		createConfig.MaximumUptimeMinutes = &maxUptimeMinutes
	}

	var zones []string
	if !plan.Zones.IsNull() {
		zonesResult, zonesDiags := StringListToInterface(ctx, plan.Zones)
		diags.Append(zonesDiags...)
		if diags.HasError() {
			return nil, ""
		}
		if len(zonesResult) > 0 {
			zones = zonesResult
			createConfig.AllowedAZs = zonesResult
		}
	}

	autoSelectWorkerConfig := false
	if !plan.AutoSelectWorkerConfig.IsNull() {
		autoSelectWorkerConfig = plan.AutoSelectWorkerConfig.ValueBool()
		createConfig.AutoSelectWorkerConfig = autoSelectWorkerConfig
	}

	if !plan.AdvancedInstanceConfig.IsNull() {
		advancedConfig, err := DynamicToInterface(ctx, plan.AdvancedInstanceConfig)
		if err != nil {
			AddConfigError(diags, "Failed to Convert Advanced Instance Config", err.Error())
			return nil, ""
		}
		if advancedConfig != nil {
			createConfig.AdvancedConfigurationsJSON = advancedConfig
		}
	}

	flags := make(map[string]interface{})
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
	}

	if !plan.EnableCrossZoneScaling.IsNull() {
		flags["allow-cross-zone-autoscaling"] = plan.EnableCrossZoneScaling.ValueBool()
	}

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
		createConfig.Flags = flags
	}

	if !plan.HeadNode.IsNull() {
		headNodeConfig, err := nodeConfigToAPI(ctx, plan.HeadNode)
		if err != nil {
			AddConfigError(diags, "Failed to Convert Head Node", err.Error())
			return nil, ""
		}
		if headNodeConfig != nil {
			createConfig.HeadNodeType = headNodeConfig
		}
	}

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
			createConfig.WorkerNodeTypes = workerConfigs
		}
	}

	deploymentConfig := cloudDeploymentComputeConfig{
		AllowedAZs:                 zones,
		HeadNodeType:               createConfig.HeadNodeType,
		WorkerNodeTypes:            createConfig.WorkerNodeTypes,
		AutoSelectWorkerConfig:     autoSelectWorkerConfig,
		Flags:                      createConfig.Flags,
		AdvancedConfigurationsJSON: createConfig.AdvancedConfigurationsJSON,
	}

	if !plan.CloudResource.IsNull() {
		deploymentConfig.CloudDeployment = plan.CloudResource.ValueString()
	}

	createConfig.DeploymentConfigs = []cloudDeploymentComputeConfig{deploymentConfig}

	createRequest := &computeTemplateRequest{
		Name:       plan.Name.ValueString(),
		Config:     createConfig,
		Anonymous:  false,
		NewVersion: true,
	}

	return createRequest, cloudID
}

func (r *ComputeConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ComputeConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Capture what the user actually configured before it's overwritten below,
	// so head_node/worker_nodes' Computed sub-attributes (e.g. resources, which
	// the API auto-fills from instance_type) can be masked back to null when
	// the user did not set them - mirroring Read's prior-state masking, using
	// the plan itself as "prior" since this is the resource's first apply.
	priorHeadNode := plan.HeadNode
	priorWorkerNodes := plan.WorkerNodes

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

	log.Printf("[DEBUG] POST /api/v2/compute_templates/ - Creating compute config")

	apiResult, err := DoRequestAndParse[computeTemplateResponse](
		ctx, r.client, "POST", "/api/v2/compute_templates/", reqBody,
		http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "create compute config", err)
		return
	}

	resultData := apiResult.Result

	if resultData.ID == "" {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return an ID")
		return
	}

	plan.ID = types.StringValue(resultData.Name)
	plan.ConfigID = types.StringValue(resultData.ID)
	log.Printf("[INFO] Created compute config: name=%s, config_id=%s", resultData.Name, resultData.ID)

	if resultData.Version > 0 {
		plan.Version = types.Int64Value(resultData.Version)
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", resultData.Name, resultData.Version))
	}
	if resultData.CreatedAt != "" {
		plan.CreatedAt = types.StringValue(resultData.CreatedAt)
	}
	if resultData.LastModifiedAt != "" {
		plan.LastModifiedAt = types.StringValue(resultData.LastModifiedAt)
	}

	// head_node/worker_nodes are Required/Optional blocks, but sub-attributes
	// like resources are Optional+Computed (the API fills them in from
	// instance_type); idle_termination_minutes/maximum_uptime_minutes are the
	// same story at the top level. Populate all of them from the create
	// response the same way Read does, or they are left Unknown and Terraform
	// rejects the apply with "Provider returned invalid result object".
	populateComputedFieldsFromResponse(ctx, resultData.Config, priorHeadNode, priorWorkerNodes, &plan, &resp.Diagnostics)

	// Set state with all fields populated
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// populateComputedFieldsFromResponse resolves the Computed fields a
// create/update API response is the only source of truth for: head_node and
// worker_nodes' Computed sub-attributes (name, resources, ...), masked
// against prior the same way Read does, plus the top-level
// idle_termination_minutes/maximum_uptime_minutes (CC2). Both Create and
// Update must call this: unlike Read, they have no earlier chance to observe
// these values, and any Computed attribute left Unknown when state is Set
// causes Terraform to reject the apply with "Provider produced inconsistent
// result after apply".
func populateComputedFieldsFromResponse(
	ctx context.Context,
	configData computeTemplateConfig,
	priorHeadNode types.Object,
	priorWorkerNodes types.List,
	plan *ComputeConfigResourceModel,
	diags *diag.Diagnostics,
) {
	// CC2: top-level only, same as the Read side -- never per-deployment.
	// Explicit else-Null (not just "leave it"), mirroring Read exactly: both
	// attributes are Optional+Computed+UseStateForUnknown with no Default, so
	// an Unknown left unresolved here (e.g. a defensive nil from the API that
	// should not happen given the backend's own idle default, but costs
	// nothing to handle) would hit the identical "Provider produced
	// inconsistent result after apply" this function exists to prevent.
	if configData.IdleTerminationMinutes != nil {
		plan.IdleTerminationMinutes = types.Int64Value(*configData.IdleTerminationMinutes)
	} else {
		plan.IdleTerminationMinutes = types.Int64Null()
	}
	if configData.MaximumUptimeMinutes != nil {
		plan.MaximumUptimeMinutes = types.Int64Value(*configData.MaximumUptimeMinutes)
	} else {
		plan.MaximumUptimeMinutes = types.Int64Null()
	}

	eff := resolveEffectiveComputeConfig(configData)

	if eff.HeadNodeType != nil {
		headNodeObj, headNodeDiags := apiNodeTypeToTerraform(ctx, eff.HeadNodeType)
		diags.Append(headNodeDiags...)
		if !diags.HasError() {
			plan.HeadNode = maskNodeFromPrior(ctx, headNodeObj, priorHeadNode, diags)
		}
	}

	if len(eff.WorkerNodeTypes) > 0 {
		workerInterfaces := make([]interface{}, 0, len(eff.WorkerNodeTypes))
		for _, worker := range eff.WorkerNodeTypes {
			workerInterfaces = append(workerInterfaces, worker)
		}
		workerNodesList, workerNodesDiags := apiWorkerNodeTypesToTerraform(ctx, workerInterfaces)
		diags.Append(workerNodesDiags...)
		if !diags.HasError() {
			plan.WorkerNodes = maskWorkerNodesFromPrior(ctx, workerNodesList, priorWorkerNodes, diags)
		}
	}
}

func (r *ComputeConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ComputeConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Capture the prior nested objects so we can mask API-normalized defaults
	// (e.g. resources/required_resources auto-filled from instance_type) back to
	// null when the user did not explicitly set them.
	priorHeadNode := state.HeadNode
	priorWorkerNodes := state.WorkerNodes
	priorMinResources := state.MinResources
	priorMaxResources := state.MaxResources

	// Use ConfigID for API lookup (version-specific ID)
	// Fall back to ID if ConfigID is not set (for backwards compatibility or import)
	lookupID := state.ConfigID.ValueString()
	if lookupID == "" {
		lookupID = state.ID.ValueString()
	}

	// Make API call to get compute config
	apiResult, err := DoRequestAndParse[computeTemplateResponse](
		ctx, r.client, "GET", fmt.Sprintf("/api/v2/compute_templates/%s", lookupID), nil,
		http.StatusOK, http.StatusNotFound,
	)
	if err != nil {
		if apiResult == nil {
			log.Printf("[WARN] Compute config not found, removing from state: config_id=%s", lookupID)
			resp.State.RemoveResource(ctx)
			return
		}
		AddAPIError(&resp.Diagnostics, "read compute config", err)
		return
	}

	resultData := apiResult.Result

	// CC11: a config archived out of band (e.g. via the console, or archived
	// alongside a rename replace) returns 200 with archived_at populated, not
	// a 404 -- without this check it would linger in state forever instead of
	// planning a clean recreate, same as the existing 404/removed path below.
	if resultData.ArchivedAt != "" {
		log.Printf("[WARN] Compute config archived, removing from state: config_id=%s", lookupID)
		resp.State.RemoveResource(ctx)
		return
	}

	if resultData.ID != "" {
		state.ConfigID = types.StringValue(resultData.ID)
	}

	if resultData.Name != "" {
		state.Name = types.StringValue(resultData.Name)
		state.ID = types.StringValue(resultData.Name)
	}

	if resultData.Version > 0 {
		state.Version = types.Int64Value(resultData.Version)
		state.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", resultData.Name, resultData.Version))
	}

	if resultData.CreatedAt != "" {
		state.CreatedAt = types.StringValue(resultData.CreatedAt)
	}

	if resultData.LastModifiedAt != "" {
		state.LastModifiedAt = types.StringValue(resultData.LastModifiedAt)
	}

	configData := resultData.Config
	if configData.CloudID != "" {
		state.CloudID = types.StringValue(configData.CloudID)
	}

	// CC2: idle_termination_minutes/maximum_uptime_minutes are TOP-LEVEL config
	// fields only -- the API never places them on a per-deployment override,
	// unlike flags/head_node/worker_nodes below, so they are read directly off
	// configData rather than through resolveEffectiveComputeConfig.
	if configData.IdleTerminationMinutes != nil {
		state.IdleTerminationMinutes = types.Int64Value(*configData.IdleTerminationMinutes)
	} else {
		state.IdleTerminationMinutes = types.Int64Null()
	}
	if configData.MaximumUptimeMinutes != nil {
		state.MaximumUptimeMinutes = types.Int64Value(*configData.MaximumUptimeMinutes)
	} else {
		state.MaximumUptimeMinutes = types.Int64Null()
	}

	eff := resolveEffectiveComputeConfig(configData)
	if eff.CloudDeployment != "" {
		state.CloudResource = types.StringValue(eff.CloudDeployment)
	}

	if len(eff.AllowedAZs) > 0 {
		if len(eff.AllowedAZs) == 1 && strings.EqualFold(eff.AllowedAZs[0], "any") {
			state.Zones = types.ListNull(types.StringType)
		} else {
			allowedAZInterfaces := make([]interface{}, 0, len(eff.AllowedAZs))
			for _, az := range eff.AllowedAZs {
				allowedAZInterfaces = append(allowedAZInterfaces, az)
			}
			zonesList, diags := InterfaceListToString(ctx, allowedAZInterfaces)
			resp.Diagnostics.Append(diags...)
			state.Zones = zonesList
		}
	}

	state.AutoSelectWorkerConfig = types.BoolValue(eff.AutoSelect)

	if eff.Flags != nil {
		if minResources, ok := eff.Flags["min_resources"].(map[string]interface{}); ok {
			minResourcesMap, diags := InterfaceMapToFloat64(ctx, minResources)
			resp.Diagnostics.Append(diags...)
			state.MinResources = restoreMapKeyCasing(ctx, minResourcesMap, priorMinResources)
		}

		if maxResources, ok := eff.Flags["max_resources"].(map[string]interface{}); ok {
			maxResourcesMap, diags := InterfaceMapToFloat64(ctx, maxResources)
			resp.Diagnostics.Append(diags...)
			state.MaxResources = restoreMapKeyCasing(ctx, maxResourcesMap, priorMaxResources)
		}

		if enableCrossZone, ok := eff.Flags["allow-cross-zone-autoscaling"].(bool); ok {
			state.EnableCrossZoneScaling = types.BoolValue(enableCrossZone)
		}
	}

	// NOTE: We intentionally do NOT read user-defined flags from the API response
	// The flags field should only reflect what's in the user's configuration
	// We extract special flags (min_resources, max_resources, allow-cross-zone-autoscaling) above,
	// but user's custom flags are preserved as-is from their configuration.
	//
	// CC12: the one exception is ImportState, which populates flags and
	// advanced_instance_config (top-level and per-node) directly from the API
	// exactly once, since that is the only point where recovered-at-import and
	// genuinely-never-configured are not ambiguous. Read leaves them on prior
	// state untouched either way, so whatever ImportState seeds here persists
	// through every later refresh.

	if eff.HeadNodeType != nil {
		headNodeObj, headNodeDiags := apiNodeTypeToTerraform(ctx, eff.HeadNodeType)
		resp.Diagnostics.Append(headNodeDiags...)
		if !resp.Diagnostics.HasError() {
			state.HeadNode = maskNodeFromPrior(ctx, headNodeObj, priorHeadNode, &resp.Diagnostics)
		}
	}

	if len(eff.WorkerNodeTypes) > 0 {
		workerInterfaces := make([]interface{}, 0, len(eff.WorkerNodeTypes))
		for _, worker := range eff.WorkerNodeTypes {
			workerInterfaces = append(workerInterfaces, worker)
		}
		workerNodesList, workerNodesDiags := apiWorkerNodeTypesToTerraform(ctx, workerInterfaces)
		resp.Diagnostics.Append(workerNodesDiags...)
		if !resp.Diagnostics.HasError() {
			state.WorkerNodes = maskWorkerNodesFromPrior(ctx, workerNodesList, priorWorkerNodes, &resp.Diagnostics)
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// maskNodeFromPrior preserves null on nested node attributes (resources,
// required_resources, labels, advanced_instance_config, flags, cloud_deployment)
// that were null in the prior state. The Anyscale API auto-fills these from the
// instance_type which would otherwise cause drift when the user did not set them.
func maskNodeFromPrior(ctx context.Context, apiNode types.Object, priorNode types.Object, diags *diag.Diagnostics) types.Object {
	if priorNode.IsNull() || priorNode.IsUnknown() || apiNode.IsNull() {
		return apiNode
	}

	priorAttrs := priorNode.Attributes()
	apiAttrs := apiNode.Attributes()
	masked := make(map[string]attr.Value, len(apiAttrs))
	for k, v := range apiAttrs {
		masked[k] = v
	}

	for _, name := range []string{"resources", "required_resources", "labels", "advanced_instance_config", "flags", "cloud_deployment"} {
		if prior, ok := priorAttrs[name]; ok && prior != nil && prior.IsNull() {
			if apiVal, ok := masked[name]; ok {
				masked[name] = nullValueOf(apiVal)
			}
		}
	}

	// resourceMapToAPI canonicalizes well-known resource keys to lowercase
	// (cpu/gpu/memory/object_store_memory) before sending, so a configured
	// "CPU" round-trips from the API as "cpu". Restore the user's casing here
	// instead of at the request layer, so state matches plan.
	if priorResources, ok := priorAttrs["resources"].(types.Map); ok {
		if apiResources, ok := masked["resources"].(types.Map); ok {
			masked["resources"] = restoreMapKeyCasing(ctx, apiResources, priorResources)
		}
	}

	obj, objDiags := types.ObjectValue(apiNode.AttributeTypes(ctx), masked)
	diags.Append(objDiags...)
	return obj
}

// restoreMapKeyCasing returns apiMap with each key's casing replaced by the
// case-insensitively matching key from priorMap, where one exists. Anyscale's
// API normalizes some map keys (e.g. resource type names) regardless of how
// they were configured; without this, state drifts from plan on every read.
// Keys with no case-insensitive match in priorMap (new keys, or import with no
// prior to match against) are left as the API returned them.
func restoreMapKeyCasing(ctx context.Context, apiMap types.Map, priorMap types.Map) types.Map {
	if apiMap.IsNull() || apiMap.IsUnknown() || priorMap.IsNull() || priorMap.IsUnknown() {
		return apiMap
	}

	priorElems := priorMap.Elements()
	if len(priorElems) == 0 {
		return apiMap
	}

	priorCasing := make(map[string]string, len(priorElems))
	for k := range priorElems {
		priorCasing[strings.ToLower(k)] = k
	}

	apiElems := apiMap.Elements()
	restored := make(map[string]attr.Value, len(apiElems))
	for k, v := range apiElems {
		if orig, ok := priorCasing[strings.ToLower(k)]; ok {
			restored[orig] = v
		} else {
			restored[k] = v
		}
	}

	mapVal, mapDiags := types.MapValue(apiMap.ElementType(ctx), restored)
	if mapDiags.HasError() {
		return apiMap
	}
	return mapVal
}

// maskWorkerNodesFromPrior applies maskNodeFromPrior elementwise on the
// worker_nodes list, matching prior elements by index.
func maskWorkerNodesFromPrior(ctx context.Context, apiWorkers types.List, priorWorkers types.List, diags *diag.Diagnostics) types.List {
	if priorWorkers.IsNull() || priorWorkers.IsUnknown() || apiWorkers.IsNull() {
		return apiWorkers
	}

	apiElems := apiWorkers.Elements()
	priorElems := priorWorkers.Elements()
	if len(apiElems) == 0 {
		return apiWorkers
	}

	masked := make([]attr.Value, 0, len(apiElems))
	for i, apiVal := range apiElems {
		apiObj, ok := apiVal.(types.Object)
		if !ok {
			masked = append(masked, apiVal)
			continue
		}
		var priorObj types.Object
		if i < len(priorElems) {
			if obj, ok := priorElems[i].(types.Object); ok {
				priorObj = obj
			}
		}
		masked = append(masked, maskNodeFromPrior(ctx, apiObj, priorObj, diags))
	}

	listVal, listDiags := types.ListValue(apiWorkers.ElementType(ctx), masked)
	diags.Append(listDiags...)
	return listVal
}

// nullValueOf returns a typed null value matching the type of v.
func nullValueOf(v attr.Value) attr.Value {
	switch t := v.(type) {
	case types.Map:
		return types.MapNull(t.ElementType(context.Background()))
	case types.List:
		return types.ListNull(t.ElementType(context.Background()))
	case types.Object:
		return types.ObjectNull(t.AttributeTypes(context.Background()))
	case types.String:
		return types.StringNull()
	case types.Bool:
		return types.BoolNull()
	case types.Int64:
		return types.Int64Null()
	case types.Float64:
		return types.Float64Null()
	default:
		return v
	}
}

func (r *ComputeConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ComputeConfigResourceModel
	var state ComputeConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Capture what the user configured before it's overwritten below, same
	// reasoning as Create: this is what tells populateComputedFieldsFromResponse
	// which Computed sub-attributes (resources, ...) the user left unset.
	priorHeadNode := plan.HeadNode
	priorWorkerNodes := plan.WorkerNodes

	// Anyscale compute configs support versioning - updates create a new version with the same name.
	// The API handles this via POST with new_version=true and the same name.
	// This gives us a new ID and incremented version number.

	tflog.Info(ctx, "Updating compute config by creating new version", map[string]any{
		"name":          plan.Name.ValueString(),
		"old_config_id": state.ConfigID.ValueString(),
		"old_version":   state.Version.ValueInt64(),
	})

	// Build the request using the same helper as Create. This also resolves
	// plan.CloudID to its effective value below, whether the user configured
	// cloud_id or cloud_name directly.
	updateRequest, _ := r.buildComputeConfigRequest(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// CC3b: the cloud is immutable in place. A compute config's identity is
	// tied to the cloud it was created under; unlike Cloud resources, there is
	// no per-field PATCH here, so an in-place cloud change would silently
	// create a new version under the NEW cloud while leaving the old
	// version's cloud unmanaged and unaware anything moved -- the same shape
	// of orphan CC3a fixes for renames, just not detectable at plan time,
	// since only an apply-time lookup can resolve what cloud_name resolves
	// to. buildComputeConfigRequest has already resolved plan.CloudID to its
	// effective value above (whether the user configured cloud_id or
	// cloud_name), so this comparison also correctly does NOT fire when a
	// user merely switches which of the two attributes they reference the
	// same cloud by -- the resolved ID is identical either way.
	if !state.CloudID.IsNull() && plan.CloudID.ValueString() != state.CloudID.ValueString() {
		AddConfigError(&resp.Diagnostics,
			"Compute Config Cloud Is Immutable",
			fmt.Sprintf(
				"This compute config is on cloud %q and cannot be moved to a different cloud in place: doing so would silently create a new version under the new cloud while leaving the existing version's cloud unmanaged. To intentionally move this compute config to a different cloud, replace the resource instead (terraform apply -replace, or taint it before applying).",
				state.CloudID.ValueString(),
			),
		)
		return
	}

	// Make API call to create new version
	reqBody, err := MarshalRequestBody(updateRequest)
	if err != nil {
		AddJSONError(&resp.Diagnostics, "marshal", "compute config update request", err)
		return
	}

	log.Printf("[DEBUG] POST /api/v2/compute_templates/ - Creating new version of compute config")

	apiResult, err := DoRequestAndParse[computeTemplateResponse](
		ctx, r.client, "POST", "/api/v2/compute_templates/", reqBody,
		http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "update compute config (create new version)", err)
		return
	}

	resultData := apiResult.Result

	if resultData.ID == "" {
		AddConfigError(&resp.Diagnostics, "Invalid Response", "API did not return an ID for the new version")
		return
	}

	tflog.Info(ctx, "Created new compute config version", map[string]any{
		"name":          resultData.Name,
		"new_config_id": resultData.ID,
		"new_version":   resultData.Version,
	})

	plan.ID = types.StringValue(resultData.Name)
	plan.ConfigID = types.StringValue(resultData.ID)
	if resultData.Version > 0 {
		plan.Version = types.Int64Value(resultData.Version)
		plan.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", resultData.Name, resultData.Version))
	}
	if resultData.CreatedAt != "" {
		plan.CreatedAt = types.StringValue(resultData.CreatedAt)
	}
	if resultData.LastModifiedAt != "" {
		plan.LastModifiedAt = types.StringValue(resultData.LastModifiedAt)
	}

	// Same as Create: resolve head_node/worker_nodes' Computed sub-attributes
	// (and idle_termination_minutes/maximum_uptime_minutes) from the response,
	// or a value left Unknown (e.g. a brand-new nameless worker group added in
	// this update, or max_uptime omitted entirely) makes Terraform reject the
	// apply.
	populateComputedFieldsFromResponse(ctx, resultData.Config, priorHeadNode, priorWorkerNodes, &plan, &resp.Diagnostics)

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

	log.Printf("[INFO] Archiving compute config: name=%s, config_id=%s", state.Name.ValueString(), configID)

	_, err := DoRequestRaw(
		ctx, r.client, "POST", fmt.Sprintf("/api/v2/compute_templates/%s/archive", configID), nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "delete compute config", err)
		return
	}

	log.Printf("[INFO] Archived compute config successfully")
}

func (r *ComputeConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import accepts the version-specific config ID (e.g., "cpt_xxx")
	// We set it as config_id, and Read will populate id (name) from the API response
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("config_id"), req.ID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// CC12: fetch the config once, here, to recover the write-only fields
	// (flags, advanced_instance_config -- top-level and per-node) that
	// ordinary Read intentionally never reads back (see the NOTE comments in
	// Read). Import is the one place recovering them is unambiguous: there is
	// no prior state yet to confuse "recovered at import" with "genuinely
	// never configured", and Read always preserves whatever these fields
	// already say in prior state, so whatever is seeded here survives every
	// later refresh untouched.
	//
	// CC11: this fetch doubles as an early, clear rejection of importing an
	// already-archived config, instead of importing a phantom that Read would
	// silently remove on the very next refresh.
	apiResult, err := DoRequestAndParse[computeTemplateResponse](
		ctx, r.client, "GET", fmt.Sprintf("/api/v2/compute_templates/%s", req.ID), nil,
		http.StatusOK, http.StatusNotFound,
	)
	if err != nil {
		if apiResult == nil {
			AddConfigError(&resp.Diagnostics, "Compute Config Not Found",
				fmt.Sprintf("No compute config exists with ID %q.", req.ID))
			return
		}
		AddAPIError(&resp.Diagnostics, "import compute config", err)
		return
	}

	resultData := apiResult.Result
	if resultData.ArchivedAt != "" {
		AddConfigError(&resp.Diagnostics, "Compute Config Archived",
			fmt.Sprintf("Compute config %q has been archived and cannot be imported.", req.ID))
		return
	}

	eff := resolveEffectiveComputeConfig(resultData.Config)

	// Top-level flags: recover everything except the keys that surface as
	// their own attributes (min_resources, max_resources, cross-zone
	// scaling), matching the remainder Read always leaves for the user.
	if eff.Flags != nil {
		userFlags := make(map[string]interface{}, len(eff.Flags))
		for k, v := range eff.Flags {
			switch k {
			case "min_resources", "max_resources", "allow-cross-zone-autoscaling":
				continue
			default:
				userFlags[k] = v
			}
		}
		if len(userFlags) > 0 {
			flagsDynamic, err := InterfaceToDynamic(ctx, userFlags)
			if err != nil {
				AddConfigError(&resp.Diagnostics, "Failed to Recover Flags", err.Error())
				return
			}
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("flags"), flagsDynamic)...)
		}
	}

	if len(eff.AdvancedConfig) > 0 {
		advDynamic, err := InterfaceToDynamic(ctx, eff.AdvancedConfig)
		if err != nil {
			AddConfigError(&resp.Diagnostics, "Failed to Recover Advanced Instance Config", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("advanced_instance_config"), advDynamic)...)
	}

	// Per-node flags/advanced_instance_config: apiNodeTypeToTerraform and
	// apiWorkerNodeTypeToTerraform already extract these from the live API
	// response as real values -- Read only ever loses them via
	// maskNodeFromPrior, which nulls them because prior state was null. Seed
	// full node objects here, but null the OTHER ambiguous sub-attributes
	// (resources, required_resources, labels, cloud_deployment) that Read
	// would still want to treat as unconfigured absent a real prior to check.
	if eff.HeadNodeType != nil {
		headNodeObj, headNodeDiags := apiNodeTypeToTerraform(ctx, eff.HeadNodeType)
		resp.Diagnostics.Append(headNodeDiags...)
		if !resp.Diagnostics.HasError() {
			headNodeObj = nullAmbiguousImportFields(ctx, headNodeObj, &resp.Diagnostics)
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("head_node"), headNodeObj)...)
		}
	}

	if len(eff.WorkerNodeTypes) > 0 {
		workerInterfaces := make([]interface{}, 0, len(eff.WorkerNodeTypes))
		for _, worker := range eff.WorkerNodeTypes {
			workerInterfaces = append(workerInterfaces, worker)
		}
		workerNodesList, workerNodesDiags := apiWorkerNodeTypesToTerraform(ctx, workerInterfaces)
		resp.Diagnostics.Append(workerNodesDiags...)
		if !resp.Diagnostics.HasError() {
			workerNodesList = nullAmbiguousImportFieldsList(ctx, workerNodesList, &resp.Diagnostics)
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("worker_nodes"), workerNodesList)...)
		}
	}
}

// Helper functions for converting nested objects

func resourceMapToAPI(resources types.Map) map[string]interface{} {
	if resources.IsNull() || resources.IsUnknown() {
		return nil
	}

	elements := resources.Elements()
	if len(elements) == 0 {
		return nil
	}

	apiResources := make(map[string]interface{})
	customResources := make(map[string]interface{})

	for key, value := range elements {
		floatValue, ok := value.(types.Float64)
		if !ok || floatValue.IsNull() {
			continue
		}

		switch strings.ToLower(key) {
		case "cpu":
			apiResources["cpu"] = floatValue.ValueFloat64()
		case "gpu":
			apiResources["gpu"] = floatValue.ValueFloat64()
		case "memory":
			apiResources["memory"] = floatValue.ValueFloat64()
		case "object_store_memory":
			apiResources["object_store_memory"] = floatValue.ValueFloat64()
		default:
			customResources[key] = floatValue.ValueFloat64()
		}
	}

	if len(customResources) > 0 {
		apiResources["custom_resources"] = customResources
	}

	if len(apiResources) == 0 {
		return nil
	}

	return apiResources
}

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
		"name":          "head",
		"instance_type": node.InstanceType.ValueString(),
	}

	if resourcesMap := resourceMapToAPI(node.Resources); len(resourcesMap) > 0 {
		config["resources"] = resourcesMap
	}

	// Add required_resources
	if !node.RequiredResources.IsNull() {
		var reqRes RequiredResourcesModel
		diags := node.RequiredResources.As(ctx, &reqRes, basetypes.ObjectAsOptions{})
		if !diags.HasError() {
			requiredResourcesMap := make(map[string]interface{})

			if !reqRes.CPU.IsNull() {
				requiredResourcesMap["cpu"] = reqRes.CPU.ValueInt64()
			}
			if !reqRes.Memory.IsNull() {
				requiredResourcesMap["memory"] = reqRes.Memory.ValueString()
			}
			if !reqRes.GPU.IsNull() {
				requiredResourcesMap["gpu"] = reqRes.GPU.ValueInt64()
			}
			if !reqRes.Accelerator.IsNull() {
				requiredResourcesMap["accelerator"] = reqRes.Accelerator.ValueString()
			}
			if !reqRes.TPU.IsNull() {
				requiredResourcesMap["tpu"] = reqRes.TPU.ValueInt64()
			}
			if !reqRes.TPUHosts.IsNull() {
				requiredResourcesMap["anyscale/tpu_hosts"] = reqRes.TPUHosts.ValueInt64()
			}
			if !reqRes.CPUArchitecture.IsNull() {
				requiredResourcesMap["cpu_architecture"] = reqRes.CPUArchitecture.ValueString()
			}

			if len(requiredResourcesMap) > 0 {
				config["required_resources"] = requiredResourcesMap
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

	// Add advanced_instance_config (JSON string) - map to API field name
	if !node.AdvancedInstanceConfig.IsNull() && node.AdvancedInstanceConfig.ValueString() != "" {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(node.AdvancedInstanceConfig.ValueString()), &advancedConfig); err == nil {
			config["advanced_configurations_json"] = advancedConfig
		}
	}

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

	flags := map[string]interface{}{}
	if !node.Flags.IsNull() && node.Flags.ValueString() != "" {
		if err := json.Unmarshal([]byte(node.Flags.ValueString()), &flags); err != nil {
			return nil, err
		}
	}

	if len(flags) > 0 {
		config["flags"] = flags
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

	if resourcesMap := resourceMapToAPI(worker.Resources); len(resourcesMap) > 0 {
		config["resources"] = resourcesMap
	}

	// Add required_resources
	if !worker.RequiredResources.IsNull() {
		var reqRes RequiredResourcesModel
		diags := worker.RequiredResources.As(ctx, &reqRes, basetypes.ObjectAsOptions{})
		if !diags.HasError() {
			requiredResourcesMap := make(map[string]interface{})

			if !reqRes.CPU.IsNull() {
				requiredResourcesMap["cpu"] = reqRes.CPU.ValueInt64()
			}
			if !reqRes.Memory.IsNull() {
				requiredResourcesMap["memory"] = reqRes.Memory.ValueString()
			}
			if !reqRes.GPU.IsNull() {
				requiredResourcesMap["gpu"] = reqRes.GPU.ValueInt64()
			}
			if !reqRes.Accelerator.IsNull() {
				requiredResourcesMap["accelerator"] = reqRes.Accelerator.ValueString()
			}
			if !reqRes.TPU.IsNull() {
				requiredResourcesMap["tpu"] = reqRes.TPU.ValueInt64()
			}
			if !reqRes.TPUHosts.IsNull() {
				requiredResourcesMap["anyscale/tpu_hosts"] = reqRes.TPUHosts.ValueInt64()
			}
			if !reqRes.CPUArchitecture.IsNull() {
				requiredResourcesMap["cpu_architecture"] = reqRes.CPUArchitecture.ValueString()
			}

			if len(requiredResourcesMap) > 0 {
				config["required_resources"] = requiredResourcesMap
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

	// Add advanced_instance_config (JSON string) - map to API field name
	if !worker.AdvancedInstanceConfig.IsNull() && worker.AdvancedInstanceConfig.ValueString() != "" {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(worker.AdvancedInstanceConfig.ValueString()), &advancedConfig); err == nil {
			config["advanced_configurations_json"] = advancedConfig
		}
	}

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

	flags := map[string]interface{}{}
	if !worker.Flags.IsNull() && worker.Flags.ValueString() != "" {
		if err := json.Unmarshal([]byte(worker.Flags.ValueString()), &flags); err != nil {
			return nil, err
		}
	}

	if len(flags) > 0 {
		config["flags"] = flags
	}

	return config, nil
}

// apiNodeTypeToTerraform converts an API head_node_type response to a Terraform types.Object
func apiNodeTypeToTerraform(ctx context.Context, apiNode map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	nodeAttrTypes := nodeConfigAttrTypes()

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

	requiredResources := types.ObjectNull(requiredResourcesAttrTypes())
	if rr, ok := apiNode["required_resources"].(map[string]interface{}); ok {
		reqResObj, reqResDiags := apiRequiredResourcesToTerraform(ctx, rr)
		diags.Append(reqResDiags...)
		requiredResources = reqResObj
	}

	labels := types.MapNull(types.StringType)
	if lbl, ok := apiNode["labels"].(map[string]interface{}); ok {
		labelsMap, labelsDiags := InterfaceMapToString(ctx, lbl)
		diags.Append(labelsDiags...)
		labels = labelsMap
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

	nodeAttrs := map[string]attr.Value{
		"instance_type":            instanceType,
		"resources":                resources,
		"required_resources":       requiredResources,
		"labels":                   labels,
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
	if rr, ok := apiWorker["required_resources"].(map[string]interface{}); ok {
		reqResObj, reqResDiags := apiRequiredResourcesToTerraform(ctx, rr)
		diags.Append(reqResDiags...)
		requiredResources = reqResObj
	}

	labels := types.MapNull(types.StringType)
	if lbl, ok := apiWorker["labels"].(map[string]interface{}); ok {
		labelsMap, labelsDiags := InterfaceMapToString(ctx, lbl)
		diags.Append(labelsDiags...)
		labels = labelsMap
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

	workerAttrs := map[string]attr.Value{
		"name":                     name,
		"min_nodes":                minNodes,
		"max_nodes":                maxNodes,
		"market_type":              marketType,
		"instance_type":            instanceType,
		"resources":                resources,
		"required_resources":       requiredResources,
		"labels":                   labels,
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
		"cpu":              types.Int64Type,
		"memory":           types.StringType,
		"gpu":              types.Int64Type,
		"accelerator":      types.StringType,
		"tpu":              types.Int64Type,
		"tpu_hosts":        types.Int64Type,
		"cpu_architecture": types.StringType,
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

// nodeConfigAttrTypes returns the attr.Type shape matching NodeConfigModel
// (head_node). Mirrors nodeConfigAttributes' schema one level down, at the
// attr.Type level needed for types.Object/types.ObjectValueFrom conversions.
func nodeConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"instance_type":            types.StringType,
		"resources":                types.MapType{ElemType: types.Float64Type},
		"required_resources":       requiredResourcesObjectType(),
		"labels":                   types.MapType{ElemType: types.StringType},
		"advanced_instance_config": types.StringType,
		"flags":                    types.StringType,
		"cloud_deployment":         cloudDeploymentObjectType(),
	}
}

// workerNodeConfigAttrTypes returns the attr.Type shape matching
// WorkerNodeConfigModel: nodeConfigAttrTypes plus the worker-specific fields.
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
	// Terraform format: {cpu: X, gpu: Y, memory: Z, object_store_memory: W, ...custom}
	flatMap := make(map[string]interface{})

	if cpu, ok := apiRes["cpu"].(float64); ok {
		flatMap["cpu"] = cpu
	}
	if gpu, ok := apiRes["gpu"].(float64); ok {
		flatMap["gpu"] = gpu
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

func apiRequiredResourcesToTerraform(ctx context.Context, apiPR map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	cpu := types.Int64Null()
	if c, ok := apiPR["cpu"].(float64); ok {
		cpu = types.Int64Value(int64(c))
	}

	memory := types.StringNull()
	if m, ok := apiPR["memory"].(float64); ok {
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
	if th, ok := apiPR["anyscale/tpu_hosts"].(float64); ok {
		tpuHosts = types.Int64Value(int64(th))
	} else if th, ok := apiPR["anyscale_tpu_hosts"].(float64); ok {
		tpuHosts = types.Int64Value(int64(th))
	}

	cpuArchitecture := types.StringNull()
	if ca, ok := apiPR["cpu_architecture"].(string); ok {
		cpuArchitecture = types.StringValue(ca)
	}

	attrs := map[string]attr.Value{
		"cpu":              cpu,
		"memory":           memory,
		"gpu":              gpu,
		"accelerator":      accelerator,
		"tpu":              tpu,
		"tpu_hosts":        tpuHosts,
		"cpu_architecture": cpuArchitecture,
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
