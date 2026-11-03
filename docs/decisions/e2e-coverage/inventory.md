# Inventory: tests that run against the real Anyscale API (2026-11-02, base 76fe790)

All `TestAcc*` tests live in `internal/acctest/`. Each of the 529 functions was classified by its
body, including tests that only call a helper:

- **Mock (398):** the provider points at an httptest server.
- **Offline (19):** no Anyscale API at all. These are `terraform validate` checks, in-process
  fake providers, a plan against an unreachable URL, and a forced-failure rerun control.
- **Real API (112):** listed below.

## Key findings

1. **No test creates real AWS VM, GCP VM, EKS, GKE or AKS infrastructure.**
   - Every real-workload test deploys onto the static AWS VM fixture
     `tfp-test-aws-useast1-STATIC`.
   - Cloud tests register an `anyscale_cloud` with placeholder identities and no cloud
     resource.
2. **No workflow sets `ANYSCALE_TEST_REAL_INFRA`, `ANYSCALE_TEST_USER_EMAIL`,
   `ANYSCALE_TEST_ORG_NAME`, `ANYSCALE_TEST_IAM_MAPPING_CLOUD_ID` or
   `ANYSCALE_TEST_CLOUD_ID`/`_NAME`.**
   - That leaves 16 REAL_INFRA-gated and 7 EMAIL-gated tests that never run in CI.
   - `.github/scripts/summarize-acctest-skips.sh` warns about this in the step summary.
3. **The example scenario targets (`make test-*`) only apply and destroy.**
   - They make no plan-empty check, no import and no assertion.
   - Destroy failures are swallowed (`|| true` in the EXIT trap), so the target's exit status
     reflects apply only.
   - Required variables come from a local `terraform.tfvars`.
   - No workflow calls them.
4. **`test-aws-eks-basic` stops at phase 1.**
   - `install_gateway_resources` defaults to false, so the Gateway objects and the Anyscale
     operator are never installed.
   - It needs a manual `helm pull` first.
   - `test-gcp-gke-basic` installs no operator either.
5. **Examples with no runner:**
   - `azure-aks-basic` (validated and planned only; no Azure subscription).
   - `kitchen-sink`.
   - `aws-vm-basic-resource`.
   - `container-image-compute-config`.
6. **There is no Buildkite configuration in the repo.**
   - The only references are the `SkipIfNoRealInfra` doc comment and skip message, which point
     at a "buildkite e2e" lane defined outside the repo.
   - The `buildkite/terraform-provider-anyscale-aws-e2e` PR check reports "passed and blocked"
     with no jobs executed.
7. **The test harness cannot target a non-AWS-VM fixture yet.**
   - `pinnedVMCloud` failed on Kubernetes clouds, and `isKnownProvider` dropped Azure. Both are
     fixed in G0.
   - `GetAllConfiguredClouds` read only page 1 of `/api/v2/clouds`. Fixed in G0b: it now pages,
     and `pinnedCloud` resolves the fixture by ID.
8. **CI regexes:** every `TestAcc*` name matches `^TestAcc[A-Za-z]+Resource` or
   `^TestAcc[A-Za-z]+DataSource` except `TestAccWakeGate`. Gates, not names, decide what runs.

## Gates

Every row needs `TF_ACC=1` plus a token.

| Gate | What it requires |
|---|---|
| none | Only `TF_ACC` and a token; runs in CI's credentialed lane. |
| REAL_INFRA | `ANYSCALE_TEST_REAL_INFRA=1` (`SkipIfNoRealInfra`). |
| EMAIL | `ANYSCALE_TEST_USER_EMAIL`, plus `ANYSCALE_TEST_ORG_NAME`. |
| IAMMAP | `ANYSCALE_TEST_IAM_MAPPING_CLOUD_ID`, plus REAL_INFRA. |
| legacy / converted | `ANYSCALE_TEST_ORG_MODE`. |
| RB-enabled | Skips when `GET /api/v2/roles/` returns 404. |

Abbreviations used in the tables:

- **CI-lanes:** the `ci.yml` acctest shards, `scheduled-acctest.yml` (daily) and
  `acctest-fork-pr.yml` (manual).
