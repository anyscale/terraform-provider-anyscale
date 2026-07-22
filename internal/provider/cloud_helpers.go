package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// kubernetesConfigInertFieldDeprecationMessage is shared by both resources'
// kubernetes_config.{namespace,ingress_host,cluster_name,context,
// kubeconfig_path} attributes (C5): expandKubernetesConfig only ever sends
// anyscale_operator_iam_identity/zones/redis_endpoint to the API, so these
// five have never had any effect - they're deprecated, not removed, since
// removing a schema attribute outright is a breaking change on its own
// (batched into a future major with migration notes instead; see
// CLOUD-SYNC-DESIGN.md C5).
const kubernetesConfigInertFieldDeprecationMessage = "not sent to the Anyscale API; has no effect. Will be removed in a future major release - remove from your configuration."

// azureCloudNotSupportedMessage and genericCloudNotSupportedMessage are the
// single source of truth for the AZURE/GENERIC rejection, shared between each
// resource's ValidateConfig (plan-time guard, added for K9 - catches the
// common case where the value is already known in the config) and
// buildProviderConfig's runtime check below (kept as the last line of
// defense for a value that's still unknown at plan time, e.g. interpolated
// from another resource's computed output). Both call sites must produce the
// exact same text so the two behave as one guard with two trigger points,
// not two different messages a user could see for the same limitation.
//
// The Anyscale Platform API genuinely supports AZURE (Kubernetes compute
// stack only) and GENERIC clouds - this is a provider-side gap, not a
// platform limitation. See K8S-CLOUD-CONTRACT.md K8/K9 for the disposition
// trail; the message deliberately does not promise a timeline.
const genericCloudNotSupportedMessage = "generic clouds are not yet supported by this provider"

// validateAzureK8SOnly is the plan-time guard shared by CloudResource and
// CloudResourceResource's ValidateConfig for the AZURE provider (K9/AKS):
// Anyscale does not support Azure VM clouds (confirmed against the backend's
// own validator, which rejects AZURE/GENERIC for any compute_stack other than
// K8S), and an Azure cloud's object storage bucket must be a full abfss://
// URI - unlike AWS/GCP, buildProviderConfig's AZURE case does not auto-prepend
// a scheme, so a bare bucket name would be sent to the API verbatim and
// silently fail to resolve as valid Azure storage rather than erroring
// clearly. Surfacing both checks here, at plan time, means a user seeing this
// error never gets as far as a real (and, for the compute_stack case, always
// rejected) POST /api/v2/clouds call.
func validateAzureK8SOnly(ctx context.Context, computeStack string, objectStorage types.Object) diag.Diagnostics {
	var diags diag.Diagnostics

	if computeStack != "K8S" {
		diags.AddAttributeError(
			path.Root("compute_stack"),
			"Azure Requires Kubernetes Compute Stack",
			"azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds. Set compute_stack = \"K8S\", or use a different cloud_provider for a VM cloud.",
		)
		return diags
	}

	if objectStorage.IsNull() || objectStorage.IsUnknown() {
		return diags
	}

	var osModel ObjectStorageModel
	asDiags := objectStorage.As(ctx, &osModel, basetypes.ObjectAsOptions{})
	diags.Append(asDiags...)
	if asDiags.HasError() || osModel.BucketName.IsNull() || osModel.BucketName.IsUnknown() {
		return diags
	}

	if bucket := osModel.BucketName.ValueString(); bucket != "" && !strings.HasPrefix(bucket, "abfss://") {
		diags.AddAttributeError(
			path.Root("object_storage").AtName("bucket_name"),
			"Azure Object Storage Requires abfss:// URI",
			fmt.Sprintf("azure object storage must be a full abfss:// URI (e.g. abfss://<container>@<account>.dfs.core.windows.net); got %q. Unlike AWS/GCP, this provider does not auto-prepend a scheme for Azure - pass the complete URI the same way you would to `anyscale cloud register --cloud-storage-bucket-name`. See https://docs.anyscale.com/reference/cli/cloud#cloud-cli for details.", bucket),
		)
	}

	return diags
}

