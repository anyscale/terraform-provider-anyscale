package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &GlobalResourceSchedulerDataSource{}
	_ datasource.DataSourceWithConfigure = &GlobalResourceSchedulerDataSource{}
)

// NewGlobalResourceSchedulerDataSource creates a new global resource scheduler data source.
func NewGlobalResourceSchedulerDataSource() datasource.DataSource {
	return &GlobalResourceSchedulerDataSource{}
}

// GlobalResourceSchedulerDataSource defines the data source implementation.
type GlobalResourceSchedulerDataSource struct {
	client *Client
}

// GlobalResourceSchedulerDataSourceModel describes the data source data model.
type GlobalResourceSchedulerDataSourceModel struct {
	// Input attribute (required)
	Name types.String `tfsdk:"name"`

	// Computed outputs
	ID                            types.String `tfsdk:"id"`
	OrganizationID                types.String `tfsdk:"organization_id"`
	EnableRootlessDataplaneConfig types.Bool   `tfsdk:"enable_rootless_dataplane_config"`
	CloudIDs                      types.List   `tfsdk:"cloud_ids"`

	// Spec as a computed nested structure
	Spec []GlobalResourceSchedulerSpecDataSourceModel `tfsdk:"spec"`
}

// GlobalResourceSchedulerSpecDataSourceModel represents the global resource scheduler specification in data source.
type GlobalResourceSchedulerSpecDataSourceModel struct {
	Kind         types.String                          `tfsdk:"kind"`
	MachineTypes []SchedulerMachineTypeDataSourceModel `tfsdk:"machine_types"`
}

// SchedulerMachineTypeDataSourceModel represents a machine type in data source.
type SchedulerMachineTypeDataSourceModel struct {
	Name            types.String                             `tfsdk:"name"`
	LaunchTemplates []SchedulerLaunchTemplateDataSourceModel `tfsdk:"launch_templates"`
	RecyclePolicy   []SchedulerRecyclePolicyDataSourceModel  `tfsdk:"recycle_policy"`
	Partitions      []SchedulerPartitionDataSourceModel      `tfsdk:"partitions"`
}

// SchedulerLaunchTemplateDataSourceModel represents a launch template in data source.
type SchedulerLaunchTemplateDataSourceModel struct {
	InstanceType types.String `tfsdk:"instance_type"`
	MarketType   types.String `tfsdk:"market_type"`
	Zones        types.List   `tfsdk:"zones"`
}

// SchedulerRecyclePolicyDataSourceModel represents a recycle policy in data source.
type SchedulerRecyclePolicyDataSourceModel struct {
	MaxWorkloads     types.Int64  `tfsdk:"max_workloads"`
	RotationInterval types.String `tfsdk:"rotation_interval"`
	MaxIdleDuration  types.String `tfsdk:"max_idle_duration"`
}

// SchedulerPartitionDataSourceModel represents a partition in data source.
type SchedulerPartitionDataSourceModel struct {
	Name  types.String                   `tfsdk:"name"`
	Size  types.Int64                    `tfsdk:"size"`
	Rules []SchedulerRuleDataSourceModel `tfsdk:"rules"`
}

// SchedulerRuleDataSourceModel represents a rule in data source.
type SchedulerRuleDataSourceModel struct {
	Selector types.String `tfsdk:"selector"`
	Priority types.Int64  `tfsdk:"priority"`
	Quota    types.Int64  `tfsdk:"quota"`
}

// Metadata returns the data source type name.
func (d *GlobalResourceSchedulerDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_global_resource_scheduler"
}

