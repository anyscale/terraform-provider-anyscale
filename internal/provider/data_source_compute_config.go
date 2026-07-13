package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &ComputeConfigDataSource{}
	_ datasource.DataSourceWithConfigure = &ComputeConfigDataSource{}
)

// NewComputeConfigDataSource returns a new compute config data source.
func NewComputeConfigDataSource() datasource.DataSource {
	return &ComputeConfigDataSource{}
}

// ComputeConfigDataSource defines the data source implementation.
type ComputeConfigDataSource struct {
	client *Client
}

// ComputeConfigDataSourceModel describes the data source data model.
type ComputeConfigDataSourceModel struct {
	// Input - either ID or Name must be specified
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	// Cloud identification - can provide either ID or Name as input, also computed as output
	CloudID   types.String `tfsdk:"cloud_id"`
	CloudName types.String `tfsdk:"cloud_name"`

	// Computed outputs (excluding CloudID and CloudName which are above)
	ConfigID               types.String `tfsdk:"config_id"`    // Version-specific API ID
	NameVersion            types.String `tfsdk:"name_version"` // Formatted as "name:version" for use with Anyscale APIs
	Versions               types.List   `tfsdk:"versions"`     // List of available version numbers
	Region                 types.String `tfsdk:"region"`
	IdleTerminationMinutes types.Int64  `tfsdk:"idle_termination_minutes"`
	MaximumUptimeMinutes   types.Int64  `tfsdk:"maximum_uptime_minutes"`
	EnableCrossZoneScaling types.Bool   `tfsdk:"enable_cross_zone_scaling"`
	AutoSelectWorkerConfig types.Bool   `tfsdk:"auto_select_worker_config"`
	ProjectID              types.String `tfsdk:"project_id"`
	Version                types.Int64  `tfsdk:"version"`
	CreatedAt              types.String `tfsdk:"created_at"`
	LastModifiedAt         types.String `tfsdk:"last_modified_at"`

	// CC6: node topology parity with the resource. Same underlying shape
	// (NodeConfigModel / WorkerNodeConfigModel), all Computed-only here.
	Zones       types.List   `tfsdk:"zones"`
	HeadNode    types.Object `tfsdk:"head_node"`
	WorkerNodes types.List   `tfsdk:"worker_nodes"`
}

// Metadata returns the data source type name.
func (d *ComputeConfigDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_config"
}

// Schema defines the data source schema.
func (d *ComputeConfigDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Use this data source to retrieve information about an existing Anyscale Compute Configuration. You can look up a compute config by its ID or name. A few resource-only fields (`min_resources`, `max_resources`, `flags`, `advanced_instance_config`, `cloud_resource`) aren't yet exposed here — see the [Compute Config guide](../guides/compute-config.md).",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The unique identifier of the compute config. Either `id` or `name` must be specified.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the compute config. Either `id` or `name` must be specified. This field is computed when looking up by ID. If multiple compute configs have the same name, the most recently created one will be returned.",
			},

			// Computed fields
			"cloud_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The ID of the Anyscale cloud. Can be used to filter compute configs when looking up by name. Either `cloud_id` or `cloud_name` can be specified.",
			},
			"cloud_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The name of the Anyscale cloud. Can be used to filter compute configs when looking up by name. Either `cloud_id` or `cloud_name` can be specified. If provided, will be resolved to cloud_id.",
			},
			"config_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The version-specific API ID of the compute config. This is the API identifier for the specific version.",
			},
			"name_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The compute config name and version formatted as `name:version` for use with Anyscale APIs. Null for anonymous compute configs (created without a name).",
			},
			"versions": schema.ListAttribute{
				ElementType:         types.Int64Type,
				Computed:            true,
				MarkdownDescription: "List of all available version numbers for this compute config, sorted in ascending order.",
			},
			"region": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The region to launch clusters in. Null if the API doesn't report a region for this compute config.",
			},
			"idle_termination_minutes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Number of minutes after which idle clusters will be terminated. 0 means disabled.",
			},
			"maximum_uptime_minutes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Maximum uptime in minutes before cluster termination.",
			},
			"enable_cross_zone_scaling": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether instances can run across multiple availability zones.",
			},
			"auto_select_worker_config": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether worker node groups are automatically selected based on workload.",
			},
			"project_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The project ID this compute config is associated with.",
			},
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The version number of this compute config. Null if the API doesn't report a version number.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The timestamp when the compute config was created. Null if the API doesn't report a creation timestamp.",
			},
			"last_modified_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The timestamp when the compute config was last modified. Null if the API doesn't report a last-modified timestamp.",
			},
			"zones": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Availability zones considered for this cluster.",
			},
			"head_node": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Configuration for the head node of the cluster.",
				Attributes:          dataSourceNodeAttributes(),
			},
			"worker_nodes": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Configuration for the worker nodes of the cluster.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: dataSourceWorkerNodeAttributes(),
				},
			},
		},
	}
}