// validateMountPathSupported rejects file_storage.mount_path on AWS, where
// the backend genuinely has no field to store it, confirmed by live-infra
// readback (see MOUNT-PATH-BUG-TRACE.md): AWSNFSResources has only
// efs_id/mount_target_ip, no path field - a live apply-then-readback showed
// the value silently discarded. GCP Filestore (root_dir) and Azure/Generic
// (their own NfsMountPath) both have a real field and are left alone here.
//
// The Kubernetes-native shared-storage mechanism
// (persistent_volume_claim/csi_ephemeral_volume_driver - also a path-less
// proto) is deliberately NOT checked here: the schema's own bidirectional
// stringvalidator.ConflictsWith already rejects mount_path alongside those
// two fields, so a second check here would just double the error for the
// same problem. Keying the AWS check off which fields are actually set (not
// off compute_stack) means a K8S cloud using mount_targets (not
// persistent_volume_claim/csi_ephemeral_volume_driver) still resolves to the
// provider's own real NFS field where one exists (GCP root_dir, Azure/Generic
// NfsMountPath).
func validateMountPathSupported(provider string, fileStorage *FileStorageModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if fileStorage == nil {
		return diags
	}

	mountPathSet := !fileStorage.MountPath.IsNull() && !fileStorage.MountPath.IsUnknown() && fileStorage.MountPath.ValueString() != ""
	if !mountPathSet {
		return diags
	}

	if provider == "AWS" {
		diags.AddAttributeError(
			path.Root("file_storage").AtName("mount_path"),
			"mount_path Not Supported On AWS",
			"mount_path is a GCP Filestore / Azure NFS concept - AWS EFS-backed clouds have no backend field to store it, so the value is silently ignored rather than erroring (confirmed via a live create against the real API: the configured value never reaches the stored cloud). This is not a rejection of a valid setting, it is catching a config value that would otherwise silently do nothing. Remove mount_path; use mount_targets to specify EFS mount target addresses instead.",
		)
	}

	return diags
}

// rejectFieldOnK8S is the shared mechanics behind
// validateSubnetNamesSupported (GCP) and validateSubnetIDsSupported (AWS):
// both provider branches share the same underlying bug (a subnet-list
// conversion block gated only on the field being non-empty, not on
// compute_stack, that reassigns the same NetworkInfo the Kubernetes branch
// already wrote from kubernetes_config.zones - confirmed independently for
// both providers, cloud_deployment_model.go:44 then :162 for AWS or :215 for
// GCP). Deliberately takes the full title/message rather than assembling one
// from a field name: AWS's actual behavior is NOT symmetric with GCP (see
// validateSubnetIDsSupported), so a shared templated message would either
// overclaim or underclaim depending on which AWS attribute was set - only
// the "is this set, and is compute_stack K8S" condition and the
// AddAttributeError mechanics are genuinely shared.
func rejectFieldOnK8S(computeStack string, isFieldSet bool, attrPath path.Path, title, message string) diag.Diagnostics {
	var diags diag.Diagnostics
	if !isFieldSet || computeStack != "K8S" {
		return diags
	}
	diags.AddAttributeError(attrPath, title, message)
	return diags
}

// validateSubnetNamesSupported rejects gcp_config.subnet_names when
// compute_stack is K8S. subnet_names is a VM-compute concept - the backend's
// GCP-provider conversion branch that applies it is gated only on the field
// being non-empty, not on compute_stack, so it runs unconditionally after the
// K8S branch already wrote NetworkInfo from kubernetes_config.zones in the
// same function. This is a confirmed, real overwrite (traced precisely, not
// inferred, independently re-verified by architect): the K8S-derived zone
// list is discarded and replaced with every zone in the region, each
// carrying the configured subnet id even though GKE never claimed those
// extra zones or that subnet - a genuine corruption of the cloud's
// registered networking, not a benign no-op. GKE networking comes entirely
// from kubernetes_config.zones; subnet_names has no role there. GCP VM
// clouds are unaffected and genuinely support multiple subnets - see
// subnet-names-gcp-supports-multiple-no-cardinality-validator for why a
// cardinality check on VM would be wrong.
func validateSubnetNamesSupported(computeStack string, gcpConfig *GCPConfigModel) diag.Diagnostics {
	if gcpConfig == nil {
		return nil
	}

	subnetNamesSet := !gcpConfig.SubnetNames.IsNull() && !gcpConfig.SubnetNames.IsUnknown() && len(gcpConfig.SubnetNames.Elements()) > 0
	return rejectFieldOnK8S(computeStack, subnetNamesSet,
		path.Root("gcp_config").AtName("subnet_names"),
		"subnet_names Not Supported On Kubernetes Compute",
		"subnet_names is a VM-compute concept - GKE networking comes entirely from kubernetes_config.zones. Setting subnet_names on a Kubernetes cloud does not just have no effect, it silently corrupts the registered networking: the backend discards the real zones and replaces them with every zone in the region, each carrying this subnet id (confirmed by tracing the actual conversion code, which applies subnet_names unconditionally after the Kubernetes zone list is written). Remove subnet_names for Kubernetes compute; it has no role there.",
	)
}

