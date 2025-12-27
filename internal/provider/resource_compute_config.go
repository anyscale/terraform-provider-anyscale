package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceComputeConfig returns the schema for the anyscale_compute_config resource
func ResourceComputeConfig() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceComputeConfigCreate,
		ReadContext:   resourceComputeConfigRead,
		UpdateContext: resourceComputeConfigUpdate,
		DeleteContext: resourceComputeConfigDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The unique identifier of the compute config.",
			},
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The name of the compute config. If not provided, an anonymous config will be created.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The project ID to associate the compute config with.",
			},
			"cloud_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The ID of the Anyscale cloud to use for launching clusters.",
			},
			"region": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "USE_CLOUD",
				Description: "The region to launch clusters in. Defaults to USE_CLOUD which uses the cloud's default region.",
			},
			"idle_termination_minutes": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      120,
				Description:  "If set to a positive number, Anyscale will terminate the cluster this many minutes after the cluster is idle. Set to 0 to disable. Defaults to 120 minutes.",
				ValidateFunc: validation.IntAtLeast(0),
			},
			"maximum_uptime_minutes": {
				Type:         schema.TypeInt,
				Optional:     true,
				Description:  "If set to a positive number, Anyscale will terminate the cluster this many minutes after cluster start.",
				ValidateFunc: validation.IntAtLeast(1),
			},
			"allowed_azs": {
				Type:        schema.TypeList,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "The availability zones that sessions are allowed to be launched in. If not specified, any AZ may be used.",
			},
			"min_resources": {
				Type:        schema.TypeMap,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeFloat},
				Description: "Total minimum logical resources across all nodes in the cluster (e.g., {\"CPU\": 4, \"GPU\": 1})",
			},
			"max_resources": {
				Type:        schema.TypeMap,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeFloat},
				Description: "Total maximum logical resources across all nodes in the cluster (e.g., {\"CPU\": 100, \"GPU\": 8})",
			},
			"enable_cross_zone_scaling": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Allow instances in the cluster to be run across multiple zones. Recommended for production services.",
			},
			"advanced_configurations_json": {
				Type:             schema.TypeString,
				Optional:         true,
				Description:      "Advanced configurations for this compute config to pass to the cloud provider when launching instances. Should be valid JSON.",
				ValidateFunc:     validation.StringIsJSON,
				DiffSuppressFunc: suppressEquivalentJSON,
			},
			"auto_select_worker_config": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "If set to true, worker node groups will automatically be selected based on workload.",
			},
			"flags": {
				Type:             schema.TypeString,
				Optional:         true,
				Description:      "A set of advanced cluster-level flags that can be used to configure a particular workload. Should be valid JSON.",
				ValidateFunc:     validation.StringIsJSON,
				DiffSuppressFunc: suppressEquivalentJSON,
			},
			"anonymous": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "An anonymous compute config does not show up in the list of cluster configs.",
			},
			"version": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "The version number of this compute config.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The timestamp when the compute config was created.",
			},
			"last_modified_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The timestamp when the compute config was last modified.",
			},
			"head_node": {
				Type:        schema.TypeList,
				Required:    true,
				MaxItems:    1,
				Description: "Configuration for the head node of the cluster.",
				Elem: &schema.Resource{
					Schema: nodeConfigSchema(),
				},
			},
			"worker_nodes": {
				Type:        schema.TypeList,
				Optional:    true,
				Description: "Configuration for the worker nodes of the cluster. If not provided, worker nodes will be automatically selected based on logical resource requests.",
				Elem: &schema.Resource{
					Schema: workerNodeConfigSchema(),
				},
			},
		},
	}
}