// dataSourceNodeAttributes mirrors the resource's nodeConfigAttributes shape
// (internal/provider/resource_compute_config.go), Computed-only: a data
// source has no Optional/Required distinction, and datasource/schema types
// are a distinct Go type from resource/schema even where structurally
// identical, so the two cannot share a single schema.Attribute map.
func dataSourceNodeAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"instance_type": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Cloud provider instance type (e.g., `m5.2xlarge` on AWS, `n2-standard-8` on GCP).",
		},
		"resources": schema.MapAttribute{
			ElementType:         types.Float64Type,
			Computed:            true,
			MarkdownDescription: "Logical resources available on this node.",
		},
		"required_resources": schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: "Explicit hardware requirements for custom instance types (free pod shapes).",
			Attributes: map[string]schema.Attribute{
				"cpu":              schema.Int64Attribute{Computed: true, MarkdownDescription: "Number of CPUs allocated."},
				"memory":           schema.StringAttribute{Computed: true, MarkdownDescription: "Amount of memory allocated."},
				"gpu":              schema.Int64Attribute{Computed: true, MarkdownDescription: "Number of GPUs allocated."},
				"accelerator":      schema.StringAttribute{Computed: true, MarkdownDescription: "Type of accelerator (e.g., `T4`, `L4`, `A100`, `H100`, `TPU-V6E`)."},
				"tpu":              schema.Int64Attribute{Computed: true, MarkdownDescription: "Number of TPUs allocated."},
				"tpu_hosts":        schema.Int64Attribute{Computed: true, MarkdownDescription: "Number of TPU hosts."},
				"cpu_architecture": schema.StringAttribute{Computed: true, MarkdownDescription: "CPU architecture, e.g. `x86_64` or `arm64`."},
			},
		},
		"labels": schema.MapAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "Labels associated with the node for scheduling purposes.",
		},
		"advanced_instance_config": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Advanced instance configuration passed through to the cloud provider, as a JSON string.",
		},
		"flags": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Node-level flags, as a JSON string.",
		},
		"cloud_deployment": schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: "Cloud deployment selectors for this node.",
			Attributes: map[string]schema.Attribute{
				"provider":     schema.StringAttribute{Computed: true, MarkdownDescription: "Cloud provider name, e.g., `aws` or `gcp`."},
				"region":       schema.StringAttribute{Computed: true, MarkdownDescription: "Cloud provider region, e.g., `us-west-2`."},
				"machine_pool": schema.StringAttribute{Computed: true, MarkdownDescription: "Machine pool name."},
				"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Cloud deployment ID from cloud setup."},
			},
		},
	}
}

// dataSourceWorkerNodeAttributes mirrors the resource's
// workerNodeConfigAttributes: dataSourceNodeAttributes plus the
// worker-specific fields.
func dataSourceWorkerNodeAttributes() map[string]schema.Attribute {
	attrs := dataSourceNodeAttributes()
	attrs["name"] = schema.StringAttribute{Computed: true, MarkdownDescription: "Unique name of this worker group."}
	attrs["min_nodes"] = schema.Int64Attribute{Computed: true, MarkdownDescription: "Minimum number of nodes of this type kept running."}
	attrs["max_nodes"] = schema.Int64Attribute{Computed: true, MarkdownDescription: "Maximum number of nodes of this type."}
	attrs["market_type"] = schema.StringAttribute{Computed: true, MarkdownDescription: "ON_DEMAND, SPOT, or PREFER_SPOT."}
	return attrs
}