// validateSubnetIDsSupported rejects aws_config.subnet_ids and
// subnet_ids_to_az when compute_stack is K8S. Unlike the GCP case, this is
// NOT symmetric - traced precisely rather than assumed from the GCP shape.
// The AWS-provider conversion block shares the identical unguarded-clobber
// mechanism (same function, same NetworkInfo field, no compute_stack check),
// but only reaches it if a pre-existing, unrelated length guard passes
// (len(SubnetIds) == len(Zones)), and whether that guard passes depends on
// which of the two Terraform attributes was used, not on compute_stack:
//   - subnet_ids_to_az (map form) builds SubnetIDs and Zones together in
//     this provider's own expand code, always equal length, so it passes the
//     guard and reaches the real clobber - same corruption as GCP.
//   - subnet_ids (plain list form) never gets a matching Zones value from
//     this provider's expand code (or from the CLI's register command, or
//     from the backend's own VM-only auto-derivation), so it trips the
//     length guard first and produces a confusing, unrelated backend error
//     ("subnet IDs do not match zones") instead of ever reaching the write.
//
// Rejecting both anyway, not just the one that reaches the Go-level clobber:
// the user-facing mistake (AWS subnet config on a stack that does not use
// it) is identical either way, and the plain-list form falling through to a
// different confusing error today is an accident of this provider's own
// expand code, not a designed safeguard worth preserving.
func validateSubnetIDsSupported(computeStack string, awsConfig *AWSConfigModel) diag.Diagnostics {
	if awsConfig == nil {
		return nil
	}

	subnetIDsSet := !awsConfig.SubnetIDs.IsNull() && !awsConfig.SubnetIDs.IsUnknown() && len(awsConfig.SubnetIDs.Elements()) > 0
	subnetIDsToAZSet := !awsConfig.SubnetIDsToAZ.IsNull() && !awsConfig.SubnetIDsToAZ.IsUnknown() && len(awsConfig.SubnetIDsToAZ.Elements()) > 0
	if !subnetIDsSet && !subnetIDsToAZSet {
		return nil
	}

	attr := "subnet_ids"
	if subnetIDsToAZSet {
		attr = "subnet_ids_to_az"
	}
	return rejectFieldOnK8S(computeStack, true,
		path.Root("aws_config").AtName(attr),
		fmt.Sprintf("%s Not Supported On Kubernetes Compute", attr),
		fmt.Sprintf("%s is a VM-compute concept - EKS networking comes entirely from kubernetes_config.zones. Setting it on a Kubernetes cloud does not do what you expect: depending on which subnet attribute is used it either silently corrupts the registered networking (subnet_ids_to_az) or triggers an unrelated backend error about subnet and zone counts not matching (subnet_ids) - confirmed by tracing the actual conversion code either way. Remove %s for Kubernetes compute; it has no role there.", attr, attr),
	)
}

// ResolveCloudNameToID converts a cloud name to a cloud ID by querying the Anyscale API.
// If multiple clouds have the same name, it returns the most recently created one.
//
// This function consolidates the cloud name resolution logic that was previously
// duplicated across multiple resources and data sources.
//
// Example usage:
//
//	cloudID, err := ResolveCloudNameToID(ctx, r.client, cloudName)
//	if err != nil {
//	    resp.Diagnostics.AddError(
//	        "Cloud Name Resolution Failed",
//	        fmt.Sprintf("Failed to resolve cloud name '%s' to ID: %s", cloudName, err.Error()),
//	    )
//	    return
//	}
func ResolveCloudNameToID(ctx context.Context, client *Client, cloudName string) (string, error) {
	tflog.Debug(ctx, "Resolving cloud name to ID", map[string]any{"cloud_name": cloudName})

	// Fetch all clouds
	cloudsResp, err := DoRequestAndParse[CloudsListResponse](
		ctx,
		client,
		"GET",
		"/api/v2/clouds",
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to list clouds: %w", err)
	}

	// Find matching cloud(s), picking the most recently created on a name collision
	matchedCloudID := PickMostRecentMatch(ctx, "cloud", cloudName, cloudsResp.Results,
		func(c CloudResult) bool { return c.Name == cloudName },
		func(c CloudResult) string { return c.ID },
		func(c CloudResult) string { return c.CreatedAt },
	)

	if matchedCloudID == "" {
		return "", fmt.Errorf("no cloud found with name '%s'", cloudName)
	}

	tflog.Info(ctx, "Resolved cloud name to ID", map[string]any{
		"cloud_name": cloudName,
		"cloud_id":   matchedCloudID,
	})

	return matchedCloudID, nil
}