// Schema defines the schema for the data source.
func (d *GlobalResourceSchedulerDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches details about an Anyscale Global Resource Scheduler by name.",

		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the global resource scheduler.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the global resource scheduler.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The organization ID that owns the global resource scheduler.",
			},
			"enable_rootless_dataplane_config": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether rootless dataplane configuration is enabled.",
			},
			"cloud_ids": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "List of cloud IDs attached to this global resource scheduler.",
			},
			"spec": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The global resource scheduler specification.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kind": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The type of global resource scheduler. Always `ANYSCALE_MANAGED`.",
						},
						"machine_types": schema.ListNestedAttribute{
							Computed:            true,
							MarkdownDescription: "Machine type configurations.",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{
										Computed:            true,
										MarkdownDescription: "Resource identifier (e.g., `RES-8CPU-32GB`).",
									},
									"launch_templates": schema.ListNestedAttribute{
										Computed:            true,
										MarkdownDescription: "Cloud-specific instance configurations.",
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"instance_type": schema.StringAttribute{
													Computed:            true,
													MarkdownDescription: "Cloud provider instance type.",
												},
												"market_type": schema.StringAttribute{
													Computed:            true,
													MarkdownDescription: "Provisioning model: `ON_DEMAND` or `SPOT`.",
												},
												"zones": schema.ListAttribute{
													Computed:            true,
													ElementType:         types.StringType,
													MarkdownDescription: "Availability zones.",
												},
											},
										},
									},
									"recycle_policy": schema.ListNestedAttribute{
										Computed:            true,
										MarkdownDescription: "Instance rotation policy.",
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"max_workloads": schema.Int64Attribute{
													Computed:            true,
													MarkdownDescription: "Maximum workloads before rotation.",
												},
												"rotation_interval": schema.StringAttribute{
													Computed:            true,
													MarkdownDescription: "Time interval for rotation.",
												},
												"max_idle_duration": schema.StringAttribute{
													Computed:            true,
													MarkdownDescription: "Maximum idle time before termination.",
												},
											},
										},
									},
									"partitions": schema.ListNestedAttribute{
										Computed:            true,
										MarkdownDescription: "Resource allocation groups.",
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"name": schema.StringAttribute{
													Computed:            true,
													MarkdownDescription: "Partition name.",
												},
												"size": schema.Int64Attribute{
													Computed:            true,
													MarkdownDescription: "Total machines in partition.",
												},
												"rules": schema.ListNestedAttribute{
													Computed:            true,
													MarkdownDescription: "Scheduling rules.",
													NestedObject: schema.NestedAttributeObject{
														Attributes: map[string]schema.Attribute{
															"selector": schema.StringAttribute{
																Computed:            true,
																MarkdownDescription: "Kubernetes-style label selector.",
															},
															"priority": schema.Int64Attribute{
																Computed:            true,
																MarkdownDescription: "Scheduling priority.",
															},
															"quota": schema.Int64Attribute{
																Computed:            true,
																MarkdownDescription: "Maximum machines for matching workloads.",
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

// Configure adds the provider configured client to the data source.
func (d *GlobalResourceSchedulerDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *GlobalResourceSchedulerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config GlobalResourceSchedulerDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	schedulerName := config.Name.ValueString()

	tflog.Debug(ctx, "Looking up global resource scheduler", map[string]any{
		"name": schedulerName,
	})

	// List all global resource schedulers and find by name
	listResp, err := DoRequestAndParse[ListMachinePoolsResponse](
		ctx,
		d.client,
		"GET",
		"/api/v2/machine_pools/",
		nil,
		http.StatusOK,
	)
	if err != nil {
		AddAPIError(&resp.Diagnostics, "list global resource schedulers", err)
		return
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
		AddConfigError(&resp.Diagnostics, "Global Resource Scheduler Not Found",
			fmt.Sprintf("No global resource scheduler found with name '%s'", schedulerName))
		return
	}

	// Populate config
	config.ID = types.StringValue(foundScheduler.MachinePoolID)
	config.Name = types.StringValue(foundScheduler.MachinePoolName)
	config.OrganizationID = types.StringValue(foundScheduler.OrganizationID)
	config.EnableRootlessDataplaneConfig = types.BoolValue(foundScheduler.EnableRootlessDataplaneConfig)

	// Convert cloud IDs to list
	cloudIDs := make([]string, len(foundScheduler.CloudIDs))
	copy(cloudIDs, foundScheduler.CloudIDs)
	cloudIDsList, diags := types.ListValueFrom(ctx, types.StringType, cloudIDs)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	config.CloudIDs = cloudIDsList

	// Convert spec
	if len(foundScheduler.Spec) > 0 {
		specModel, err := d.specFromAPI(ctx, foundScheduler.Spec)
		if err != nil {
			tflog.Warn(ctx, "Failed to convert spec from API", map[string]any{
				"error": err.Error(),
			})
		} else {
			config.Spec = specModel
		}
	}

	tflog.Info(ctx, "Global resource scheduler found", map[string]any{
		"id":   foundScheduler.MachinePoolID,
		"name": foundScheduler.MachinePoolName,
	})

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// specFromAPI converts the API spec to data source model format.
func (d *GlobalResourceSchedulerDataSource) specFromAPI(ctx context.Context, apiSpec map[string]any) ([]GlobalResourceSchedulerSpecDataSourceModel, error) {
	if len(apiSpec) == 0 {
		return nil, nil
	}

	specModel := GlobalResourceSchedulerSpecDataSourceModel{}

	// Kind
	if kind, ok := apiSpec["kind"].(string); ok {
		specModel.Kind = types.StringValue(kind)
	} else {
		specModel.Kind = types.StringValue("ANYSCALE_MANAGED")
	}

	// Machine types
	if machineTypesRaw, ok := apiSpec["machine_types"].([]any); ok {
		machineTypes := make([]SchedulerMachineTypeDataSourceModel, 0, len(machineTypesRaw))

		for _, mtRaw := range machineTypesRaw {
			mt, ok := mtRaw.(map[string]any)
			if !ok {
				continue
			}

			machineType := SchedulerMachineTypeDataSourceModel{}

			if name, ok := mt["machine_type"].(string); ok {
				machineType.Name = types.StringValue(name)
			}

			// Launch templates
			if templatesRaw, ok := mt["launch_templates"].([]any); ok {
				templates := make([]SchedulerLaunchTemplateDataSourceModel, 0, len(templatesRaw))
				for _, ltRaw := range templatesRaw {
					lt, ok := ltRaw.(map[string]any)
					if !ok {
						continue
					}

					template := SchedulerLaunchTemplateDataSourceModel{}
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

					templates = append(templates, template)
				}
				machineType.LaunchTemplates = templates
			}

			// Recycle policy
			if rpRaw, ok := mt["recycle_policy"].(map[string]any); ok {
				rp := SchedulerRecyclePolicyDataSourceModel{}
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
				machineType.RecyclePolicy = []SchedulerRecyclePolicyDataSourceModel{rp}
			}

			// Partitions
			if partitionsRaw, ok := mt["partitions"].([]any); ok {
				partitions := make([]SchedulerPartitionDataSourceModel, 0, len(partitionsRaw))
				for _, pRaw := range partitionsRaw {
					p, ok := pRaw.(map[string]any)
					if !ok {
						continue
					}

					partition := SchedulerPartitionDataSourceModel{}
					if name, ok := p["name"].(string); ok {
						partition.Name = types.StringValue(name)
					}
					if size, ok := p["size"].(float64); ok {
						partition.Size = types.Int64Value(int64(size))
					}

					// Rules
					if rulesRaw, ok := p["rules"].([]any); ok {
						rules := make([]SchedulerRuleDataSourceModel, 0, len(rulesRaw))
						for _, rRaw := range rulesRaw {
							rule, ok := rRaw.(map[string]any)
							if !ok {
								continue
							}

							ruleModel := SchedulerRuleDataSourceModel{}
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

	return []GlobalResourceSchedulerSpecDataSourceModel{specModel}, nil
}