// Configure adds the provider configured client to the data source.
func (d *ComputeConfigDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		AddConfigError(&resp.Diagnostics,
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *ComputeConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config ComputeConfigDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate that either ID or Name is provided
	if config.ID.IsNull() && config.Name.IsNull() {
		AddConfigError(&resp.Diagnostics,
			"Missing Required Attribute",
			"Either 'id' or 'name' must be specified to look up a compute config.",
		)
		return
	}

	var configID string
	var err error

	if !config.ID.IsNull() {
		// Look up by ID
		configID = config.ID.ValueString()
		tflog.Info(ctx, "Looking up compute config by ID", map[string]any{"id": configID})
	} else {
		// Look up by name
		name := config.Name.ValueString()
		cloudID := ""

		// Resolve cloud_id from either cloud_id or cloud_name
		if !config.CloudID.IsNull() {
			cloudID = config.CloudID.ValueString()
		} else if !config.CloudName.IsNull() {
			cloudName := config.CloudName.ValueString()
			tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": cloudName})

			cloudID, err = ResolveCloudNameToID(ctx, d.client, cloudName)
			if err != nil {
				AddConfigError(&resp.Diagnostics,
					"Cloud Name Resolution Failed",
					fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
				)
				return
			}
		}

		tflog.Info(ctx, "Looking up compute config by name", map[string]any{"name": name, "cloud_id": cloudID})

		configID, err = d.findComputeConfigByName(ctx, name, cloudID)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "find compute config", err)
			return
		}

		if configID == "" {
			AddConfigError(&resp.Diagnostics,
				"Compute Config Not Found",
				fmt.Sprintf("No compute config found with name '%s'", name),
			)
			return
		}
	}

	// CC5a: fetch and parse using the same typed structs the resource uses
	// (computeTemplateResponse/computeTemplate/computeTemplateConfig, see
	// resource_compute_config.go) instead of hand-parsing a raw
	// map[string]interface{}. Still calls /ext/v0/cluster_computes -- the
	// endpoint migration to /api/v2/compute_templates itself is CC5b, gated
	// and deferred separately; ext/v0 and api/v2 are reshapings of the same
	// underlying record, so the same JSON field names decode correctly here.
	computeResp, err := DoRequestAndParse[computeTemplateResponse](
		ctx,
		d.client,
		"GET",
		fmt.Sprintf("/ext/v0/cluster_computes/%s", configID),
		nil,
		http.StatusOK,
	)
	if err != nil {
		tflog.Error(ctx, "Failed to fetch compute config", map[string]any{"error": err.Error()})
		AddAPIError(&resp.Diagnostics, "fetch compute config", err)
		return
	}

	resultData := computeResp.Result

	config.ID = types.StringValue(resultData.ID)
	config.ConfigID = types.StringValue(resultData.ID) // Also set config_id for consistency with resource

	configName := resultData.Name
	if configName != "" {
		config.Name = types.StringValue(configName)
	}

	// DS-CC-3: explicit null on the else branch - version numbers are 1-indexed,
	// so 0 means absent, matching computeTemplate.Version's own convention.
	if resultData.Version > 0 {
		config.Version = types.Int64Value(resultData.Version)
		config.NameVersion = types.StringValue(fmt.Sprintf("%s:%d", configName, resultData.Version))
	} else {
		config.Version = types.Int64Null()
		config.NameVersion = types.StringNull()
	}

	// Fetch all versions of this compute config by name
	if configName != "" {
		versions, err := d.fetchComputeConfigVersions(ctx, configName)
		if err != nil {
			tflog.Warn(ctx, "Failed to fetch versions list", map[string]any{"error": err.Error()})
			config.Versions = types.ListNull(types.Int64Type)
		} else {
			versionsList, diags := types.ListValueFrom(ctx, types.Int64Type, versions)
			resp.Diagnostics.Append(diags...)
			config.Versions = versionsList
		}
	} else {
		config.Versions = types.ListNull(types.Int64Type)
	}

	// DS-CC-3: explicit stringOrNull instead of leaving the else case to an
	// implicit zero-value types.String (which happens to equal null today,
	// but is fragile - not obviously so to a future reader/refactor).
	config.CreatedAt = stringOrNull(resultData.CreatedAt)
	config.LastModifiedAt = stringOrNull(resultData.LastModifiedAt)

	if resultData.ProjectID != "" {
		config.ProjectID = types.StringValue(resultData.ProjectID)
	} else {
		config.ProjectID = types.StringNull()
	}

	configData := resultData.Config
	if configData.CloudID != "" {
		config.CloudID = types.StringValue(configData.CloudID)

		cloudResp, err := DoRequestAndParse[CloudResponse](
			ctx,
			d.client,
			"GET",
			fmt.Sprintf("/api/v2/clouds/%s", configData.CloudID),
			nil,
			http.StatusOK,
		)
		if err == nil {
			config.CloudName = types.StringValue(cloudResp.Result.Name)
		} else {
			// If we can't fetch cloud name, just leave it null - it's not critical
			tflog.Debug(ctx, "Could not fetch cloud name", map[string]any{"cloud_id": configData.CloudID})
		}
	}

	// idle_termination_minutes/maximum_uptime_minutes are top-level config
	// fields only, same as the resource -- never per-deployment, so read
	// straight off configData rather than through resolveEffectiveComputeConfig.
	if configData.IdleTerminationMinutes != nil {
		config.IdleTerminationMinutes = types.Int64Value(*configData.IdleTerminationMinutes)
	} else {
		config.IdleTerminationMinutes = types.Int64Null()
	}
	if configData.MaximumUptimeMinutes != nil {
		config.MaximumUptimeMinutes = types.Int64Value(*configData.MaximumUptimeMinutes)
	} else {
		config.MaximumUptimeMinutes = types.Int64Null()
	}
	// DS-CC-3: explicit stringOrNull, same reasoning as created_at/last_modified_at above.
	config.Region = stringOrNull(configData.Region)

	eff := resolveEffectiveComputeConfig(configData)

	config.EnableCrossZoneScaling = types.BoolValue(false)
	if eff.Flags != nil {
		if enableCrossZone, ok := eff.Flags["allow-cross-zone-autoscaling"].(bool); ok {
			config.EnableCrossZoneScaling = types.BoolValue(enableCrossZone)
		}
	}
	config.AutoSelectWorkerConfig = types.BoolValue(eff.AutoSelect)

	// CC6: node topology parity with the resource. A data source has no
	// prior state to mask Computed sub-attributes against (there is nothing
	// analogous to "the user left this null on purpose" for a read-only
	// lookup), so these report exactly what the API returns, unmasked --
	// unlike the resource, which nulls resources/required_resources/etc. that
	// were never explicitly configured to avoid perpetual plan drift. A data
	// source has no plan to drift.
	if len(eff.AllowedAZs) > 0 {
		allowedAZInterfaces := make([]interface{}, 0, len(eff.AllowedAZs))
		for _, az := range eff.AllowedAZs {
			allowedAZInterfaces = append(allowedAZInterfaces, az)
		}
		zonesList, diags := InterfaceListToString(ctx, allowedAZInterfaces)
		resp.Diagnostics.Append(diags...)
		config.Zones = zonesList
	} else {
		config.Zones = types.ListNull(types.StringType)
	}

	config.HeadNode = types.ObjectNull(nodeConfigAttrTypes())
	if eff.HeadNodeType != nil {
		headNodeObj, headNodeDiags := apiNodeTypeToTerraform(ctx, eff.HeadNodeType)
		resp.Diagnostics.Append(headNodeDiags...)
		if !resp.Diagnostics.HasError() {
			config.HeadNode = headNodeObj
		}
	}

	config.WorkerNodes = types.ListNull(types.ObjectType{AttrTypes: workerNodeConfigAttrTypes()})
	if len(eff.WorkerNodeTypes) > 0 {
		workerInterfaces := make([]interface{}, 0, len(eff.WorkerNodeTypes))
		for _, worker := range eff.WorkerNodeTypes {
			workerInterfaces = append(workerInterfaces, worker)
		}
		workerNodesList, workerNodesDiags := apiWorkerNodeTypesToTerraform(ctx, workerInterfaces)
		resp.Diagnostics.Append(workerNodesDiags...)
		if !resp.Diagnostics.HasError() {
			config.WorkerNodes = workerNodesList
		}
	}

	tflog.Info(ctx, "Successfully retrieved compute config", map[string]any{
		"id":   configID,
		"name": config.Name.ValueString(),
	})

	// Set state
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// searchClusterComputesPaged pages through POST /ext/v0/cluster_computes/search until every
// page is exhausted, decoding each page's body with decode. This endpoint's pagination is
// body-based (ClusterComputesQuery.paging, a PageQuery{count, paging_token}) and its response
// metadata carries next_paging_token the same way every GET list endpoint's does - just inside
// the response body instead of being driven by URL query params. Traced against product
// backend/server/api/base/routers/cluster_computes_router.go (search_cluster_computes) and
// backend/server/common/models/queries.go (PageQuery, count defaults to 10 if never set - the
// root cause of DS-CC-1/DS-CC-2's truncation). The shared PaginatedRequest helper only handles
// GET+query-string pagination, so this stays a local loop rather than a shared helper - a single
// POST-body-paginated endpoint doesn't justify generalizing that helper's shape.
func searchClusterComputesPaged[T any](
	ctx context.Context,
	client *Client,
	basePayload map[string]interface{},
	decode func(body []byte) (items []T, nextToken *string, err error),
) ([]T, error) {
	var allItems []T
	var pagingToken string

	for {
		payload := make(map[string]interface{}, len(basePayload)+1)
		for k, v := range basePayload {
			payload[k] = v
		}
		paging := map[string]interface{}{"count": 100}
		if pagingToken != "" {
			paging["paging_token"] = pagingToken
		}
		payload["paging"] = paging

		body, err := MarshalRequestBody(payload)
		if err != nil {
			return nil, err
		}

		respBody, err := DoRequestRaw(ctx, client, "POST", "/ext/v0/cluster_computes/search", body, http.StatusOK)
		if err != nil {
			return nil, fmt.Errorf("search request failed: %w", err)
		}

		items, nextToken, err := decode(respBody)
		if err != nil {
			return nil, fmt.Errorf("failed to parse search response: %w", err)
		}
		allItems = append(allItems, items...)

		if nextToken == nil || *nextToken == "" {
			break
		}
		pagingToken = *nextToken
	}

	return allItems, nil
}

