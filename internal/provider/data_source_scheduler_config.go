package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &SchedulerConfigDataSource{}
var _ datasource.DataSourceWithConfigure = &SchedulerConfigDataSource{}

func NewSchedulerConfigDataSource() datasource.DataSource {
	return &SchedulerConfigDataSource{}
}

type SchedulerConfigDataSource struct {
	client *Client
}

func (d *SchedulerConfigDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scheduler_config"
}

// schedulerConfigDataSourceSelectorAttributes mirrors
// schedulerMatchExpressionAttributes with every leaf Computed.
//
// The resource's helper is deliberately not reused: its leaves carry
// `Required: true`, which on a data source would turn a read-only field into a
// user input. Making that helper mode-aware would change the resource's schema
// to serve this file, so the tree is spelled out instead.
func schedulerConfigDataSourceSelectorAttributes(what string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"key": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Label key matched on " + what + ".",
		},
		"operator": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "How `key` is compared: `in` and `not_in` test membership in `values`; `exists` and `does_not_exist` test only for the key's presence and carry no `values`.",
		},
		"values": schema.ListAttribute{
			Computed:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Values compared against `key`. `null` for the `exists` and `does_not_exist` operators, which take none.",
		},
	}
}

func (d *SchedulerConfigDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the active [Anyscale Scheduler](https://docs.anyscale.com/scheduler) configuration for your organization: the resource flavors, resource queues, scheduling rules, and recycle policy that govern how workloads are admitted and where they run.\n\n" +
			"~> **Beta.** The Anyscale Scheduler is a beta product and its configuration fields may change.\n\n" +
			"This data source takes no arguments. An organization has at most one active scheduler config and the API token already scopes the request to an organization, so there is nothing to select on - and for the same reason there is no plural variant. Versions are not addressable either: the API serves only the active config, so this always reads the current one and reports its number in `version`.\n\n" +
			"~> **Errors when no config exists.** If the organization has never applied a scheduler config, this fails rather than returning an empty document - an empty read would be indistinguishable from a config with every section unset. Create one with the `anyscale_scheduler_config` resource, or in the Anyscale console.\n\n" +
			"~> **Errors when the scheduler is disabled.** If the Anyscale Scheduler is not enabled for your organization, the read fails with an actionable message rather than an empty result. Unlike the resource's refresh, which leaves prior state untouched, there is no prior state here to fall back to.\n\n" +
			"~> **Reading a config this same configuration applies.** Terraform may read a data source during plan, before the `anyscale_scheduler_config` resource in the same configuration has applied. If you read a config your own configuration manages, add `depends_on = [anyscale_scheduler_config.<name>]` so the read is deferred to apply; otherwise the first plan errors with \"no active scheduler config\" or returns the previous version.\n\n" +
			"Sections the config does not set - `resource_flavors`, `resource_queues`, `scheduling_rules`, `recycle_policy` - read back as `null`, never as an empty list.\n\n" +
			"-> **A note on naming.** You may also see this called the **Global Resource Scheduler (GRS)** - the Anyscale CLI help text and some API error messages still use that name. They are the same product.",
		Attributes: map[string]schema.Attribute{
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Version number of the active config. Config documents are immutable and monotonically versioned: every apply mints a new version rather than editing the previous one.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When this config version was applied, RFC 3339.",
			},
			"creator_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the user that applied this config version. May be `null` for versions applied by an automated process that the API does not attribute to a user.",
			},
			"resource_flavors": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Named hardware profiles that queues allocate quota against, in the order the config declares them. Order is significant: when a workload can run on more than one flavor, flavors are tried in this order. `null` if the config sets no flavors.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Name of the flavor, referenced by `resource_queues[].resource_groups[].flavors[].name`.",
						},
						"selector": schema.ListNestedAttribute{
							Computed:            true,
							MarkdownDescription: "Node label expressions selecting the machines this flavor covers. All expressions must match (AND).",
							NestedObject:        schema.NestedAttributeObject{Attributes: schedulerConfigDataSourceSelectorAttributes("the node")},
						},
						"advanced_instance_config": schema.StringAttribute{
							Computed:   true,
							CustomType: jsontypes.NormalizedType{},
							MarkdownDescription: "Cloud-provider-specific instance configuration applied to machines in this flavor, as a JSON string. Use `jsondecode()` to read individual fields. " +
								"Key order and whitespace are whatever the API returned and are not meaningful; compare with `jsondecode()` rather than by text.",
						},
					},
				},
			},
			"resource_queues": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Queues that workloads are admitted into, each carrying its own quota and preemption policy. `null` if the config sets no queues.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Name of the queue, referenced by `scheduling_rules[].resource_queue`.",
						},
						"cohort_name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Cohort this queue belongs to. Queues in the same cohort can lend and borrow unused quota from one another; `null` means the queue never shares quota.",
						},
						"preemption": schema.SingleNestedAttribute{
							Computed:            true,
							MarkdownDescription: "When workloads in this queue may be preempted to make room for others. `null` if the queue sets no preemption policy.",
							Attributes: map[string]schema.Attribute{
								"reclaim_within_cohort": schema.StringAttribute{
									Computed:            true,
									MarkdownDescription: "Whether this queue may preempt workloads in other queues of its cohort to reclaim quota it lent out: `never`, `lower_priority`, or `any`.",
								},
								"borrow_within_cohort": schema.StringAttribute{
									Computed:            true,
									MarkdownDescription: "Whether this queue may preempt workloads in other queues of its cohort in order to borrow quota beyond its own: `never` or `lower_priority`.",
								},
								"within_resource_queue": schema.StringAttribute{
									Computed:            true,
									MarkdownDescription: "Whether a workload in this queue may preempt another workload in the same queue: `never` or `lower_priority`.",
								},
							},
						},
						"resource_groups": schema.ListNestedAttribute{
							Computed:            true,
							MarkdownDescription: "Quota for this queue, grouped by the resource types each group governs.",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"covered_resources": schema.ListAttribute{
										Computed:            true,
										ElementType:         types.StringType,
										MarkdownDescription: "Resource types this group governs, lowercase (for example `cpu`, `gpu`, `memory_gb`, `tpu`).",
									},
									"flavors": schema.ListNestedAttribute{
										Computed:            true,
										MarkdownDescription: "Per-flavor quota within this group, in declaration order - flavors are tried in that order.",
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"name": schema.StringAttribute{
													Computed:            true,
													MarkdownDescription: "Name of a flavor declared in `resource_flavors`.",
												},
												"resources": schema.ListNestedAttribute{
													Computed:            true,
													MarkdownDescription: "Quota for each covered resource type on this flavor.",
													NestedObject: schema.NestedAttributeObject{
														Attributes: map[string]schema.Attribute{
															"name": schema.StringAttribute{
																Computed:            true,
																MarkdownDescription: "Resource type this quota applies to, one of the enclosing group's `covered_resources`.",
															},
															"nominal_quota": schema.Float64Attribute{
																Computed:            true,
																MarkdownDescription: "Quota guaranteed to this queue for this resource. `null` means unlimited. **`0` is not the same as `null`**: an explicit `0` blocks this resource entirely for this queue.",
															},
															"lending_limit": schema.Float64Attribute{
																Computed:            true,
																MarkdownDescription: "Most of `nominal_quota` this queue will lend to other queues in its cohort. `null` means no limit; `0` lends nothing.",
															},
															"borrowing_limit": schema.Float64Attribute{
																Computed:            true,
																MarkdownDescription: "Most this queue may borrow from its cohort beyond `nominal_quota`. `null` means no limit; `0` borrows nothing.",
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
			"scheduling_rules": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Rules mapping workloads to queues. **First match wins, top to bottom**, so this order is significant. `null` if the config sets no rules; note that once any rule exists, a workload matching none of them is rejected rather than run unscheduled.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"resource_queue": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Name of the queue matching workloads are admitted into.",
						},
						"selector": schema.ListNestedAttribute{
							Computed:            true,
							MarkdownDescription: "Workload label expressions this rule matches on. All expressions must match (AND). `null` for a catch-all rule.",
							NestedObject:        schema.NestedAttributeObject{Attributes: schedulerConfigDataSourceSelectorAttributes("the workload")},
						},
						"priority_policy": schema.SingleNestedAttribute{
							Computed:            true,
							MarkdownDescription: "Priority bounds applied to workloads matched by this rule. `null` if the rule sets none.",
							Attributes: map[string]schema.Attribute{
								"default": schema.Int64Attribute{
									Computed:            true,
									MarkdownDescription: "Priority assigned to a matching workload that requests none.",
								},
								"min": schema.Int64Attribute{
									Computed:            true,
									MarkdownDescription: "Lowest priority a matching workload may request.",
								},
								"max": schema.Int64Attribute{
									Computed:            true,
									MarkdownDescription: "Highest priority a matching workload may request.",
								},
								"on_violation": schema.StringAttribute{
									Computed:            true,
									MarkdownDescription: "What happens to a workload requesting a priority outside `min`/`max`: `reject` it, or `force_update` its priority to the nearest bound.",
								},
							},
						},
					},
				},
			},
			"recycle_policy": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "When the scheduler retires and replaces the machines it manages. Reflects what the config stores; the scheduler does not act on it yet, so a policy here does not describe scheduling behavior in effect. `null` if the config sets no recycle policy.",
				Attributes: map[string]schema.Attribute{
					"rotation_interval": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "How long a machine may serve before being rotated out, as a duration string (for example `24h`).",
					},
					"max_workloads": schema.Int64Attribute{
						Computed:            true,
						MarkdownDescription: "How many workloads a machine may run before being rotated out.",
					},
					"max_idle_duration": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "How long a machine may sit idle before being reclaimed, as a duration string (for example `10m`).",
					},
				},
			},
		},
	}
}

