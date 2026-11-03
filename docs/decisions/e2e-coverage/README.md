# Design: end-to-end test coverage across platforms

**Status: draft 2026-11-02. Target matrix and test specs; the gap column is filled from the
inventory of existing tests.** Input for running real-infrastructure tests in Buildkite.

## Platforms

| Key | Platform | Cloud shape | Real infra authorized |
|---|---|---|---|
| AWS-VM | AWS VM | `cloud_provider = AWS`, `compute_stack = VM` | yes (tear down within 24h) |
| GCP-VM | Google VM | `GCP`, `VM` | yes |
| EKS | AWS EKS | `AWS`, `K8S` | yes |
| GKE | Google GKE | `GCP`, `K8S` | yes |
| AKS | Azure AKS | `AZURE`, `K8S` (Azure is Kubernetes-only) | **no**: write, compile-check, do not run until approved |

## Two layers

Creating a cloud needs the platform's own infrastructure (VPC, IAM roles, buckets, a Kubernetes
cluster with the operator). Everything else only needs a working cloud. So:

- **Layer 1, cloud lifecycle (nightly, expensive).** One scenario per platform that creates the
  platform infrastructure and the Anyscale cloud with Terraform, applies, plans, imports and
  destroys. Today these are the `examples/*` scenario targets.
- **Layer 2, workloads (per merge or nightly).** `ANYSCALE_TEST_REAL_INFRA`-gated acceptance
  tests that run against one long-lived fixture cloud per platform, selected by
  `ANYSCALE_TEST_CLOUD_NAME`. The same test code runs on all five; the Buildkite step sets the
  cloud.

Layer 2 needs a static fixture cloud per platform. Only `tfp-test-aws-useast1-STATIC` (AWS-VM)
exists today. **Open decision:** engineering provisions GCP-VM, EKS, GKE and AKS fixtures, named
`tfp-test-<platform>-STATIC`, by name only (IDs are never committed).

## Prerequisite: platform-aware fixture resolution

Every real-infra workload test resolves its cloud through `pinnedVMCloud()`, which fails the test
on a non-VM cloud, so pointing `ANYSCALE_TEST_CLOUD_NAME` at a Kubernetes fixture fails every
test instead of running it. Before any per-platform test:

- `pinnedCloud(t)` resolves the fixture as today and returns its provider and compute stack.
- `skipUnlessPlatform(t, cloud, platforms...)` skips (never fails) with the platform named, for
  tests that do not apply.
- Per-platform inputs (instance types, regions, image URIs) come from one table keyed by
  platform, not literals in each test.
- Existing VM-only tests keep their behavior on AWS-VM; a non-VM fixture makes them skip with a
  reason, not fail.

## What every resource test checks

Unless a row says otherwise, a resource test is one `resource.Test` with:

1. Create, then `ExpectEmptyPlan` post-apply (no perpetual diff on this platform).
2. One in-place update of a mutable attribute, `ExpectResourceAction(Update)` pre-apply, empty
   plan after.
3. Import: an `ImportState` step with `ImportStateVerify` (and `ImportStateCheck` for any
   deliberate divergence). Cold import where import recovery is platform-specific.
4. Destroy, with `CheckDestroy` asserting the API object is gone (404 or archived).

Names must match `^TestAcc[A-Za-z]+Resource` or `^TestAcc[A-Za-z]+DataSource`, include
`RealInfra`, and use `UniqueName`. Every created object must be sweepable.

## Target matrix

P = runs on that platform. N = must fail cleanly (plan or apply error naming the limitation).
1 = platform-independent: run once, on AWS-VM. Blank = not applicable.

### Layer 1: cloud lifecycle

| Surface | AWS-VM | GCP-VM | EKS | GKE | AKS | Platform-specific checks |
|---|---|---|---|---|---|---|
| `anyscale_cloud` all-in-one | P | P | P | P | P | object_storage scheme (`s3://`, `gs://`, verbatim `abfss://`); import of a cloud whose config set only source inputs plans no-op (derived fields: `zones`, `mount_targets`, `mount_path`, MemoryDB/Memorystore endpoints) |
| `anyscale_cloud` empty + `anyscale_cloud_resource` | P | P | P | P | P | split pattern; second resource on one cloud; `file_storage` in-place update |
| `cloud_resource` MemoryDB | P | | | | | `memorydb_cluster_name` → ARN/endpoint derived, import no-op |
| `cloud_resource` Memorystore | | P | | | | `memorystore_instance_name` → endpoint derived |
| `cloud_resource` file storage | P (EFS) | P (Filestore) | P (PVC/CSI) | P (PVC/CSI) | P (PVC/CSI) | `mount_targets`/`mount_path` derived on VM |
| `anyscale_cloud` with `compute_stack = VM` on Azure | | | | | N | plan-time rejection |
| `data.anyscale_cloud` / `data.anyscale_clouds` | P | P | P | P | P | config blocks read back per platform |

