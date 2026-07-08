# Handoff — terraform-provider-anyscale

**Last updated**: 2026-07-07  
**Integration branch**: `integration/eks-v21-bottlerocket` (base f5325a5, HEAD 420eef8)

---

## Current Status

The EKS v21 + Bottlerocket migration is **complete and fully validated on real AWS**. The integration branch is built, verified, and ready for a pull request. No work is in flight.

- `integration/eks-v21-bottlerocket` pushed to origin — three hero branches merged, zero conflicts
- Every commit has real-AWS evidence (112-resource full E2E + targeted S3 apply/destroy)
- Release classification: **below-patch** — zero provider Go/schema changes; examples and CI only
- PR not yet opened (shipwright opens next session)

---

## Repository Health

| Check | Status |
|-------|--------|
| `go build` | Green on integration branch |
| `go vet` | Green |
| `terraform fmt` | Clean |
| `terraform validate` (examples) | Clean via new CI gate |
| Unit tests | Unchanged from origin/main (zero Go changes) |
| Real-AWS E2E | 112/0/0 apply, 112/112 destroy, 0 leaks |
| Provider version | v0.1.2 (unchanged; this is below-patch) |

---

## Decisions

### vpc-cni `before_compute=true` (CRITICAL)
v21 hardcodes `bootstrap_self_managed_addons=false` — EKS installs no default CNI. Without explicit `vpc-cni = { before_compute = true }` in the `addons` block, nodes join the cluster and stay permanently `NotReady`. `terraform plan` passes; only a real apply reveals this. **Do not remove this from the addons block.**

### Bottlerocket two-volume disk model
`disk_size` on Bottlerocket configures `/dev/xvda` (~2GB read-only OS volume). The data volume for container images is `/dev/xvdb` and cannot be sized via `disk_size`. Worker and GPU node groups use explicit `block_device_mappings` with both volumes. The default node group intentionally omits `block_device_mappings` (uses Bottlerocket defaults: xvda=2GB, xvdb=20GB).

### S3 bucket uniqueness by design
`anyscale_bucket_name = "${var.eks_cluster_name}-${var.aws_region}-${data.aws_caller_identity.current.account_id}"` with `bucket_namespace = "global"`. Deterministic across re-applies, globally unique, importable.  
- Rejected: `bucket_namespace=account-regional` — requires `<account_id>-<region>-an` suffix format, the exact validation that failed at apply time in the pre-fix code.
- Rejected: random prefix (`anyscale_bucket_prefix`) — non-deterministic; re-apply after state loss generates a new name, orphaning the old bucket silently.

### Bottlerocket image_uri separation
`image_uri` in Anyscale is a standard OCI container image run as a pod via containerd inside Kubernetes. Bottlerocket is only the **node host OS**. No caveat or compatibility note needed — these are separate layers. Per user clarification during the session.

### Consolidation onto one branch
The real E2E ran against a combination that initially spanned two branches (forge + assayer). Architect ruled: consolidate onto `crystl/tfp-forge` so the shipped artifact equals the tested artifact. All seven aws-eks-basic commits are on that branch alone.

---

## Current Risks

### Low — PR not yet opened
The integration branch is built and verified. Shipwright will open the PR next session. No code risk; just process.

### Low — incidental multi-resource-cloud-basic fix (assayer 989d115)
An unrelated fix from assayer's branch was merged into the integration branch. Scope is contained but it has not been formally reviewed in isolation. Worth a separate look before merging to main.

### Negligible — CI gate merge order
The CI gate (`ci.yml`, assayer 46c9507) runs `terraform validate` on all examples. It must pass against the v21 code. Confirmed passing today. Safe to merge in either order relative to forge's commits since the files are disjoint.

---

## Next Work

### Immediate (next session)
1. Open pull request from `integration/eks-v21-bottlerocket` to `main` (shipwright)
2. Add PR description referencing: v21 migration, Bottlerocket switch, vpc-cni requirement, S3 uniqueness fix, new CI gate, new README
3. Review and label the incidental multi-resource-cloud-basic fix (assayer 989d115) — confirm it belongs in this PR or split it out

### Deferred
- **Tag-based EKS sweeper**: `make test-aws-eks-basic` lacks a sweeper. Destroy-on-exit trap covers normal runs, but a crash can orphan a cluster. Natural follow-up: add a sweeper matching the `workload=tf-provider-e2e-test` tag. See `internal/acctest/sweeper_project_test.go` for the pattern.
- **EKS v1.36 support matrix**: confirm EKS v1.36 is in the provider's tested version matrix and update any docs that list supported versions.

---

## Per-Hero Next Actions

| Hero | Next action |
|------|-------------|
| **tfp-shipwright** | Open PR: `integration/eks-v21-bottlerocket` → `main`. No changelog fragment (below-patch). |
| **tfp-architect** | Review incidental 989d115 (multi-resource-cloud-basic); decide if it splits from this PR. |
| **tfp-forge** | None — deliverable complete. |
| **tfp-assayer** | None — E2E done. Available for targeted re-validation if PR review surfaces questions. |
| **tfp-scribe** | None — docs complete. PR description is shipwright's job. |
| **terraformer** | None — IaC review complete. |

---

## Operational Notes for Cold-Start Agents

These were discovered during this session and cost real debugging time. Read before touching this repo.

### `terraform init` is safe per example directory
`terraform init` fetches third-party modules and the AWS provider. It is safe and necessary. dev_overrides only skips installing the **anyscale provider** (prints a warning, nothing more). What you must **not** do: trust the shared `~/.terraformrc` for real validation runs — it points at whatever binary sits at the main repo root, which may be stale or from a different worktree. Use an isolated scratch CLI config (a temporary `.terraformrc` pointing at a freshly-built binary) for any apply or validate you intend to trust. That is what backed every real result this session.

### Worktree file isolation
Files written inside a Crystl worktree are visible at their path only from within that worktree. From outside (e.g., a teammate's shard), reading the same path returns "file does not exist." After the worktree is merged, the files land at the correct path in the integration branch. During a live session, share content via quest messages rather than file paths.

### `git diff` with a pathspec can silently return empty
`git diff A B -- path` and `git show commit -- path` can return zero bytes even when real changes exist at that path. Confirmed reproducible and environment-level. If a scoped diff comes back empty, do not conclude the commit is empty — use an unscoped diff (`git diff A B`) and grep for the filename, or read the file directly off disk.

### plan-cannot-catch-this pattern
Three separate bugs in this session were undetectable by `terraform plan` or `terraform validate` and appeared only at real apply time:
- vpc-cni not installed (cluster applies cleanly, nodes hang `NotReady` forever)
- S3 bucket name format rejected by aws-anyscale-s3 module at create time
- Makefile `eks_cluster_name` collision across concurrent test runs

When changing EKS or networking configuration, always run a real apply, not just a plan. The new CI gate catches `validate`-class errors, but it cannot catch apply-time API validation.