func (d *SchedulerConfigDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the active config through getActiveSchedulerConfig, which
// accepts only 200 and converts a 404 into ErrSchedulerConfigNotFound. Calling
// DoRequestAndParse here with 404 in the accepted-status list would hand back a
// zero-valued response instead of an error, and "no config exists" would read
// as a config with every section unset.
//
// Both error paths are hard errors. The resource's Read fails open on a
// disabled scheduler, but that is licensed by the presence of prior state to
// leave untouched, not by the status code - a data source has none, so falling
// open here would mean publishing an empty document to everything downstream.
func (d *SchedulerConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	current, err := getActiveSchedulerConfig(ctx, d.client)
	if err != nil {
		switch {
		case errors.Is(err, ErrSchedulerConfigNotFound):
			resp.Diagnostics.AddError(
				"No Active Scheduler Config",
				"This organization has no active Anyscale Scheduler configuration to read.\n\n"+
					"Apply one with the `anyscale_scheduler_config` resource, or create one in the Anyscale console, before reading it here.\n\n"+
					"If this configuration also applies `anyscale_scheduler_config`, add `depends_on` to this data source so the read happens after that apply rather than during the plan that precedes it.",
			)
		case errors.Is(err, ErrSchedulerNotEnabled):
			resp.Diagnostics.AddError("Anyscale Scheduler Not Enabled", err.Error())
		default:
			resp.Diagnostics.AddError("Unable to Read Scheduler Config", err.Error())
		}
		return
	}

	// Reuses the resource's model and flatten helper rather than a parallel
	// pair: the tfsdk field set is identical, and a second flatten would be free
	// to drift on the null-vs-empty mapping this data source depends on.
	model, err := flattenSchedulerConfig(current.Result.Config)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Scheduler Config", err.Error())
		return
	}
	model.Version = types.Int64Value(current.Result.Version)
	model.CreatedAt = types.StringValue(current.Result.CreatedAt)
	model.CreatorID = stringOrNull(current.Result.CreatorID)

	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}