// nodeConfigSchema returns the schema for a node configuration
func nodeConfigSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"instance_type": {
			Type:        schema.TypeString,
			Required:    true,
			Description: "Cloud provider instance type (e.g., m5.2xlarge on AWS, n2-standard-8 on GCP). Use 'custom' when required_resources is provided.",
		},
		"resources": {
			Type:        schema.TypeMap,
			Optional:    true,
			Elem:        &schema.Schema{Type: schema.TypeFloat},
			Description: "Logical resources that will be available on this node. Defaults to match the physical resources of the instance type.",
		},
		"required_resources": {
			Type:        schema.TypeList,
			Optional:    true,
			MaxItems:    1,
			Description: "Physical resources for custom instance types (free pod shapes). Explicitly defines CPU, memory, and GPU resources.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"cpu": {
						Type:         schema.TypeInt,
						Optional:     true,
						Description:  "Number of CPUs to allocate.",
						ValidateFunc: validation.IntAtLeast(0),
					},
					"memory": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Amount of memory to allocate. Can be specified as bytes (int) or as a string with units (e.g., '4Gi', '1024Mi').",
					},
					"gpu": {
						Type:         schema.TypeInt,
						Optional:     true,
						Description:  "Number of GPUs to allocate.",
						ValidateFunc: validation.IntAtLeast(0),
					},
					"accelerator": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Type of accelerator (e.g., 'T4', 'L4', 'A100', 'H100', 'TPU-V6E').",
					},
					"tpu": {
						Type:         schema.TypeInt,
						Optional:     true,
						Description:  "Number of TPUs to allocate.",
						ValidateFunc: validation.IntAtLeast(0),
					},
					"tpu_hosts": {
						Type:         schema.TypeInt,
						Optional:     true,
						Description:  "Number of TPU hosts (for anyscale/tpu_hosts custom resource).",
						ValidateFunc: validation.IntAtLeast(0),
					},
				},
			},
		},
		"labels": {
			Type:        schema.TypeMap,
			Optional:    true,
			Elem:        &schema.Schema{Type: schema.TypeString},
			Description: "Labels to associate the node with for scheduling purposes.",
		},
		"required_labels": {
			Type:        schema.TypeMap,
			Optional:    true,
			Elem:        &schema.Schema{Type: schema.TypeString},
			Description: "Required labels that must be present on the node for scheduling purposes.",
		},
		"advanced_instance_config": {
			Type:             schema.TypeString,
			Optional:         true,
			Description:      "Advanced instance configurations that will be passed through to the cloud provider. Should be valid JSON.",
			ValidateFunc:     validation.StringIsJSON,
			DiffSuppressFunc: suppressEquivalentJSON,
		},
		"flags": {
			Type:             schema.TypeString,
			Optional:         true,
			Description:      "Node-level flags specifying advanced or experimental options. Should be valid JSON.",
			ValidateFunc:     validation.StringIsJSON,
			DiffSuppressFunc: suppressEquivalentJSON,
		},
		"cloud_deployment": {
			Type:        schema.TypeList,
			Optional:    true,
			MaxItems:    1,
			Description: "Cloud deployment selectors for this node; one or more selectors may be passed to target a specific deployment.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"provider": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Cloud provider name, e.g., aws or gcp.",
					},
					"region": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Cloud provider region, e.g., us-west-2.",
					},
					"machine_pool": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Machine pool name.",
					},
					"id": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Cloud deployment ID from cloud setup.",
					},
				},
			},
		},
	}
}