- **Static AWS VM:** the pinned fixture.
- **Empty-cloud reg:** an `anyscale_cloud` record with a fake role and no cloud resource.
- **Org:** no cloud involved.

## Real-API acceptance tests

| Test | Under test | Platform | Gate | Lifecycle ops | Runs in CI |
|---|---|---|---|---|---|
| WakeGate | cloud wake probe | Static AWS VM | `ANYSCALE_TEST_WAKE_GATE` | API probe | rerun path only |
| CloudDataSource_ByID / _ByName | ds cloud | Static AWS VM | none | ds read | yes |
| CloudDataSource_WithComputeConfig | ds cloud + compute_config | Static AWS VM | none | create, ds read, destroy | yes |
| CloudDataSource_MatchesResourceState | cloud + ds cloud | Empty-cloud reg | none | create, update, ds read, destroy | yes |
| CloudDataSource_C2ParityMatchesPluralDataSource | cloud + ds cloud/clouds | Empty-cloud reg | none | create, ds read, destroy | yes |
| CloudsDataSource_* (8) | ds clouds | Static AWS VM (one: first cloud in list) | none | ds read | yes |
| ComputeConfigDataSource_Basic / _WithVersions / _EnableCrossZoneScaling | compute_config + ds | Static AWS VM | none | create, (update), ds read, destroy | yes |
| ContainerImageDataSource, ContainerImagesDataSource_Basic / _WithBuild | build + ds | Org | none | create, ds read, destroy | yes |
| OrganizationDataSource_* (3) | ds organization | Org | none | ds read | yes |
| OrganizationUserDataSource_* (5), OrganizationUsersDataSource_* (5) | ds org user(s) | Org | none | ds read | yes |
| ProjectDataSource_* (4), ProjectsDataSource_* (6) | project + ds | Static AWS VM | none | create, ds read, destroy | yes |
| UserDataSource_* (5) | ds user | Org / Static AWS VM | none | ds read | yes |
| UserGroupDataSource_basic, UserGroupsDataSource_basic | user_group + ds | Org | none | create, ds read, destroy | yes |
| CloudResource_AWS_EmptyCloud | cloud | Empty-cloud reg | none | create, re-plan, destroy | yes |
| CloudResource_GCP_K8S | cloud (K8S) | GKE placeholder, no cluster | REAL_INFRA | create, import, destroy | no |
| CloudResource_Disappears | cloud | Empty-cloud reg | none | create, drift, destroy | yes |
| CloudAccessResource_Live* (4) | cloud_access | Empty-cloud reg | EMAIL + legacy | create / import / update / drift | no |
| CloudIAMMappingResource_RealCloud_ColdImport_BareCloudID / _Lifecycle | cloud_iam_mapping | dedicated cloud | REAL_INFRA + IAMMAP | import; create, update, destroy | no |
| CloudResourceResource_AzureVM_NotSupported / _Generic_NotSupported | cloud_resource | plan-time reject | none | ExpectError | yes |
| CloudResource validation (10: AzureVM, PVC/CSI, Redis/MemoryDB, compute_stack, mount_path, subnets on K8S) | cloud | plan-time reject | none | ExpectError | yes |
| ComputeConfigResource_Basic | compute_config | each VM provider found (AWS only in CI logs) | none | create, import, destroy | yes |
| ComputeConfigResource_WithWorkers / _Update / _RenameForcesReplace / _InconsistentResultRegressions / _Disappears | compute_config | Static AWS VM | none | create, update, replace, import, drift, destroy | yes |
| ComputeConfigResource_ImportRecoversWriteOnlyFields_RealAPI / _ImportRecoversMaskedFieldsWhenSet / _ImportNonLatestVersionRetainsOwnValues / _ImportPreExistingOutOfBandFixture | compute_config | Static AWS VM | none | cold import, plan | yes |
| ComputeConfigResource_K8S | compute_config | first K8S cloud in org | none | create, destroy | skips: no K8S cloud |
| ContainerImageBuildResource_Basic / _Update / _WithProjectID | container_image_build | Org | none | create, update, import, destroy | yes |
| ContainerImageRegistryResource_Basic / _BYOD / _ImageURIUpdate_RealAPI / _RayVersionUpdate_RealAPI | container_image_registry | Org | none | create, update, import, destroy | yes |
| OrganizationUserRoleResourceR5LegacyPutPreservesDenyRoles / ...ChangingDenyRoleWritePersists | organization_user_role | Org | EMAIL + legacy | create, update, destroy | no |
| ProjectResource_Basic / _WithDescription / _Disappears | project | Static AWS VM | none | create, replace, import, drift, destroy | yes |
| RoleBindingResource_RealAPI | role_binding + user_group | Static AWS VM | converted + RB-enabled | create, import, drift, destroy | skips (org mode legacy) |
| RuntimeEnvPackageResource_RealInfra / _RealInfraService | runtime_env_package (+ service) | Static AWS VM | REAL_INFRA | create, update, destroy | no |
| ServiceResource_RealInfra / _InPlaceRollout | service + compute_config | Static AWS VM | REAL_INFRA | create, rollout, IN_PLACE, destroy | no |
| ServiceResource_RealInfra_RolledBackRollout | service | Static AWS VM | REAL_INFRA | failed rollout, drift, restart, destroy | no |
| ServiceResource_RealInfra_StopAndRestart | service | Static AWS VM | REAL_INFRA | stop, restart, destroy | no |
| ServiceResource_RealInfra_InputsParity / _ImageURIRegistry | service / builds probe | Static AWS VM / Org | REAL_INFRA | rollout, rollback, probes | no |
| ServiceResource_RealInfra_CanaryPercent{Probe,ProbeCompletion} | service (design probes) | Static AWS VM | REAL_INFRA | out-of-band probes | no |
| ServiceResource_RealInfra_CanaryPercent{HoldShiftComplete,FailedHoldRollsBack,Cutover} | service | Static AWS VM | REAL_INFRA | hold, shift, rollback, cutover | no (passed by hand 2026-11-02) |
| UserGroupResource_basic / _disappears / _DuplicateNameError | user_group | Org | none | create, update, import, drift | yes |
| UserGroupMembersResource_RealAPI | user_group_members | Org | EMAIL | create, update, import, destroy | no |