### Layer 2: workloads against the fixture cloud

| Surface | AWS-VM | GCP-VM | EKS | GKE | AKS | Platform-specific checks |
|---|---|---|---|---|---|---|
| `anyscale_compute_config` | P | P | P | P | P | platform instance types; `advanced_instance_config` (top-level, head, worker) holds platform-specific JSON and must round-trip with no diff; worker groups; new version on update |
| `anyscale_container_image_build` | 1 | | | | | build reaches SUCCEEDED; rebuild on change |
| `anyscale_container_image_registry` | 1 | | | | | BYOD image, one build per create |
| `anyscale_service` golden path | P | P | P | P | P | deploy v1 RUNNING → change to v2 rolls out → `primary_version` moves; destroy terminates |
| `anyscale_service` canary_percent | P | P | P | P | P | hold 0 → 25 → null; failed hold rolls back; 100 cutover (exists on AWS-VM) |
| `anyscale_service` stopped / restart | P | | P | | | stop → TERMINATED, restart → RUNNING |
| ephemeral `anyscale_service_credentials` | P | | P | | | `auth_token` + `base_url` answer an authenticated request to the running service |
| `anyscale_cloud_iam_mapping` | P (IAM role) | P (service account) | P (K8s SA) | P (K8s SA) | P (K8s SA) | identity type per platform; needs `ANYSCALE_TEST_IAM_MAPPING_CLOUD_ID` per platform, never the STATIC fixture |
| `anyscale_system_cluster` | P | P | P* | P* | P* | *K8S is gated by `enable_task_dashboard_k8s` (501 when off): assert either RUNNING or the clean 501 diagnostic |
| `anyscale_runtime_env_package` | P | P | P | P | N | Azure unsupported: assert the documented error |
| `anyscale_project` | P | P | P | P | P | `parent_cloud_id` per platform; delete waits out the 403 race |
| `anyscale_cloud_access` | 1 | | | | | cloud-scoped but platform-independent API |
| data: `compute_config`, `service(s)`, `system_cluster`, `cloud_iam_mapping`, `project(s)`, `container_image(s)` | P | P | P | P | P | read back what the resource tests created |

### Organization-level (platform-independent, run once)

`organization_invitation`, `organization_user`, `organization_user_role`,
`organization_default_cloud`, `user_group`, `user_group_members`, `role_binding`,
`scheduler_config`, and data sources `user`, `organization`, `organization_user(s)`,
`user_group(s)`, `role`, `scheduler_config`. These need `ANYSCALE_TEST_USER_EMAIL`,
`ANYSCALE_TEST_ORG_NAME` and `ANYSCALE_TEST_ORG_MODE`; they belong in one Buildkite step, not the
per-platform matrix.

## Buildkite shape (recommendation)

- One step per platform per layer, each setting `ANYSCALE_TEST_CLOUD_NAME` (layer 2) or running
  the platform's scenario target (layer 1), plus one org-level step.