// resolveCloudIDFilter resolves a data source's cloud_id/cloud_name filter pair down to a single
// cloud_id, shared by every data source that offers both (project(s), service(s)). If cloudName
// is null, cloudID's own value is returned unchanged. On resolution failure it adds an error
// diagnostic itself and returns ok=false; callers should return immediately in that case.
func resolveCloudIDFilter(ctx context.Context, client *Client, cloudID, cloudName types.String, diags *diag.Diagnostics) (resolvedCloudID string, ok bool) {
	if cloudName.IsNull() {
		return cloudID.ValueString(), true
	}

	name := cloudName.ValueString()
	tflog.Info(ctx, "Resolving cloud_name to cloud_id", map[string]any{"cloud_name": name})

	resolvedID, err := ResolveCloudNameToID(ctx, client, name)
	if err != nil {
		AddAPIError(diags, "resolve cloud name", err)
		return "", false
	}
	return resolvedID, true
}

// listCloudResources pages through every cloud resource (deployment) attached
// to a cloud. Both anyscale_cloud_resource and the anyscale_cloud data source
// need this same listing - centralizing it means there's exactly one place
// that paginates GET /clouds/{id}/resources instead of several copies that
// could drift (e.g. one paginating, one only reading page 1).
func listCloudResources(ctx context.Context, client *Client, cloudID string) ([]CloudDeploymentResult, error) {
	results, err := PaginatedRequest(
		ctx, client, fmt.Sprintf("/api/v2/clouds/%s/resources", cloudID), nil,
		func(body []byte) ([]CloudDeploymentResult, *string, error) {
			var deploymentsResp CloudDeploymentsResponse
			if err := json.Unmarshal(body, &deploymentsResp); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal cloud resources: %w", err)
			}
			return deploymentsResp.Results, deploymentsResp.Metadata.NextPagingToken, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud resources: %w", err)
	}
	return results, nil
}

// findDefaultInCloudResources returns the resource flagged as the cloud's
// primary/default deployment, or nil if none is (a brand-new empty cloud has
// zero resources at all; a cloud with only non-default resources is possible
// in principle, though not through this provider).
func findDefaultInCloudResources(results []CloudDeploymentResult) *CloudDeploymentResult {
	for i := range results {
		if results[i].IsDefault {
			return &results[i]
		}
	}
	return nil
}