// workerNodeConfigSchema returns the schema for a worker node configuration
func workerNodeConfigSchema() map[string]*schema.Schema {
	workerSchema := nodeConfigSchema()

	// Add worker-specific fields
	workerSchema["name"] = &schema.Schema{
		Type:        schema.TypeString,
		Optional:    true,
		Description: "Unique name of this worker group. Defaults to a human-friendly representation of the instance type.",
	}
	workerSchema["min_nodes"] = &schema.Schema{
		Type:         schema.TypeInt,
		Optional:     true,
		Default:      0,
		Description:  "Minimum number of nodes of this type that will be kept running in the cluster.",
		ValidateFunc: validation.IntAtLeast(0),
	}
	workerSchema["max_nodes"] = &schema.Schema{
		Type:         schema.TypeInt,
		Optional:     true,
		Default:      10,
		Description:  "Maximum number of nodes of this type that can be running in the cluster.",
		ValidateFunc: validation.IntAtLeast(1),
	}
	workerSchema["market_type"] = &schema.Schema{
		Type:     schema.TypeString,
		Optional: true,
		Default:  "ON_DEMAND",
		Description: "The type of instances to use: ON_DEMAND (standard pricing), SPOT (discounted, interruptible), " +
			"or PREFER_SPOT (prefer spot with on-demand fallback).",
		ValidateFunc: validation.StringInSlice([]string{"ON_DEMAND", "SPOT", "PREFER_SPOT"}, false),
	}

	return workerSchema
}

func resourceComputeConfigCreate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	client := meta.(*Client)

	// Build the API request
	createRequest := map[string]interface{}{
		"anonymous": d.Get("anonymous").(bool),
		"config": map[string]interface{}{
			"cloud_id": d.Get("cloud_id").(string),
		},
	}

	// Add optional name
	if name, ok := d.GetOk("name"); ok {
		createRequest["name"] = name.(string)
	}

	// Add optional project_id
	if projectID, ok := d.GetOk("project_id"); ok {
		createRequest["project_id"] = projectID.(string)
	}

	config := createRequest["config"].(map[string]interface{})

	// Add region
	if region, ok := d.GetOk("region"); ok {
		config["region"] = region.(string)
	}

	// Add idle_termination_minutes
	if idleTermination, ok := d.GetOk("idle_termination_minutes"); ok {
		config["idle_termination_minutes"] = idleTermination.(int)
	}

	// Add maximum_uptime_minutes
	if maximumUptime, ok := d.GetOk("maximum_uptime_minutes"); ok {
		config["maximum_uptime_minutes"] = maximumUptime.(int)
	}

	// Add allowed_azs
	if allowedAzs, ok := d.GetOk("allowed_azs"); ok {
		config["allowed_azs"] = interfaceSliceToStringSlice(allowedAzs.([]interface{}))
	}

	// Add min_resources
	if minResources, ok := d.GetOk("min_resources"); ok {
		config["min_resources"] = minResources.(map[string]interface{})
	}

	// Add max_resources
	if maxResources, ok := d.GetOk("max_resources"); ok {
		config["max_resources"] = maxResources.(map[string]interface{})
	}

	// Add enable_cross_zone_scaling
	if enableCrossZone, ok := d.GetOk("enable_cross_zone_scaling"); ok {
		config["enable_cross_zone_scaling"] = enableCrossZone.(bool)
	}

	// Add auto_select_worker_config
	if autoSelect, ok := d.GetOk("auto_select_worker_config"); ok {
		config["auto_select_worker_config"] = autoSelect.(bool)
	}

	// Add advanced_configurations_json
	if advancedConfigJSON, ok := d.GetOk("advanced_configurations_json"); ok {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(advancedConfigJSON.(string)), &advancedConfig); err != nil {
			return diag.FromErr(fmt.Errorf("invalid advanced_configurations_json: %w", err))
		}
		config["advanced_configurations_json"] = advancedConfig
	}

	// Add flags
	if flagsJSON, ok := d.GetOk("flags"); ok {
		var flags map[string]interface{}
		if err := json.Unmarshal([]byte(flagsJSON.(string)), &flags); err != nil {
			return diag.FromErr(fmt.Errorf("invalid flags: %w", err))
		}
		config["flags"] = flags
	}

	// Add head_node
	if headNodeList, ok := d.GetOk("head_node"); ok {
		headNodes := headNodeList.([]interface{})
		if len(headNodes) > 0 {
			headNode := headNodes[0].(map[string]interface{})
			config["head_node_type"] = buildNodeConfig(headNode)
		}
	}

	// Add worker_nodes
	if workerNodesList, ok := d.GetOk("worker_nodes"); ok {
		workerNodes := workerNodesList.([]interface{})
		workerNodeConfigs := make([]map[string]interface{}, 0, len(workerNodes))
		for _, workerNode := range workerNodes {
			workerNodeMap := workerNode.(map[string]interface{})
			workerNodeConfigs = append(workerNodeConfigs, buildWorkerNodeConfig(workerNodeMap))
		}
		config["worker_node_types"] = workerNodeConfigs
	}

	// Make API call to create compute config
	jsonData, err := json.Marshal(createRequest)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error marshaling request: %w", err))
	}

	log.Printf("[DEBUG] POST /api/v2/cluster-computes - Request: %s", string(jsonData))

	resp, err := client.DoRequest("POST", "/api/v2/cluster-computes", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[ERROR] Failed to create compute config: %v", err)
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading response: %w", err))
	}

	log.Printf("[DEBUG] POST /api/v2/cluster-computes - Response Status: %d, Body: %s", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return diag.Errorf("failed to create compute config: %s - %s", resp.Status, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return diag.FromErr(fmt.Errorf("error parsing response: %w", err))
	}

	// Extract result from response
	resultData, ok := result["result"].(map[string]interface{})
	if !ok {
		return diag.Errorf("error parsing response from API")
	}

	// Set the ID
	if id, ok := resultData["id"].(string); ok {
		d.SetId(id)
		log.Printf("[INFO] Created compute config: id=%s", id)
	} else {
		return diag.Errorf("error: API did not return an ID")
	}

	// Read back the resource to populate computed fields
	return resourceComputeConfigRead(ctx, d, meta)
}

func resourceComputeConfigRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	client := meta.(*Client)

	// Make API call to get compute config
	resp, err := client.DoRequest("GET", fmt.Sprintf("/api/v2/cluster-computes/%s", d.Id()), nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading compute config: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		log.Printf("[WARN] Compute config not found, removing from state: id=%s", d.Id())
		d.SetId("")
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading response: %w", err))
	}

	log.Printf("[DEBUG] GET /api/v2/cluster-computes/%s - Response Status: %d", d.Id(), resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return diag.Errorf("failed to read compute config: %s - %s", resp.Status, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return diag.FromErr(fmt.Errorf("error parsing response: %w", err))
	}

	// Extract result from response
	resultData, ok := result["result"].(map[string]interface{})
	if !ok {
		return diag.Errorf("error parsing response from API")
	}

	// Update state with response
	if name, ok := resultData["name"].(string); ok {
		d.Set("name", name)
	}

	if version, ok := resultData["version"].(float64); ok {
		d.Set("version", int(version))
	}

	if anonymous, ok := resultData["anonymous"].(bool); ok {
		d.Set("anonymous", anonymous)
	}

	if createdAt, ok := resultData["created_at"].(string); ok {
		d.Set("created_at", createdAt)
	}

	if lastModifiedAt, ok := resultData["last_modified_at"].(string); ok {
		d.Set("last_modified_at", lastModifiedAt)
	}

	if projectID, ok := resultData["project_id"].(string); ok {
		d.Set("project_id", projectID)
	}

	// Extract config object
	if configData, ok := resultData["config"].(map[string]interface{}); ok {
		if cloudID, ok := configData["cloud_id"].(string); ok {
			d.Set("cloud_id", cloudID)
		}

		if region, ok := configData["region"].(string); ok {
			d.Set("region", region)
		}

		if idleTermination, ok := configData["idle_termination_minutes"].(float64); ok {
			d.Set("idle_termination_minutes", int(idleTermination))
		}

		if maximumUptime, ok := configData["maximum_uptime_minutes"].(float64); ok {
			d.Set("maximum_uptime_minutes", int(maximumUptime))
		}

		if allowedAzs, ok := configData["allowed_azs"].([]interface{}); ok {
			d.Set("allowed_azs", allowedAzs)
		}

		if minResources, ok := configData["min_resources"].(map[string]interface{}); ok {
			d.Set("min_resources", minResources)
		}

		if maxResources, ok := configData["max_resources"].(map[string]interface{}); ok {
			d.Set("max_resources", maxResources)
		}

		if enableCrossZone, ok := configData["enable_cross_zone_scaling"].(bool); ok {
			d.Set("enable_cross_zone_scaling", enableCrossZone)
		}

		if autoSelect, ok := configData["auto_select_worker_config"].(bool); ok {
			d.Set("auto_select_worker_config", autoSelect)
		}

		if advancedConfig, ok := configData["advanced_configurations_json"].(map[string]interface{}); ok {
			d.Set("advanced_configurations_json", advancedConfig)
		}

		if flags, ok := configData["flags"].(map[string]interface{}); ok {
			d.Set("flags", flags)
		}
	}

	return nil
}