- `-run '^TestAcc[A-Za-z]+(Resource|DataSource)_RealInfra'`, so the same selector works for every
  platform. Tests that do not apply to a platform skip with a reason (via a platform helper that
  reads the resolved cloud's provider and compute stack), never fail.
- Concurrency: one platform step at a time per fixture cloud; services on one fixture cloud can
  run in parallel.
- Cleanup: an always-run `make sweep` step after the matrix; layer 1 relies on the scenario
  targets' EXIT traps.
- A step that skips every test must fail the build (a green step that ran nothing is the existing
  `aws-e2e` scaffold's failure mode).

## Open decisions

1. Static fixture clouds for GCP-VM, EKS, GKE, AKS (engineering).
2. AKS real-infra runs (user approval).
3. Dedicated IAM-mapping clouds per platform, or skip IAM mapping outside AWS-VM.
4. Whether `enable_task_dashboard_k8s` is on for the K8S fixtures.
5. A dedicated system-cluster cloud per platform (`ANYSCALE_TEST_SYSTEM_CLUSTER_CLOUD_ID`), or AWS-VM only.

## Gaps (target matrix vs the 2026-11-02 inventory)

The inventory (`inventory.md`, alongside this file) found: no test creates real platform
infrastructure; every workload test runs on the AWS-VM fixture; 23 real-API tests never run
because no workflow sets their gates; the scenario targets assert nothing and swallow destroy
failures; there is no Buildkite configuration in the repo.

Priority: P0 unblocks the rest, P1 is the largest coverage gain, P2 fills zero-coverage
surfaces, P3 waits on a decision.

| # | Gap | Priority | Spec |
|---|---|---|---|
| G0 | Harness cannot target a non-AWS-VM fixture | P0 | Done: `pinnedCloud`, `skipUnlessPlatform`, AZURE/GENERIC recognized. |
| G0b | Fixture resolution reads only page 1 of `/api/v2/clouds` | P0 | `pinnedCloud` resolves the fixture by `GET /api/v2/clouds/{id}`. Auto-discovery (`GetAllConfiguredClouds`) also pages through every cloud. Unit test with the fixture on page 2. |
| G1 | Scenario targets assert nothing, swallow destroy failures | P0 | One script for every `test-*` target: `apply`; `plan -detailed-exitcode` exits 0; `state rm`, then `import` the `anyscale_cloud` and each `anyscale_cloud_resource`, then `plan -detailed-exitcode` exits 0 (derived-field import round-trip on real infra); `destroy` with its exit status propagated; the cloud GET returns 404. Variables from `TF_VAR_*`, no tfvars file. |
| G2 | No AKS runner | P3 | `test-azure-aks-basic` on the G1 script; written and validated, not run until AKS is approved. |
| G3 | EKS/GKE examples stop before the operator | P2 | Install the operator (phase 2) so the scenario ends with a cloud that runs workloads and can serve as a layer-2 fixture. With engineering. |
| G4 | No EKS split-pattern scenario; other examples unrun (`aws-vm-basic-resource`, `kitchen-sink`, `container-image-compute-config`) | P2 | EKS empty cloud + `anyscale_cloud_resource`; `test-*` targets for the unrun examples on the G1 script. |
| G5 | Workload tests run only on AWS-VM | P1 | One platform inputs table (instance types, K8S via `ResolveK8sInstanceType`; region; base image; `advanced_instance_config` sample). Move onto `pinnedCloud` + the table: service golden path, stop/restart, the three canary e2e tests, `RuntimeEnvPackageResource_RealInfra(Service)` (AKS asserts the documented unsupported error), `CloudDataSource_ByName`, `ProjectResource_Basic`. No behavior change on AWS-VM. |
| G6 | `anyscale_service_credentials` has no real test | P2 | Running service; open the ephemeral resource through `echoprovider`; `auth_token` and `base_url` set; a request to `base_url` with the token is not 401/403, and without it is. AWS-VM and EKS. |
| G7 | `anyscale_system_cluster` has only a mock test | P2 | Runs only on a dedicated cloud named by `ANYSCALE_TEST_SYSTEM_CLUSTER_CLOUD_ID` (skips when unset, refuses the static fixture): create enables and starts it and Delete is state-only, so it would permanently change a shared fixture. Enable+start, describe reports running, plan empty, import, destroy; after destroy the cluster is still enabled (Delete is state-only by contract). `t.Cleanup` calls `POST /api/v2/system_workload/{cloud_id}/terminate` (what `anyscale cloud terminate-system-cluster` uses) so the cluster does not keep running. On K8S accept running or the clean 501 diagnostic (`enable_task_dashboard_k8s` off), never another error. |
| G8 | Data sources not read back on real infra | P2 | `service`, `services`, `system_cluster`, `cloud_iam_mapping` read back what G5-G7 created, per platform. |
| G9 | `compute_config` only on AWS-VM | P1 | `_Basic`/`_Update`/`_WithWorkers` per platform; `advanced_instance_config` round-trips with no diff; import no-op. |
| G10 | Org resources with no real test: `organization_invitation`, `organization_user`, `organization_default_cloud`, `scheduler_config` | P2 | Invitation: create to a plus-alias, import, destroy revokes. The other three change org-wide state: snapshot before, restore in `t.Cleanup`, and gate on a new `ANYSCALE_TEST_ORG_MUTATION=1` so they never run in a shared org by accident. |
| G11 | 23 real-API tests never run in CI (engineering) | P1 | Buildkite steps set `ANYSCALE_TEST_REAL_INFRA`, `ANYSCALE_TEST_USER_EMAIL`, `ANYSCALE_TEST_ORG_NAME`, `ANYSCALE_TEST_ORG_MODE` and the per-platform `ANYSCALE_TEST_CLOUD_NAME`. |
| G12 | `cloud_iam_mapping` real tests on one dedicated AWS cloud only | P3 | Same tests per platform once dedicated IAM-mapping clouds exist (open decision 3). |

Test rules for every new or moved test: names match the CI regexes and include `RealInfra`;
`UniqueName`; sweepable; `CheckDestroy` against the API; mutation-proof each new assertion
(break the provider behavior, see the test fail, revert); the PR says what was run on real
infrastructure and what was only compile-checked.