## Example scenario targets (Makefile)

All of them: apply, then destroy in an EXIT trap with the failure swallowed. Nothing is asserted.

| Target | Example | Platform | Creates |
|---|---|---|---|
| test-aws-vm-basic | aws-vm-basic | AWS VM | `cloud` all-in-one, 3 `compute_config`, `project`, ds project(s); infra via cloudfoundation module |
| test-aws-vm-full | aws-vm | AWS VM | `cloud` + `cloud_resource` (split), EFS, MemoryDB |
| test-aws-eks-basic | aws-eks-basic | EKS | `cloud` K8S; EKS, VPC, S3, EFS, IAM, pod identity, Envoy Gateway. No operator (phase 1 only). |
| test-gcp-vm-basic | gcp-vm-basic | GCP VM | `cloud` VM, 3 `compute_config` |
| test-gcp-vm-full | gcp-vm | GCP VM | `cloud` + `cloud_resource`, Filestore, Memorystore |
| test-gcp-gke-basic | gcp-gke-basic | GKE | `cloud` + `cloud_resource` K8S; GKE, GCS, Filestore, workload identity. No operator. |
| test-primary(-vm/-k8s/-aws/-gcp) | subsets of the above | | |

## Counts by platform (real API)

| Platform | Tests |
|---|---|
| Real infrastructure created by the test | 0 |
| Static AWS VM fixture | 52 |
| AWS empty-cloud registration | 8 |
| GCP VM | 0 dedicated (ComputeConfigResource_Basic, if a GCP VM cloud exists) |
| EKS | 0 dedicated |
| GKE | 1 (placeholder registration) |
| Any K8S cloud | 1 (skips in CI) |
| AKS | 0 |
| Dedicated IAM-mapping cloud | 2 |
| Org-level | 34 |
| Plan-time rejection | 12 |

CI status:

- 87 run in the CI lanes.
- 16 never run (REAL_INFRA).
- 7 never run (EMAIL).
- 1 runs only on the rerun path.
- Role binding skips in legacy org mode.
