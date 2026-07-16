# GCP GKE Basic Example

Creates a new GKE cluster and registers it with Anyscale as a `K8S` cloud, using the **split
deployment pattern**: an empty `anyscale_cloud` plus a separate `anyscale_cloud_resource` with
`compute_stack = "K8S"`. Unlike `gcp-vm-basic`, this example does **not** assume you already have
infrastructure to point at - it provisions the VPC, GCS bucket, service account, and the GKE
cluster itself, then wires the result into the `anyscale_cloud_resource`.

If you already have a running GKE cluster and just want to register it with Anyscale, this
example will create a second, redundant cluster rather than adopt yours. Neither Kubernetes
example in this repo supports that today - see [aws-eks-basic](../aws-eks-basic/), which only
differs from this example in using the all-in-one pattern instead of split. Open an issue if a
bring-your-own-cluster example would help.

## What this creates

- A VPC, subnet, and firewall rules allowing HTTPS (443) and SSH (22) from `ingress_cidr_ranges`
- A GCS bucket for Anyscale object storage, via Anyscale's [cloud foundation
  modules](https://github.com/anyscale/terraform-google-anyscale-cloudfoundation-modules)
- An optional Filestore instance for shared node storage (`enable_filestore`)
- A dedicated GKE node service account, granted the roles the Anyscale Operator and Ray need
  (`storage.admin`, `file.editor`, `iam.serviceAccountTokenCreator`, `logging.logWriter`,
  `monitoring.metricWriter`, `monitoring.viewer`, `artifactregistry.reader`), plus a Workload
  Identity binding to the in-cluster `anyscale-operator` Kubernetes service account in the
  `anyscale_k8s_namespace` namespace
- A GKE cluster ([`terraform-google-modules/kubernetes-engine`](https://registry.terraform.io/modules/terraform-google-modules/kubernetes-engine/google/latest)
  v34) with four node pools: `default-node-pool` (small on-demand pool for cluster
  components - CoreDNS, the Anyscale Operator), `ondemand-cpu` / `spot-cpu` (general-purpose CPU
  capacity for Ray workloads), and one `ondemand-gpu-<type>` / `spot-gpu-<type>` pair per entry in
  `gpu_instance_configs` (a single `T4` pair by default)
- An `anyscale_cloud` (empty) plus an `anyscale_cloud_resource` with `compute_stack = "K8S"`,
  pointing its `kubernetes_config` at the GKE node service account email and available zones

## Prerequisites

- Terraform >= 1.0
- GCP credentials with permission to create VPCs, GKE clusters, service accounts, and GCS
  buckets (e.g. `gcloud auth application-default login`)
- Anyscale credentials - either:
  - `export ANYSCALE_CLI_TOKEN="your-token"`, or
  - `~/.anyscale/credentials.json` (same format `anyscale login` produces)

`google_region` and `google_project_id` have no default and must be supplied - copy
[`terraform.tfvars.example`](./terraform.tfvars.example) to `terraform.tfvars` (already
gitignored) and fill in your own project, or pass it explicitly with `-var-file`.

## Running the example

```bash
cd examples/gcp-gke-basic
cp terraform.tfvars.example terraform.tfvars  # then edit google_project_id
terraform init
terraform plan
terraform apply
```

Or use the repo's Makefile wrapper, which runs apply and destroy with a unique `cloud_name`
suffix and a cleanup trap so a failed apply doesn't leak resources - you still need your own
`terraform.tfvars` in place first, since the wrapper only overrides `cloud_name`:

```bash
make test-gcp-gke-basic
```

Real GCP costs apply for as long as resources stay up. The CPU and GPU node pools all default to
`min_count = 0` / `initial_node_count = 0` (except `default-node-pool`, which runs 2 nodes), so
you won't pay for that capacity until something scales it up.

If you only need one half of the cycle (for example, debugging a failed apply without
re-destroying working resources), use the paired targets directly with a shared `SUFFIX`:

```bash
make apply-gcp-gke-basic SUFFIX=dev1
# ... inspect, iterate ...
make destroy-gcp-gke-basic SUFFIX=dev1
```

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `google_region` | GCP region for all resources | *(required, no default)* |
| `google_project_id` | GCP project ID | *(required, no default)* |
| `gke_cluster_name` | Name for the GKE cluster (and prefix for related resource names); must not start with a digit and must be under 23 characters | `anyscale-gke` |
| `gpu_instance_configs` | Map of GPU node pool definitions to generate (see the variable's own description in `variables.tf` for the object shape) | a single `T4` entry |
| `enable_filestore` | Create a shared Filestore instance | `false` |
| `anyscale_k8s_namespace` | Kubernetes namespace the Anyscale Operator deploys into; must match the Workload Identity binding | `anyscale-operator` |
| `ingress_cidr_ranges` | CIDR blocks allowed through the firewall on 443/22 | `["0.0.0.0/0"]` |
| `cloud_name` | Name of the Anyscale cloud to create | `tf-gke-test` |
| `labels` | Labels applied to every resource that accepts them | `{example = true, environment = "example"}` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `cloud_deployment_id` | Pass this to the Anyscale Operator during installation |
| `anyscale_operator_service_account_email` | Email of the GKE node service account bound to the in-cluster Anyscale Operator identity |

## See also

- [aws-eks-basic](../aws-eks-basic/) - the all-in-one pattern equivalent for AWS/EKS
- [Cloud resource documentation](../../docs/resources/cloud.md)
- [Cloud Resources guide](../../docs/guides/cloud-resources.md) - cross-cutting behavior, including the split-deployment pattern this example uses
- [terraform-google-modules/kubernetes-engine](https://registry.terraform.io/modules/terraform-google-modules/kubernetes-engine/google/latest) - upstream module docs
- [Anyscale documentation](https://docs.anyscale.com/)