// computeConfigSearchResult is the shape shared by both search call sites below.
type computeConfigSearchResult struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	Anonymous bool    `json:"anonymous"`
	Version   float64 `json:"version"`
}

// decodeComputeConfigSearchPage decodes one page of a cluster_computes/search response.
func decodeComputeConfigSearchPage(body []byte) ([]computeConfigSearchResult, *string, error) {
	var resp struct {
		Results  []computeConfigSearchResult `json:"results"`
		Metadata struct {
			NextPagingToken *string `json:"next_paging_token"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, err
	}
	return resp.Results, resp.Metadata.NextPagingToken, nil
}

// findComputeConfigByName looks for a compute config with the given name using the search API,
// paging through every result (DS-CC-2: the search's default page size is 10, so a name with
// more than 10 versions/anonymous variants could previously miss the real newest match).
func (d *ComputeConfigDataSource) findComputeConfigByName(ctx context.Context, name string, cloudID string) (string, error) {
	searchPayload := map[string]interface{}{
		"name": map[string]string{
			"equals": name,
		},
		"include_anonymous": false,
	}

	// Add cloud_id filter if provided
	if cloudID != "" {
		searchPayload["cloud_id"] = cloudID
	}

	results, err := searchClusterComputesPaged(ctx, d.client, searchPayload, decodeComputeConfigSearchPage)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "", nil // Not found
	}

	// The search API already exact-matches name server-side, so every result here is a
	// genuine match - pick the most recently created one on duplicates.
	var matchedConfigID string
	var latestCreatedAt string

	for _, cfg := range results {
		if matchedConfigID == "" || cfg.CreatedAt > latestCreatedAt {
			matchedConfigID = cfg.ID
			latestCreatedAt = cfg.CreatedAt
		}
	}

	WarnIfMultipleMatches(ctx, "compute config", name, len(results), matchedConfigID)

	tflog.Info(ctx, "Found compute config by name", map[string]any{
		"name":      name,
		"config_id": matchedConfigID,
	})

	return matchedConfigID, nil
}

// fetchComputeConfigVersions retrieves all version numbers for a compute config by name.
//
// DS-CC-1: the search payload previously sent no version field, which resolves to the
// documented deprecated-equivalent-to-latest-only behavior (verified against
// backend/server/api/base/models/cluster_computes.py's ClusterComputesQuery.version field and
// its validator) - so "all versions" was structurally unable to return more than one. version:
// -2 is the documented "do not filter by version" sentinel; combined with DS-CC-2's pagination
// fix, this now genuinely enumerates every version rather than just the latest.
func (d *ComputeConfigDataSource) fetchComputeConfigVersions(ctx context.Context, name string) ([]int64, error) {
	searchPayload := map[string]interface{}{
		"name": map[string]string{
			"equals": name,
		},
		"include_anonymous": false,
		"version":           -2,
	}

	results, err := searchClusterComputesPaged(ctx, d.client, searchPayload, decodeComputeConfigSearchPage)
	if err != nil {
		return nil, err
	}

	// Collect unique version numbers and sort them
	versionSet := make(map[int64]bool)
	for _, cfg := range results {
		versionSet[int64(cfg.Version)] = true
	}

	versions := make([]int64, 0, len(versionSet))
	for v := range versionSet {
		versions = append(versions, v)
	}

	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	tflog.Debug(ctx, "Found compute config versions", map[string]any{
		"name":     name,
		"versions": versions,
	})

	return versions, nil
}
