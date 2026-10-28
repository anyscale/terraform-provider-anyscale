# Design: deploying a service from a local directory

**Status: implemented; Gate 2 confirmed; Gate 1 confirmed on the static AWS VM cloud (G1-a, b, c,
e, f), not run for G1-d, g, h.** Designed at `a0205ae`. Source traces are against the product
monorepo (CLI `frontend/cli/anyscale`, backend `backend/server`) and Ray 2.59.0
`ray/_private/runtime_env/packaging.py`.

## Decision

A separate resource, **`anyscale_runtime_env_package`** (option b, below), packages a local
directory, uploads it to the cloud's data bucket under a content-addressed name, and outputs
`uri`. Users put `uri` in `ray_serve_config` themselves. `anyscale_service` gets no
`working_dir`/`excludes` that the provider injects.

- The deployed URI is in the user's own config, so state, the request and the live version agree.
  Injection would be a source→derived rewrite inside a Required attribute, the bug class this repo
  has spent the most effort closing.
- The hash is computed in `ModifyPlan`, so a code change is a plan diff on the package and on every
  service referencing it.
- It composes: one package can feed several applications, services, and later jobs.

**User decisions:** the name is `anyscale_runtime_env_package` (mirrors the backend `file_type` and
Ray's term). `.gitignore` matching is approved, with a new direct dependency on
`github.com/go-git/go-git/v5/plumbing/format/gitignore` (Apache-2.0, actively maintained, already
in the module graph through terraform-plugin-testing, and its patterns take a directory domain,
which per-directory `.gitignore` scoping needs).

## Corrections to the original brief

1. **The CLI names packages `pkg_{md5(zip bytes)}.zip`** (`utils/runtime_env.py:292`,
   `common.py:116`), not via Ray's `get_uri_for_directory` (`packaging.py:580`, `gcs://` only).
2. **That md5 is not a content hash:** the zip stores mtimes and `Path.iterdir()` order
   (`ray_utils.py:56-76`, `:106-139`), so a fresh clone gets a new name, which in Terraform means a
   redeploy per CI run. The provider does not copy it, and nothing requires it to (§2).
3. **`.gitignore` is honored**, per directory (`ray_utils.py:34-76`), unless
   `ANYSCALE_DISABLE_GITIGNORE_EXCLUSION=1`.
4. **`user_provided_working_dir` is inert for services**; its only reader is the jobs resolver
   (`ha_jobs_resolver.py:931`). Not emitted.
5. **No public API deletes a package.** The only `delete_object` caller is workspace cleanup
   (`workspaces_service.py:1836`); `CloudDataBucketAccessMode` is `READ`/`WRITE` only
   (`models/clouds.py:65`).

Confirmed as briefed: default excludes `.git,.venv,venv,__pycache__` (`workload_sdk.py:29`,
prepended to user excludes as one spec at `:210`), skip-if-exists (`anyscale_client.py:1709`),
the HTTP-PUT/smart_open split (`:1715-1743`), and `relative_working_dir` for multi-resource compute
configs (`workload_sdk.py:304-330`).

## 1. The presigned-URL endpoint

`POST /api/v2/clouds/{cloud_id}/generate_cloud_data_bucket_presigned_url` → **201**, `result`
envelope (`clouds_router.py:598-627`).

- **Request** (`clouds.py:93-131`): `file_type="runtime_env_packages"`, `file_name="pkg_<hash>.zip"`
  (charset `[A-Za-z0-9!-_.*'()/]`), `access_mode` `WRITE` (Create/Update) or `READ` (Read),
  optional `cloud_resource_id` (else the primary resource).
- **Response** (`clouds.py:184-215`): `file_uri`, `url`, `file_exists`, `bucket_name`,
  `bucket_path`, `url_scheme` (`http`/`smart_open`), `file_size`.
- **Key:** `{org_id}/{cloud_id}/runtime_env_packages/{file_name}`
  (`cloud_data_bucket_service.py:302-324`); `file_uri` is `{scheme}://{bucket}/{key}`. URL TTL 30
  min.
- **`file_exists` is a live probe** (S3 `head_object`, GCS `blob.exists`, K8s operator
  `storage_object.exists`), **except smart_open, hardcoded `false`** (`:514`).
- **Auth:** `_CLOUD_STORAGE_CHECK` (`clouds_router.py:517-546`) needs storage `UPDATE` for `WRITE`
  and `GET` for `READ`. A read-only role can plan but not apply a package change.
- **Cloud scoping:** a `cloud_resource_id` not in `cloud_id` gets 404, the same answer as a missing
  one (`_get_cloud_resource_record`, `:249-286`). Anyscale does not stop a service on cloud Y from
  naming cloud X's URI; bucket IAM decides at runtime. Documented on `cloud_id`.

**Supported storage** is an allowlist, `url_scheme=http` with an `s3://` or `gs://` `file_uri`:
AWS/GCP VM (AWS verified, Gate 1) and K8s with an operator-minted URL (`:522-532`, unverified,
G1-g). Rejected at apply: Azure (needs `x-ms-blob-type` headers, untestable under current
authorization), GENERIC `smart_open` (the CLI writes with the caller's ambient AWS credentials),
and any other scheme.

## 2. Content hashing and the zip

**Nothing requires matching the CLI.** The backend checks only `file_name`'s charset (a grep of
`backend/server` for the `pkg_` format finds nothing outside tests; control: it finds the CLI's
definition), and Ray needs only the `.zip` extension (`packaging.py:864`, `:943-948`). **Ray does
need the URI to be immutable:** it caches unpacked packages per node by URI
(`packaging.py:880-882`). A content-addressed name is write-once, so skip-if-exists is correct.

**Algorithm.** `sha256` over a version prefix, then each included entry in sorted order: kind
(file / empty dir), exec bit, length-prefixed slash path, and `sha256(file bytes)`. Excluded:
mtimes, ownership, absolute path, the source directory's name. Object name
`pkg_<64-hex>.zip`, which can never collide with the CLI's 32-hex names.

**Zip.** Deterministic: sorted entries, fixed 1980-01-01 mtime, unix mode in `external_attr` (Ray
`chmod`s from it, `packaging.py:1165-1172`, so the exec bit is semantic), and one constant
top-level directory `pkg/` (Ray strips one top-level directory from remote zips, `:947`; the CLI
uses the basename, `utils/runtime_env.py:289`). Entries are **stored, not deflated**, as the CLI's
`ZipFile` default does; this keeps the golden-bytes test independent of Go's `compress/flate`.
Create hashes the same bytes it writes into the zip, so the upload always matches its name.

**Traversal** matches the CLI: default + user excludes as one gitignore spec; each directory's
`.gitignore` scoped to its subtree; a path is excluded if *any* spec matches (so a `.gitignore`
negation cannot re-include a user exclude); excluded directories pruned; directories empty on disk
kept; symlinks followed, with cycle detection the CLI lacks; broken symlinks and special files
skipped. `RAY_OVERRIDE_RUNTIME_ENV_DEFAULT_EXCLUDES` and `ANYSCALE_DISABLE_GITIGNORE_EXCLUSION` are
**not** read. Exact `pathspec` parity on edge cases is not required. A tree with nothing left after
excludes is a plan-time error (the CLI would upload an unusable zip).

## 3. Surface

**Why ModifyPlan.** `archive_file` (`resource_archive_file.go`) rebuilds in Read; for an uploader
that changes state on refresh while the resource's own plan stays empty, so the upload never runs.
Instead `ModifyPlan` hashes the tree (no network calls); on a changed hash it sets `content_hash`
and marks `uri`, `relative_path` and `id` unknown, planning an in-place **Update** (not replace:
Delete is a no-op, so `-/+` would only alarm). An unchanged hash keeps the outputs from state.

### (a) Inject `working_dir` into `anyscale_service`: rejected

It needs a hidden Computed hash on the service; the live config permanently differs from state by
the injected URIs, so content-based out-of-band detection would redeploy on every apply; import
must un-inject URIs recognized by format (a `flatten*` recovery into a Required attribute); and
Update needs a hidden compute-config lookup to pick `working_dir` or `relative_working_dir`
(`service_sdk.py:146-168`).

### (b) A separate resource: chosen

| attribute | kind | notes |
|---|---|---|
| `source_dir` | Required | not part of the hash |
| `excludes` | Optional list | added to defaults and `.gitignore` |
| `cloud_id` | Required, RequiresReplace | |
| `cloud_resource_ids` | Optional list, RequiresReplace | default primary; same bytes to each (`anyscale_client.py:1796-1846`) |
| `content_hash` | Computed | set in ModifyPlan |
| `uri` | Computed | first target's `file_uri` |
| `relative_path` | Computed | `bucket_path`, asserted identical across targets |
| `id` | Computed | `relative_path` |

- **Read** makes a `READ` call per target and removes the resource when `url_scheme=http` and
  `file_exists=false`, or on 404 (cloud or resource gone). It never trusts smart_open's
  `file_exists`. A re-upload keeps the same `uri`; the service plans an update with a value known
  after apply, but deploys nothing because the resolved config is unchanged (G2-b, step 3).
- **Delete** removes from state only. Forced (no API), and correct: objects are content-addressed
  and shared, and during a `ROLLOUT` the old version keeps starting replicas from the old URI.
  Cleanup is a bucket lifecycle rule, owned by the bucket owner.
- **Import:** not implemented; there is no remote identity. Create is idempotent via `file_exists`.
- **Service import:** a CLI-deployed service has a `pkg_<md5>` URI, so the first apply against a
  package shows one `working_dir` diff and redeploys identical code once.
- **`py_modules`:** more packages; no extra surface.

### (c) Alternatives rejected

`archive_file` + `aws_s3_object` needs write access to the Anyscale bucket (impossible on hosted
clouds); it is documented in the guide. A data source runs on every plan and must be side-effect
free; an ephemeral resource's `Open` runs during plan and cannot reach state; a provider function
must be pure; an Action has no diff to reference.

## 4. Size and performance

The CLI has no size limit and warns per file at 100 MiB (`ray_utils.py:24`); presigned S3 PUTs cap
at 5 GiB. Every plan reads every included byte. The provider streams hashing, writes the zip to a
temp file, and PUTs it with an explicit `Content-Length` (S3 rejects chunked presigned PUTs), no
`Content-Type` and no `Authorization`. It warns at 100 MiB per file and 500 MiB total, with no hard
cap and no mtime cache (which would reintroduce cross-machine non-determinism). Errors never carry
the presigned query string, or more of a storage error body than `Code`/`Message`.

## 5. Verification gates

### Gate 1: API response shape

Run by `TestAccRuntimeEnvPackageResource_RealInfra` and
`TestAccRuntimeEnvPackageResource_RealInfraService` (`resource_runtime_env_package_realinfra_acc_test.go`,
`ANYSCALE_TEST_REAL_INFRA=1`) through the provider's own client on `tfp-test-aws-useast1-STATIC`.

- [x] **G1-a READ, missing object:** 201; `file_exists=false`; `url_scheme="http"`; `bucket_path`
  = `{org}/{cloud}/runtime_env_packages/<name>`; `file_uri` = `s3://{bucket_name}/{bucket_path}`;
  `url` is path-style `https://s3.<region>.amazonaws.com/...` with query params `X-Amz-Algorithm`,
  `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Expires`, `X-Amz-Security-Token`, `X-Amz-Signature`,
  `X-Amz-SignedHeaders`.
- [x] **G1-b WRITE, PUT, re-check:** the provider's PUT (no `Content-Type`) succeeded; a `READ`
  then returned `file_exists=true` and `file_size=610`, equal to the downloaded byte count; the
  downloaded zip held `pkg/.gitignore` and `pkg/app.py` and not the gitignored `.env`. A re-apply
  planned nothing.
- [x] **G1-c scoping:** a WRITE naming a nonexistent `cloud_resource_id` returned 404. The org has
  no second cloud, so the foreign-resource branch was not exercised directly; by source it returns
  the same 404.
- [ ] **G1-d permission split:** skipped; no storage-`GET`-only token exists.
- [x] **G1-e deploy:** a `ROLLOUT` service deployed `working_dir = <package uri>` and reached
  RUNNING on `app_v1:app`. A content change uploaded a new object, planned Update on both the
  package and the service, and rolled out a new primary version running `app_v2:app` from the new
  URI.
- [x] **G1-f `relative_working_dir`, single resource:** `relative_working_dir = <relative_path>`
  rolled out a new primary version running `app_v3:app`. The backend resolves it on a
  single-resource cloud, so the guide can name either output; it recommends `uri` for
  single-resource compute configs because it is self-describing.
- [ ] **G1-g K8s:** not run. The org has no K8s cloud, and creating EKS/GKE for it was out of scope
  for this change. K8s is allowed by the scheme check but unverified.
- [ ] **G1-h hosted:** the org has no Anyscale-hosted cloud. Hosted clouds stay undocumented.

How G1-e/f are checked. Each step's package holds a module that exists only in that upload
(`import_path = "app_vN:app"`) and refuses to import if the gitignored `.env` is present. RUNNING
alone proves nothing after step 1, because a failed rollout rolls back and still reports RUNNING.
Each step therefore asserts through the API that `primary_version.ray_serve_config` names that
step's module and package, that `primary_version.id` changed, and that `canary_version` is null.
`TestCheckDeployedVersionCatchesRollback` proves offline that the check rejects rolled-back shapes
(mutation: dropping the id comparison fails it). The test uses `ROLLOUT`: an `IN_PLACE` upgrade
rewrites the existing version (`services_dao.py:1129`), so its id never changes. No HTTP check: the
static cloud's ALB resolves, but TCP to it timed out from the machine that ran this, so HTTP-level
checks belong to a network that can reach it.

### Gate 2: framework/Core contract

Mock-server `resource.Test` runs with the real `terraform` binary
(`resource_runtime_env_package_mock_acc_test.go`):

- [x] **G2-a** `TestAccRuntimeEnvPackageResource_contentChangePlansUpdate`: a file edit with
  unchanged config plans `Update` with `uri`/`relative_path` unknown; Core accepts the
  ModifyPlan-set `content_hash`; the old object is left in place.
- [x] **G2-b** `TestAccRuntimeEnvPackageResource_serviceRedeploysOnChange`: an `IN_PLACE` service
  referencing `uri` plans `Update` in the same plan and deploys the new URI; the IN_PLACE check,
  the project/cloud validator, and `serviceDeployFieldsChanged` tolerate the partially-unknown
  Dynamic. Step 3: object lost → package Create, service Update, no new deploy.
- [x] **G2-c** `TestAccRuntimeEnvPackageResource_unchangedTreeEmptyPlan`: re-creating every file in
  another order with new mtimes gives `ExpectEmptyPlan` and no upload.
- [x] **G2-d** `TestAccRuntimeEnvPackageResource_changedAfterPlanErrors`: a file edited between plan
  and apply fails the apply with zero uploads. **Core catches it first:** its apply-time re-plan
  fails with "Provider produced inconsistent final plan" on `content_hash`. Create's own
  "source_dir Changed Since Plan" check is the backstop for the window after that re-plan.

## 6. Tests

**Unit** (`runtime_env_package_test.go`): hash invariance and sensitivity, exclude rules, symlinks,
zip structure and golden bytes, scheme rejection, upload headers and redaction.

**Mock acceptance** (names match the `acctest-resource` shard `^TestAcc[A-Za-z]+Resource`): the
four G2 tests plus `_basic`, `_existingObjectSkipsUpload` (mock returns `file_exists=true`) and
`_objectGoneRecreates`. The mock returns the Gate 1 wire shape and fails the test on a PUT without
`Content-Length` or with `Authorization`. `_basic` checks the uploaded zip's structure; golden
bytes are pinned in the unit test.

**Mutation proofs** (each introduced, the test confirmed FAILING, then reverted byte-clean):

| mutation | failing test |
|---|---|
| mtime in the hash | `_unchangedTreeEmptyPlan` |
| ModifyPlan skips the recompute on update | `_contentChangePlansUpdate` |
| ignore `file_exists` | `_existingObjectSkipsUpload` |
| drop the `pkg/` wrapper | `TestRuntimeEnvPackageZip_Structure` |
| trust smart_open `file_exists` | `TestPackageObjectMissing` |
| send `Authorization` to the bucket | `TestUploadToPresignedURL`, `_basic` |
| Read ignores a missing object | `_objectGoneRecreates` |
| ignore `.gitignore` files | `TestRuntimeEnvPackageWalk_Excludes`, `_basic` |

**Real infrastructure:** the two Gate 1 tests above. Fixtures are generated in Go rather than kept
under `testdata/`: the fixture includes a gitignored `.env`, which git would not commit. They are
fixed, so reruns write the same keys.

**Sweeper: none.** There is no list or delete API for packages and the provider holds no bucket
credentials. Leaked objects are small, inert and reused. Cleaning the static cloud's bucket is a
lifecycle-rule decision for the user; tests must not configure it.

## 7. Docs

The resource example wires a package into a service's `working_dir` (no `import.sh`).
`templates/guides/runtime-env-packages.md` carries the shared model, linked from the resource
description. **Deferred:** replacing the branch-archive `working_dir` in the `anyscale_service`
example and linking the guide from `ray_serve_config`; both touch `anyscale_service`, which has a
separate change pending. Changelog: `new-resource`.

**Accepted costs:** every plan reads the whole tree; storage is never reclaimed by the provider;
Azure and smart_open are rejected and K8s is unverified; moving from the CLI costs one redeploy of
identical code; a saved plan applied after the tree changes errors.
