# Azure AKS Basic Example

Creates a new AKS cluster and registers it with Anyscale as a `K8S` cloud, all in a single
`terraform apply`, using the **all-in-one pattern** (`anyscale_cloud` with an embedded
`kubernetes_config`) - the same pattern [`aws-eks-basic`](../aws-eks-basic/) uses, unlike
[`gcp-gke-basic`](../gcp-gke-basic/)'s split pattern. It provisions the resource group, network,
AKS cluster, ADLS Gen2 storage account, and the Anyscale Operator's managed identity, then wires
the result into one `anyscale_cloud` resource.

**Validation status: schema- and `terraform validate`-checked, not yet applied against a real AKS
cluster.** Unlike `aws-eks-basic` and `gcp-gke-basic`, this example has not been run end-to-end -
there is no Azure subscription in this provider's test environment. It has been verified with
`terraform init` and `terraform validate` against the real `azurerm` provider (no Azure
credentials required for that), and `terraform plan` was confirmed to fail only on Azure
authentication, not on any schema or configuration error. Validate it against your own Azure
subscription before relying on it, and please open an issue with anything you find.

If you already have a running AKS cluster and just want to register it with Anyscale, this
example will create a second, redundant cluster rather than adopt yours - same limitation as the
EKS and GKE examples.

## What this creates

- A resource group, VNet, and subnet for the cluster
- An AKS cluster ([`azurerm_kubernetes_cluster`](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/kubernetes_cluster)) with:
  - `oidc_issuer_enabled` and `workload_identity_enabled` set - required for Microsoft Entra
    Workload ID, the mechanism the Anyscale Operator uses to reach Azure Blob Storage without a
    long-lived secret
  - A small `default` system node pool, plus `ondemandcpu` and `spotcpu` node pools
    ([`azurerm_kubernetes_cluster_node_pool`](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/kubernetes_cluster_node_pool))
    for Ray workloads
  - One on-demand GPU node pool per entry in `gpu_node_pool_configs` (a single T4 pool,
    `Standard_NC4as_T4_v3`, by default)
  - The API server restricted to `ingress_cidr_ranges` via `api_server_access_profile`
- An ADLS Gen2 storage account (`is_hns_enabled = true`, required for the `abfss://` scheme) and a
  container for Ray's object storage
- A user-assigned managed identity for the Anyscale Operator, a federated identity credential
  linking it to the operator's Kubernetes ServiceAccount via the cluster's OIDC issuer, and a
  Storage Blob Data Contributor role assignment on the storage account
- An `anyscale_cloud` resource with `compute_stack = "K8S"` and `cloud_provider = "AZURE"`,
  pointing `kubernetes_config.anyscale_operator_iam_identity` at the managed identity's
  **principal ID** (not its client ID - see the schema description) and `object_storage.bucket_name`
  at the storage account's `abfss://` URI

This Terraform config does **not** install the Anyscale Operator itself - that's a separate Helm
step, same as the EKS and GKE examples. When you do, annotate the operator's Kubernetes
ServiceAccount with `azure.workload.identity/client-id` (the `anyscale_operator_client_id` output
below - the client ID, not the principal ID used above) and `azure.workload.identity/tenant-id`,
and label its pods `azure.workload.identity/use: "true"`, per [Microsoft's Workload ID
docs](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster).

## Prerequisites

- Terraform >= 1.0
- An Azure subscription, and credentials Terraform can use (e.g. `az login`)
- Anyscale credentials - either:
  - `export ANYSCALE_CLI_TOKEN="your-token"`, or
  - `~/.anyscale/credentials.json` (same format `anyscale login` produces)

`azure_subscription_id` and `azure_location` have no default and must be supplied - copy
[`terraform.tfvars.example`](./terraform.tfvars.example) to `terraform.tfvars` (already
gitignored) and fill in your own subscription, or pass it explicitly with `-var-file`.

## Running the example

```bash
cd examples/azure-aks-basic
cp terraform.tfvars.example terraform.tfvars  # then edit azure_subscription_id
terraform init
terraform plan
terraform apply
```

There is no `make test-azure-aks-basic` Makefile target - unlike the EKS/GKE examples, this one
cannot run unattended in CI or locally without a real Azure subscription, so it's `init`/`apply`
by hand only for now.

Real Azure costs apply for as long as resources stay up. The CPU and GPU node pools default to
`min_count = 0` (except `default`, which runs 2 nodes), so you won't pay for that capacity until
something scales it up.

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `azure_subscription_id` | Azure subscription ID | *(required, no default)* |
| `azure_location` | Azure region for all resources | *(required, no default)* |
| `aks_cluster_name` | Name for the AKS cluster (and prefix for related resource names) | `anyscale-aks` |
| `gpu_node_pool_configs` | Map of GPU node pool definitions to generate | a single `T4` entry |
| `anyscale_k8s_namespace` | Kubernetes namespace the Anyscale Operator deploys into; must match the federated credential's subject | `anyscale-operator` |
| `ingress_cidr_ranges` | CIDR blocks allowed to reach the AKS API server | `["0.0.0.0/0"]` |
| `cloud_name` | Name of the Anyscale cloud to create | `tf-aks-test` |
| `tags` | Tags applied to every resource that accepts them | `{example = "true", environment = "example"}` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `aks_cluster_name` | Name of the AKS cluster |
| `aks_oidc_issuer_url` | The cluster's OIDC issuer URL, useful when debugging Workload ID federation |
| `anyscale_operator_client_id` | Client ID of the operator's managed identity - for the ServiceAccount's `azure.workload.identity/client-id` annotation |
| `anyscale_operator_principal_id` | Principal ID of the operator's managed identity - what `anyscale_operator_iam_identity` expects |
| `storage_account_name` | The ADLS Gen2 storage account backing the `abfss://` object storage URI |

## See also

- [aws-eks-basic](../aws-eks-basic/) / [gcp-gke-basic](../gcp-gke-basic/) - the EKS and GKE equivalents
- [Cloud resource documentation](../../docs/resources/cloud.md)
- [Cloud Resources guide](../../docs/guides/cloud-resources.md) - cross-cutting behavior, including the current Azure/AKS support status
- [Anyscale Kubernetes foundation modules](https://github.com/anyscale/terraform-kubernetes-anyscale-foundation-modules) - Anyscale's own reference AKS module (`examples/azure/aks-new_cluster`), which this example's infrastructure pattern is adapted from
- [Deploy an AKS cluster with Microsoft Entra Workload ID](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster) - upstream Azure docs for the federation mechanism used here
- [Anyscale documentation](https://docs.anyscale.com/)