func resourceComputeConfigUpdate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	// Compute configs are immutable - updates create new versions
	// We need to create a new version with new_version flag
	client := meta.(*Client)

	// Build the API request (same as Create, but with new_version flag)
	createRequest := map[string]interface{}{
		"new_version": true,
		"anonymous":   d.Get("anonymous").(bool),
		"config": map[string]interface{}{
			"cloud_id": d.Get("cloud_id").(string),
		},
	}

	// Add name (required for updates)
	if name, ok := d.GetOk("name"); ok {
		createRequest["name"] = name.(string)
	} else {
		return diag.Errorf("name is required when updating a compute config to create a new version")
	}

	// Add optional project_id
	if projectID, ok := d.GetOk("project_id"); ok {
		createRequest["project_id"] = projectID.(string)
	}

	config := createRequest["config"].(map[string]interface{})

	// Add region
	if region, ok := d.GetOk("region"); ok {
		config["region"] = region.(string)
	}

	// Add idle_termination_minutes
	if idleTermination, ok := d.GetOk("idle_termination_minutes"); ok {
		config["idle_termination_minutes"] = idleTermination.(int)
	}

	// Add maximum_uptime_minutes
	if maximumUptime, ok := d.GetOk("maximum_uptime_minutes"); ok {
		config["maximum_uptime_minutes"] = maximumUptime.(int)
	}

	// Add allowed_azs
	if allowedAzs, ok := d.GetOk("allowed_azs"); ok {
		config["allowed_azs"] = interfaceSliceToStringSlice(allowedAzs.([]interface{}))
	}

	// Add min_resources
	if minResources, ok := d.GetOk("min_resources"); ok {
		config["min_resources"] = minResources.(map[string]interface{})
	}

	// Add max_resources
	if maxResources, ok := d.GetOk("max_resources"); ok {
		config["max_resources"] = maxResources.(map[string]interface{})
	}

	// Add enable_cross_zone_scaling
	if enableCrossZone, ok := d.GetOk("enable_cross_zone_scaling"); ok {
		config["enable_cross_zone_scaling"] = enableCrossZone.(bool)
	}

	// Add auto_select_worker_config
	if autoSelect, ok := d.GetOk("auto_select_worker_config"); ok {
		config["auto_select_worker_config"] = autoSelect.(bool)
	}

	// Add advanced_configurations_json
	if advancedConfigJSON, ok := d.GetOk("advanced_configurations_json"); ok {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(advancedConfigJSON.(string)), &advancedConfig); err != nil {
			return diag.FromErr(fmt.Errorf("invalid advanced_configurations_json: %w", err))
		}
		config["advanced_configurations_json"] = advancedConfig
	}

	// Add flags
	if flagsJSON, ok := d.GetOk("flags"); ok {
		var flags map[string]interface{}
		if err := json.Unmarshal([]byte(flagsJSON.(string)), &flags); err != nil {
			return diag.FromErr(fmt.Errorf("invalid flags: %w", err))
		}
		config["flags"] = flags
	}

	// Add head_node
	if headNodeList, ok := d.GetOk("head_node"); ok {
		headNodes := headNodeList.([]interface{})
		if len(headNodes) > 0 {
			headNode := headNodes[0].(map[string]interface{})
			config["head_node_type"] = buildNodeConfig(headNode)
		}
	}

	// Add worker_nodes
	if workerNodesList, ok := d.GetOk("worker_nodes"); ok {
		workerNodes := workerNodesList.([]interface{})
		workerNodeConfigs := make([]map[string]interface{}, 0, len(workerNodes))
		for _, workerNode := range workerNodes {
			workerNodeMap := workerNode.(map[string]interface{})
			workerNodeConfigs = append(workerNodeConfigs, buildWorkerNodeConfig(workerNodeMap))
		}
		config["worker_node_types"] = workerNodeConfigs
	}

	// Make API call to create new version
	jsonData, err := json.Marshal(createRequest)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error marshaling request: %w", err))
	}

	log.Printf("[DEBUG] POST /api/v2/cluster-computes (update) - Request: %s", string(jsonData))

	resp, err := client.DoRequest("POST", "/api/v2/cluster-computes", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[ERROR] Failed to update compute config: %v", err)
		return diag.FromErr(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading response: %w", err))
	}

	log.Printf("[DEBUG] POST /api/v2/cluster-computes (update) - Response Status: %d, Body: %s", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return diag.Errorf("failed to update compute config: %s - %s", resp.Status, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return diag.FromErr(fmt.Errorf("error parsing response: %w", err))
	}

	// Extract result from response
	resultData, ok := result["result"].(map[string]interface{})
	if !ok {
		return diag.Errorf("error parsing response from API")
	}

	// Update the ID if it changed (new version)
	if id, ok := resultData["id"].(string); ok {
		d.SetId(id)
		log.Printf("[INFO] Updated compute config: id=%s", id)
	}

	// Read back the resource to populate computed fields
	return resourceComputeConfigRead(ctx, d, meta)
}

func resourceComputeConfigDelete(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	client := meta.(*Client)

	log.Printf("[INFO] Deleting compute config: id=%s", d.Id())

	// Make API call to delete compute config
	resp, err := client.DoRequest("DELETE", fmt.Sprintf("/api/v2/cluster-computes/%s", d.Id()), nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error deleting compute config: %w", err))
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] DELETE /api/v2/cluster-computes/%s - Response Status: %d", d.Id(), resp.StatusCode)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[ERROR] Failed to delete compute config: %s - %s", resp.Status, string(body))
		return diag.Errorf("failed to delete compute config: %s - %s", resp.Status, string(body))
	}

	d.SetId("")
	log.Printf("[INFO] Deleted compute config successfully")
	return nil
}

