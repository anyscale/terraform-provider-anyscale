# Resume Prompt — terraform-provider-anyscale

Use this document to orient a new agent or reviewer picking up work on this repository.

---

## Start Here

Before anything else, read these three files in order:

1. **`CLAUDE.md`** (repo root) — build commands, test commands, provider conventions, authentication, acceptance test setup, Makefile targets
2. **`HANDOFF.md`** (repo root) — current branch status, what's done, what's next, per-hero actions, and operational notes that cost real debugging time last session
3. **`QUEST-LOG.md`** (repo root) — engineering journal; the 2026-07-07 entry covers the full EKS v21 migration including every design decision and the three bugs that only appear at real apply time

---

## Current State (as of 2026-07-07)

The EKS v21 + Bottlerocket migration is **complete and validated**. A pull request needs to be opened.

- **Integration branch**: `integration/eks-v21-bottlerocket` on origin (base f5325a5, HEAD 420eef8)
- **What it contains**: v21 migration, Bottlerocket OS switch, vpc-cni fix, S3 uniqueness fix, Makefile test targets, new CI validate gate, new README for the example
- **What it does NOT change**: zero provider Go code, zero schema, zero go.mod — below-patch, no version bump needed
- **Validated**: 112-resource real-AWS E2E apply + destroy, zero errors, zero leaks; targeted S3 apply/destroy for the account_id naming fix

### Immediate next action
Open a pull request: `integration/eks-v21-bottlerocket` → `main`. See `HANDOFF.md §Per-Hero Next Actions`.

---

## Key Facts to Hold in Memory

### vpc-cni is not optional
`examples/aws-eks-basic/aws_eks.tf` has `vpc-cni = { before_compute = true }` in the `addons` block. Do not remove it. EKS v21 hardcodes `bootstrap_self_managed_addons=false` — without this line, every node joins the cluster and stays permanently `NotReady`. `terraform plan` passes cleanly; the failure is invisible until a real apply.

### Bottlerocket disk layout
- `/dev/xvda` — OS volume (~2GB, read-only). `disk_size` sizes this. Do not try to configure container image storage here.
- `/dev/xvdb` — data volume for container images and ephemeral storage. Must use `block_device_mappings` to size it.
- Worker and GPU node groups have explicit `block_device_mappings` for both volumes. Default node group intentionally omits it (uses Bottlerocket defaults).

### S3 bucket name is deterministic by design
`anyscale_bucket_name = "${var.eks_cluster_name}-${var.aws_region}-${data.aws_caller_identity.current.account_id}"` — globally unique, importable, deterministic across re-applies. Do not change to a random suffix or `account-regional` namespace.

### The shared `~/.terraformrc` can test stale code
dev_overrides in `~/.terraformrc` point at the binary at the main repo root. In a worktree session, that binary may not reflect your current changes. Always build first (`make build`) and for anything you intend to trust, use an isolated scratch CLI config pointing at a freshly-built binary. See the "Operational Notes" section of `HANDOFF.md`.

---

## Build and Test Quick Reference

```bash
# Build provider binary
make build

# Unit tests
make test

# Acceptance tests (requires ANYSCALE_CLI_TOKEN or ~/.anyscale/credentials.json)
make testacc

# Format and lint
make fmt
make lint

# Run the EKS example end-to-end (creates and destroys real AWS infra)
make test-aws-eks-basic

# Sweep leaked test resources
make sweep-dry-run   # inspect
make sweep           # delete

# Docs
make docs
```

Do **not** run `terraform init` inside an example directory if a provider binary isn't available at the path dev_overrides expects — it will error on the anyscale provider. Use an isolated scratch config (see `HANDOFF.md §Operational Notes`).

---

## Where Things Live

| What | Where |
|------|-------|
| Provider entrypoint | `main.go` |
| Resources and data sources | `internal/provider/resource_*.go`, `internal/provider/data_source_*.go` |
| Acceptance tests | `internal/acctest/` |
| EKS example | `examples/aws-eks-basic/` |
| EKS example README | `examples/aws-eks-basic/README.md` (new 2026-07-07) |
| Example test targets | `Makefile` (targets: `test-aws-eks-basic`, `test-aws-eks-basic-full`, `test-aws-eks-basic-gpu`) |
| CI validate gate | `.github/workflows/ci.yml` (credential-free `terraform validate` on all examples) |
| Anyscale API docs | https://console.anyscale.com/api/v2/docs |

---

## If You're Picking Up After a Crystl Quest

Each quest hero worked on a separate git worktree branch. The integration branch was built by the architect by merging those branches:

- `crystl/tfp-forge` (7 commits — all aws-eks-basic code changes)
- `crystl/tfp-scribe` (3 files — new README, gpu tfvars example, fixed examples/README.md)
- `crystl/tfp-assayer` (2 commits — CI gate + incidental multi-resource-cloud-basic fix)

The hero branches are still on origin and can be inspected. The integration branch `integration/eks-v21-bottlerocket` is the authoritative artifact.

Terraformer (IaC review), shipwright (release engineering), and architect (orchestration) made no code commits this session — their work was decision-making, validation rulings, and integration.
