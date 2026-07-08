# Quest Log — terraform-provider-anyscale

Engineering journal. One entry per session. Newest first.

---

## 2026-07-07 — EKS v21 + Bottlerocket Migration

**Branch**: `integration/eks-v21-bottlerocket` (base f5325a5, HEAD 420eef8)  
**Release impact**: Below-patch (examples-only, zero provider Go changes)  
**Party**: tfp-architect, tfp-forge, tfp-assayer, tfp-scribe, tfp-shipwright, terraformer

### What we set out to do

Upgrade `examples/aws-eks-basic` from `terraform-aws-modules/eks` v20.33.1 to v21.24.0, switch node base images from AL2023 to Bottlerocket (separate CPU/GPU images), keep Kubernetes at 1.36+, and validate end-to-end on real AWS. The example had never successfully applied before — v20 unconditionally referenced blocks removed in AWS provider ≥6.37, causing a hard `Unsupported block type` at `terraform validate`.

### What landed

Seven commits on `crystl/tfp-forge` (authored by tfp-forge, reviewed by terraformer/architect, validated by assayer):

1. **02ce313** — eks module 20.33.1→21.24.0; 4 cluster-level renames; taints `list(object)`→`map(object)`; AWS provider floor ≥6.52
2. **25a31c9** — vpc-cni + eks-pod-identity-agent with `before_compute=true` (v21 no longer installs a default CNI)
3. **ddaca6d** — Bottlerocket switch; two-volume `block_device_mappings`; shared local for CPU/GPU groups
4. **0765907** — Makefile: unique `eks_cluster_name` per test target
5. **bf015ba** — Makefile: test tags `workload=tf-provider-e2e-test`, `environment=test`
6. **ac4d669** — `bucket_namespace=global` (aws-anyscale-s3 module default changed)
7. **f38a599** — S3 bucket name includes `account_id` — globally unique by design

Plus two independent commits on `crystl/tfp-assayer`:
- **46c9507** — examples-wide `terraform validate` CI gate (`ci.yml`)
- **989d115** — incidental multi-resource-cloud-basic fix

Documentation on `crystl/tfp-scribe`: new `examples/aws-eks-basic/README.md`, `gpu_instances.tfvars.example`, fixed `examples/README.md`.

### Real-AWS validation

First-ever successful real apply of this example:
- Apply: 112 resources, 0 errors
- Nodes: 2/2 Ready in ~75s on Bottlerocket 1.62.1 (aws-k8s-1.36), containerd 2.2.4+bottlerocket
- All 4 addons healthy: aws-node/vpc-cni, coredns, kube-proxy, eks-pod-identity-agent
- Anyscale operator registered: cloud_deployment_id + cloud_id populated
- Destroy: 112/112 clean, independently verified in AWS console, zero leaks
- Targeted S3 apply/destroy for f38a599 (ran after full E2E): account_id bucket name accepted by S3 API

### The three plan-cannot-catch-this bugs

Every one was found only by running real infrastructure — plan and validate passed in all three cases:

1. **vpc-cni missing** (25a31c9): v21 hardcodes `bootstrap_self_managed_addons=false`, so no CNI is installed by default. Nodes join the cluster and stay permanently `NotReady`. Fixed with explicit `vpc-cni = { before_compute = true }`.

2. **S3 bucket_namespace default changed** (ac4d669): The `aws-anyscale-s3` module changed its default namespace to `account-regional`, which requires bucket names in `<account-id>-<region>-an` format. The example's explicit name was rejected at create time. Fixed with `bucket_namespace = "global"`.

3. **eks_cluster_name Makefile collision** (0765907): The Makefile test targets uniquified `cloud_name` but not `eks_cluster_name`, causing collisions across concurrent test runs in a shared account. Fixed by injecting a Unix timestamp per target.

### Key design decisions

- **vpc-cni `before_compute=true`**: the only way nodes reach `Ready` state on v21. Not optional.
- **Bottlerocket two-volume model**: `disk_size` configures `/dev/xvda` (OS, ~2GB read-only); the data volume for container images is `/dev/xvdb`. Must use `block_device_mappings` to size it. Default node group intentionally left unsized (block_device_mappings omitted).
- **S3 uniqueness by design**: `"${var.eks_cluster_name}-${var.aws_region}-${data.aws_caller_identity.current.account_id}"` with `bucket_namespace=global`. Deterministic, importable, globally unique. Rejected: `account-regional` (fragile suffix validation) and random prefix (non-deterministic across re-applies).
- **Consolidation onto one branch**: the validated E2E ran against a combination across two branches; architect ruled consolidate onto crystl/tfp-forge so the shipped artifact equals the tested artifact.

### Follow-ups (deferred)

- Tag-based sweeper for example EKS clusters (enabled by `workload=tf-provider-e2e-test` tag; destroy-on-exit trap handles normal runs but crash can orphan)
- Review the incidental multi-resource-cloud-basic fix from assayer's branch (989d115) — confirm scope and whether it needs its own tracking

### Cold-start operational notes

Discovered during wind-down; applies to any future session on this repo:

- **`terraform init` is safe** per example directory — it fetches third-party modules and AWS provider. dev_overrides only skips installing the anyscale provider (prints a warning). The hazard is trusting the shared `~/.terraformrc` — it points at whatever binary sits at the main repo root, which may be stale or from another worktree. Use an isolated scratch CLI config for any apply/validate you intend to trust.
- **Worktree file isolation**: files written inside one Crystl worktree are invisible at the same path from outside that worktree during the live session. After merge they land correctly. Do not try to read a teammate's worktree files by path — they will appear missing.
- **`git diff` pathspec silent-empty**: `git diff A B -- path` and `git show commit -- path` can silently return empty results even when real changes exist. Confirmed reproducible and environment-level. If a scoped diff comes back empty, use unscoped diff + grep the filename, or read the file directly off disk.