// Helper function to build node config for API request
func buildNodeConfig(nodeMap map[string]interface{}) map[string]interface{} {
	config := map[string]interface{}{
		"instance_type": nodeMap["instance_type"].(string),
	}

	// Add resources
	if resources, ok := nodeMap["resources"].(map[string]interface{}); ok && len(resources) > 0 {
		config["resources"] = resources
	}

	// Add required_resources
	if requiredResourcesList, ok := nodeMap["required_resources"].([]interface{}); ok && len(requiredResourcesList) > 0 {
		requiredResources := requiredResourcesList[0].(map[string]interface{})
		pr := make(map[string]interface{})

		if cpu, ok := requiredResources["cpu"].(int); ok && cpu > 0 {
			pr["cpu"] = cpu
		}
		if memory, ok := requiredResources["memory"].(string); ok && memory != "" {
			pr["memory"] = memory
		}
		if gpu, ok := requiredResources["gpu"].(int); ok && gpu > 0 {
			pr["gpu"] = gpu
		}
		if accelerator, ok := requiredResources["accelerator"].(string); ok && accelerator != "" {
			pr["accelerator"] = accelerator
		}
		if tpu, ok := requiredResources["tpu"].(int); ok && tpu > 0 {
			pr["tpu"] = tpu
		}
		if tpuHosts, ok := requiredResources["tpu_hosts"].(int); ok && tpuHosts > 0 {
			pr["tpu_hosts"] = tpuHosts
		}

		if len(pr) > 0 {
			config["required_resources"] = pr
		}
	}

	// Add labels
	if labels, ok := nodeMap["labels"].(map[string]interface{}); ok && len(labels) > 0 {
		config["labels"] = labels
	}

	// Add required_labels
	if requiredLabels, ok := nodeMap["required_labels"].(map[string]interface{}); ok && len(requiredLabels) > 0 {
		config["required_labels"] = requiredLabels
	}

	// Add advanced_instance_config
	if advancedConfigJSON, ok := nodeMap["advanced_instance_config"].(string); ok && advancedConfigJSON != "" {
		var advancedConfig map[string]interface{}
		if err := json.Unmarshal([]byte(advancedConfigJSON), &advancedConfig); err == nil {
			config["advanced_instance_config"] = advancedConfig
		}
	}

	// Add flags
	if flagsJSON, ok := nodeMap["flags"].(string); ok && flagsJSON != "" {
		var flags map[string]interface{}
		if err := json.Unmarshal([]byte(flagsJSON), &flags); err == nil {
			config["flags"] = flags
		}
	}

	// Add cloud_deployment
	if cloudDeploymentList, ok := nodeMap["cloud_deployment"].([]interface{}); ok && len(cloudDeploymentList) > 0 {
		cloudDeployment := cloudDeploymentList[0].(map[string]interface{})
		cd := make(map[string]interface{})

		if provider, ok := cloudDeployment["provider"].(string); ok && provider != "" {
			cd["provider"] = provider
		}
		if region, ok := cloudDeployment["region"].(string); ok && region != "" {
			cd["region"] = region
		}
		if machinePool, ok := cloudDeployment["machine_pool"].(string); ok && machinePool != "" {
			cd["machine_pool"] = machinePool
		}
		if id, ok := cloudDeployment["id"].(string); ok && id != "" {
			cd["id"] = id
		}

		if len(cd) > 0 {
			config["cloud_deployment"] = cd
		}
	}

	return config
}