// buildProviderConfig populates deployReq's AWSConfig/GCPConfig/KubernetesConfig/
// ObjectStorage/FileStorage fields for the given provider and compute_stack, applying the
// same AWS/GCP x VM/K8S required-vs-optional rules that anyscale_cloud and
// anyscale_cloud_resource both need. Consolidates what was previously near-identical
// branching duplicated in resource_cloud.go's addCloudResource and
// resource_cloud_resource.go's addProviderConfig (workbench #6) - resource-agnostic, so
// both resources' Create paths call this directly rather than keeping their own copies.
//
// provider is normalized via strings.ToUpper so a lowercase/mixed-case value (e.g. "aws")
// still matches - see the case-normalization bugfix this consolidation builds on.
func buildProviderConfig(ctx context.Context, deployReq *CloudDeploymentRequest, provider, computeStack string, awsConfig, gcpConfig, azureConfig, kubernetesConfig, objectStorage, fileStorage types.Object) error {
	switch strings.ToUpper(provider) {
	case "AWS":
		if computeStack == "K8S" {
			// K8S: kubernetes_config + object_storage required, aws_config optional
			if kubernetesConfig.IsNull() {
				return fmt.Errorf("kubernetes_config is required when cloud_provider is AWS and compute_stack is K8S")
			}

			k8sConfig, err := expandKubernetesConfig(ctx, kubernetesConfig)
			if err != nil {
				return fmt.Errorf("failed to expand kubernetes_config: %w", err)
			}
			if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for AWS K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			if objectStorage.IsNull() {
				return fmt.Errorf("object_storage is required when cloud_provider is AWS and compute_stack is K8S")
			}

			objStorage, err := expandObjectStorage(ctx, objectStorage)
			if err != nil {
				return fmt.Errorf("failed to expand object_storage: %w", err)
			}
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "s3://") {
				bucketName = "s3://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}

			// aws_config is optional for K8S
			if !awsConfig.IsNull() {
				expanded, err := expandAWSConfig(ctx, awsConfig)
				if err != nil {
					return fmt.Errorf("failed to expand aws_config: %w", err)
				}
				deployReq.AWSConfig = expanded
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		} else {
			// VM: aws_config required
			if awsConfig.IsNull() {
				return fmt.Errorf("aws_config is required when cloud_provider is AWS and compute_stack is VM")
			}

			expanded, err := expandAWSConfig(ctx, awsConfig)
			if err != nil {
				return fmt.Errorf("failed to expand aws_config: %w", err)
			}
			deployReq.AWSConfig = expanded

			// object_storage and file_storage optional
			if !objectStorage.IsNull() {
				objStorage, err := expandObjectStorage(ctx, objectStorage)
				if err != nil {
					return fmt.Errorf("failed to expand object_storage: %w", err)
				}
				bucketName := objStorage.BucketName
				if !strings.HasPrefix(bucketName, "s3://") {
					bucketName = "s3://" + bucketName
				}
				deployReq.ObjectStorage = &ObjectStorage{
					BucketName: bucketName,
					Region:     objStorage.Region,
					Endpoint:   objStorage.Endpoint,
				}
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		}

	case "GCP":
		if computeStack == "K8S" {
			// K8S: kubernetes_config + object_storage required, gcp_config optional
			if kubernetesConfig.IsNull() {
				return fmt.Errorf("kubernetes_config is required when cloud_provider is GCP and compute_stack is K8S")
			}

			k8sConfig, err := expandKubernetesConfig(ctx, kubernetesConfig)
			if err != nil {
				return fmt.Errorf("failed to expand kubernetes_config: %w", err)
			}
			if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
				return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for GCP K8S clouds")
			}
			deployReq.KubernetesConfig = k8sConfig

			if objectStorage.IsNull() {
				return fmt.Errorf("object_storage is required when cloud_provider is GCP and compute_stack is K8S")
			}

			objStorage, err := expandObjectStorage(ctx, objectStorage)
			if err != nil {
				return fmt.Errorf("failed to expand object_storage: %w", err)
			}
			bucketName := objStorage.BucketName
			if !strings.HasPrefix(bucketName, "gs://") {
				bucketName = "gs://" + bucketName
			}
			deployReq.ObjectStorage = &ObjectStorage{
				BucketName: bucketName,
				Region:     objStorage.Region,
				Endpoint:   objStorage.Endpoint,
			}

			// gcp_config is optional for K8S
			if !gcpConfig.IsNull() {
				expanded, err := expandGCPConfig(ctx, gcpConfig)
				if err != nil {
					return fmt.Errorf("failed to expand gcp_config: %w", err)
				}
				deployReq.GCPConfig = expanded
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		} else {
			// VM: gcp_config required
			if gcpConfig.IsNull() {
				return fmt.Errorf("gcp_config is required when cloud_provider is GCP and compute_stack is VM")
			}

			expanded, err := expandGCPConfig(ctx, gcpConfig)
			if err != nil {
				return fmt.Errorf("failed to expand gcp_config: %w", err)
			}
			deployReq.GCPConfig = expanded

			// object_storage and file_storage optional
			if !objectStorage.IsNull() {
				objStorage, err := expandObjectStorage(ctx, objectStorage)
				if err != nil {
					return fmt.Errorf("failed to expand object_storage: %w", err)
				}
				bucketName := objStorage.BucketName
				if !strings.HasPrefix(bucketName, "gs://") {
					bucketName = "gs://" + bucketName
				}
				deployReq.ObjectStorage = &ObjectStorage{
					BucketName: bucketName,
					Region:     objStorage.Region,
					Endpoint:   objStorage.Endpoint,
				}
			}

			if !fileStorage.IsNull() {
				expanded, err := expandFileStorage(ctx, fileStorage)
				if err != nil {
					return fmt.Errorf("failed to expand file_storage: %w", err)
				}
				deployReq.FileStorage = expanded
			}
		}

	case "AZURE":
		// Azure is Kubernetes-only (confirmed against the backend's own
		// validator, which rejects AZURE for any compute_stack other than
		// K8S) - this defensive check is the last line of defense behind
		// ValidateConfig's plan-time guard (validateAzureK8SOnly), which
		// catches the common case where compute_stack is already known in
		// the config; this one covers a value that was still unknown at
		// plan time (e.g. interpolated from another resource).
		if computeStack != "K8S" {
			return fmt.Errorf("azure clouds only support compute_stack = \"K8S\" - Anyscale does not support Azure VM clouds")
		}

		if kubernetesConfig.IsNull() {
			return fmt.Errorf("kubernetes_config is required when cloud_provider is AZURE")
		}

		k8sConfig, err := expandKubernetesConfig(ctx, kubernetesConfig)
		if err != nil {
			return fmt.Errorf("failed to expand kubernetes_config: %w", err)
		}
		if k8sConfig == nil || k8sConfig.AnyscaleOperatorIAMIdentity == "" {
			return fmt.Errorf("kubernetes_config.anyscale_operator_iam_identity is required for AZURE K8S clouds")
		}
		deployReq.KubernetesConfig = k8sConfig

		if objectStorage.IsNull() {
			return fmt.Errorf("object_storage is required when cloud_provider is AZURE")
		}

		objStorage, err := expandObjectStorage(ctx, objectStorage)
		if err != nil {
			return fmt.Errorf("failed to expand object_storage: %w", err)
		}
		// Azure object storage is a full abfss://container@account.dfs.core.windows.net
		// URI, never a bare bucket name - unlike AWS/GCP, there is no scheme
		// to auto-prepend here. ValidateConfig's validateAzureK8SOnly already
		// rejects a bucket_name that doesn't start with abfss:// at plan
		// time; pass it through completely verbatim.
		deployReq.ObjectStorage = &ObjectStorage{
			BucketName: objStorage.BucketName,
			Region:     objStorage.Region,
			Endpoint:   objStorage.Endpoint,
		}

		// azure_config is optional (tenant_id has no equivalent requirement
		// the way anyscale_operator_iam_identity does above).
		if !azureConfig.IsNull() {
			expanded, err := expandAzureConfig(ctx, azureConfig)
			if err != nil {
				return fmt.Errorf("failed to expand azure_config: %w", err)
			}
			deployReq.AzureConfig = expanded
		}

		if !fileStorage.IsNull() {
			expanded, err := expandFileStorage(ctx, fileStorage)
			if err != nil {
				return fmt.Errorf("failed to expand file_storage: %w", err)
			}
			deployReq.FileStorage = expanded
		}

	case "GENERIC":
		return fmt.Errorf("%s", genericCloudNotSupportedMessage)
	}

	return nil
}

// bucketSchemePrefixes are the recognized cloud-storage URI scheme prefixes
// that stripBucketPrefix (cloud_config_flatten.go) and
// bucketNameSemanticEqualPlanModifier both treat as optional/equivalent to
// their bare form - s3:// for AWS, gs:// for GCP. Azure's abfss:// is a
// compound URI (container@account.dfs.core.windows.net) with no bare-name
// equivalent, so it is deliberately excluded here and always compared
// exactly - see buildProviderConfig's AZURE case, which never prepends or
// strips a scheme for it either.
var bucketSchemePrefixes = []string{"s3://", "gs://"}

// stripAnyBucketSchemePrefix removes a recognized cloud-storage scheme
// prefix from bucketName if present, otherwise returns it unchanged.
func stripAnyBucketSchemePrefix(bucketName string) string {
	for _, prefix := range bucketSchemePrefixes {
		if stripped := strings.TrimPrefix(bucketName, prefix); stripped != bucketName {
			return stripped
		}
	}
	return bucketName
}

// bucketNameSemanticEqualPlanModifier is the fix for the object_storage.bucket_name
// import round-trip bug found during the real GKE acceptance run: bucket_name is
// RequiresReplace, but it is Optional (not Computed), so Create can never legally
// rewrite it away from exactly what the user typed - state after a fresh apply
// holds the bare or scheme-prefixed form verbatim from config. ImportState,
// though, recovers the API's own canonical form via flattenObjectStorage's
// stripBucketPrefix, which only un-prefixes AWS's s3:// and never touches GCP's
// gs:// (see its doc comment: the API always returns GCP buckets gs://-prefixed,
// by design, matching the schema's own documented convention that GCP users
// write the prefix themselves). A GCP cloud whose bucket was written bare -
// exactly what the flagship gcp-gke-basic example's module output produced -
// therefore diverges the moment it is imported: state says "my-bucket", the
// freshly-imported value says "gs://my-bucket". Same bucket, and without this
// modifier RequiresReplace would force a destroy-and-recreate of a live, working
// cloud over nothing but a scheme prefix.
//
// This deliberately does NOT canonicalize to one form (always-prepend or
// always-strip): doing so would spuriously replace existing clouds whose config
// already uses the OTHER form. Instead, when the planned config value and the
// prior state value name the same bucket (differ only by a recognized scheme
// prefix), this keeps the state value - so RequiresReplace, which runs
// afterward in the same attribute's PlanModifiers list and compares plan
// against state, sees no change at all. Must be ordered BEFORE
// stringplanmodifier.RequiresReplace() in bucket_name's PlanModifiers list to
// take effect (plan modifiers thread PlanValue from one into the next).
type bucketNameSemanticEqualPlanModifier struct{}

func (m bucketNameSemanticEqualPlanModifier) Description(ctx context.Context) string {
	return "Treats a bucket name with or without its cloud-storage scheme prefix (s3://, gs://) as the same value, so a scheme-only difference between state and config does not force replacement."
}

func (m bucketNameSemanticEqualPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m bucketNameSemanticEqualPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	if req.PlanValue.Equal(req.StateValue) {
		return
	}
	if stripAnyBucketSchemePrefix(req.PlanValue.ValueString()) == stripAnyBucketSchemePrefix(req.StateValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}

// stringSetsEqual reports whether a and b contain exactly the same set of
// strings - true bidirectional equality (same cardinality after
// deduplication AND full mutual containment), not a one-directional subset
// check. A subset check would wrongly treat a genuine partial-overlap
// change (e.g. one subnet swapped for another) as unchanged; order is
// deliberately ignored, since aws_config.subnet_ids (list) and
// subnet_ids_to_az (map) have no shared ordering to compare.
func stringSetsEqual(a, b []string) bool {
	setA := make(map[string]struct{}, len(a))
	for _, s := range a {
		setA[s] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, s := range b {
		setB[s] = struct{}{}
	}
	if len(setA) != len(setB) {
		return false
	}
	for s := range setA {
		if _, ok := setB[s]; !ok {
			return false
		}
	}
	return true
}

// awsSubnetIDsSemanticEqualPlanModifier is the fix for the aws_config
// list/map import round-trip bug: subnet_ids and subnet_ids_to_az are two
// alternate representations of the same underlying subnets, but the API
// only ever returns the parallel-array shape, so flattenAWSConfig recovers
// ONLY subnet_ids_to_az at import (see its own doc comment) - subnet_ids
// always comes back null. A configuration originally written with plain
// subnet_ids therefore diffs on import: config sets subnet_ids (state is
// null), state has subnet_ids_to_az populated (config is null there) - two
// independent RequiresReplace triggers, on two different attributes, both
// pointing at the same substance-free difference.
//
// This modifier handles the subnet_ids side: when the plan (config) sets
// subnet_ids explicitly but state has no subnet_ids of its own (the case
// above), it compares the plan's ID set against the sibling
// subnet_ids_to_az's recovered STATE key set - subnet_ids_to_az's own state
// is the only place a real recovered value can be found, since subnet_ids
// itself is never recovered. On an exact set match, it pins subnet_ids'
// plan value back to its (null) state value, so the subsequent
// RequiresReplace in the same attribute's PlanModifiers slice sees no
// change. Must be ordered BEFORE listplanmodifier.RequiresReplace().
//
// Deliberately does nothing when state already holds a real subnet_ids
// value (not today's behavior, but defensive: normal plan-vs-state
// comparison already handles that case correctly without this modifier's
// help) or when the config genuinely differs in substance from what was
// recovered (a real topology change still replaces, as it should - there is
// no in-place update path for this field).
type awsSubnetIDsSemanticEqualPlanModifier struct{}

func (m awsSubnetIDsSemanticEqualPlanModifier) Description(ctx context.Context) string {
	return "Treats subnet_ids as unchanged when its values match aws_config.subnet_ids_to_az's recovered keys as a set, so a configuration using the list form does not force replacement against the map form recovered at import."
}

func (m awsSubnetIDsSemanticEqualPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m awsSubnetIDsSemanticEqualPlanModifier) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	if !req.StateValue.IsNull() {
		// subnet_ids already carries a real state value (or this is Create,
		// where StateValue is unknown/absent) - ordinary plan-vs-state
		// comparison already does the right thing, nothing to reconcile via
		// the sibling attribute.
		return
	}

	var planIDs []string
	if d := req.PlanValue.ElementsAs(ctx, &planIDs, false); d.HasError() || len(planIDs) == 0 {
		return
	}

	var stateSubnetToAZ map[string]string
	if d := req.State.GetAttribute(ctx, path.Root("aws_config").AtName("subnet_ids_to_az"), &stateSubnetToAZ); d.HasError() || len(stateSubnetToAZ) == 0 {
		return
	}

	stateIDs := make([]string, 0, len(stateSubnetToAZ))
	for id := range stateSubnetToAZ {
		stateIDs = append(stateIDs, id)
	}

	if stringSetsEqual(planIDs, stateIDs) {
		resp.PlanValue = req.StateValue
	}
}

// awsSubnetIDsToAZSemanticEqualPlanModifier is
// awsSubnetIDsSemanticEqualPlanModifier's sibling for subnet_ids_to_az -
// same bug, same fix shape, opposite direction. When the plan (config)
// leaves subnet_ids_to_az null but state holds a real recovered map (the
// case that fires when the original configuration used plain subnet_ids
// instead), it compares that recovered map's key set against the sibling
// subnet_ids' CONFIG value - config, not state, because subnet_ids' own
// state is never populated (see the sibling modifier's doc comment), so
// config is the only place a real value to compare against exists. On an
// exact set match, it pins subnet_ids_to_az's plan value back to the
// recovered state map, so the subsequent RequiresReplace sees no change.
// Must be ordered BEFORE mapplanmodifier.RequiresReplace().
//
// Neither this modifier nor its sibling depends on the other's output:
// this one reads subnet_ids' CONFIG (always known, regardless of modifier
// ordering), the sibling reads this attribute's STATE (also always known
// going in) - never each other's in-flight PLAN value - so there is no
// ordering dependency between the two attributes' modifier chains.
type awsSubnetIDsToAZSemanticEqualPlanModifier struct{}

func (m awsSubnetIDsToAZSemanticEqualPlanModifier) Description(ctx context.Context) string {
	return "Treats subnet_ids_to_az as unchanged when its keys match aws_config.subnet_ids's configured values as a set, so a configuration using the list form does not force replacement on the map form recovered at import."
}

func (m awsSubnetIDsToAZSemanticEqualPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m awsSubnetIDsToAZSemanticEqualPlanModifier) PlanModifyMap(ctx context.Context, req planmodifier.MapRequest, resp *planmodifier.MapResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if !req.PlanValue.IsNull() {
		// Explicit config value for subnet_ids_to_az itself - compare
		// directly against state as normal, nothing to reconcile via the
		// sibling attribute.
		return
	}

	var stateSubnetToAZ map[string]string
	if d := req.StateValue.ElementsAs(ctx, &stateSubnetToAZ, false); d.HasError() || len(stateSubnetToAZ) == 0 {
		return
	}

	var configSubnetIDs []string
	if d := req.Config.GetAttribute(ctx, path.Root("aws_config").AtName("subnet_ids"), &configSubnetIDs); d.HasError() || len(configSubnetIDs) == 0 {
		return
	}

	stateIDs := make([]string, 0, len(stateSubnetToAZ))
	for id := range stateSubnetToAZ {
		stateIDs = append(stateIDs, id)
	}

	if stringSetsEqual(configSubnetIDs, stateIDs) {
		resp.PlanValue = req.StateValue
	}
}