// Helper function to build worker node config for API request
func buildWorkerNodeConfig(workerMap map[string]interface{}) map[string]interface{} {
	// Start with the base node config
	config := buildNodeConfig(workerMap)

	// Add worker-specific fields
	if name, ok := workerMap["name"].(string); ok && name != "" {
		config["name"] = name
	}
	if minNodes, ok := workerMap["min_nodes"].(int); ok {
		config["min_nodes"] = minNodes
	}
	if maxNodes, ok := workerMap["max_nodes"].(int); ok {
		config["max_nodes"] = maxNodes
	}
	if marketType, ok := workerMap["market_type"].(string); ok && marketType != "" {
		config["market_type"] = marketType
	}

	return config
}

// Helper function to convert []interface{} to []string
func interfaceSliceToStringSlice(slice []interface{}) []string {
	result := make([]string, len(slice))
	for i, v := range slice {
		result[i] = v.(string)
	}
	return result
}

// suppressEquivalentJSON is a DiffSuppressFunc that ignores differences in JSON formatting
func suppressEquivalentJSON(k, old, new string, d *schema.ResourceData) bool {
	if old == "" && new == "" {
		return true
	}
	if old == "" || new == "" {
		return false
	}

	var oldJSON, newJSON interface{}
	if err := json.Unmarshal([]byte(old), &oldJSON); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(new), &newJSON); err != nil {
		return false
	}

	oldBytes, err := json.Marshal(oldJSON)
	if err != nil {
		return false
	}
	newBytes, err := json.Marshal(newJSON)
	if err != nil {
		return false
	}

	return string(oldBytes) == string(newBytes)
}
