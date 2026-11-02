# Design: `anyscale_service` input parity

**Status: implemented. Gate 2 closed (mock `resource.Test`, Terraform 1.17.0); Gate 1 closed
(real API, 2026-10-28).** Builds on `docs/decisions/service-rollout-outcome` (version tracking, rolled-back
revert, restart). Product monorepo traced at `278babb437`.

| # | Input | Decision |
|---|---|---|
| 1 | `query_auth_token_enabled` | Optional+Computed, default `true`, sent as `config.access.use_bearer_token`. Terraform owns the whole `config`. |
| 2 | `grpc_service_names` | Explicit list. Non-empty sends `config.protocols.grpc = {enabled: true, service_names}`; null/empty sends gRPC disabled. Not derived from `ray_serve_config`. |
| 3 | `env_vars` | Map sent as `config.env_vars` only; not merged into each application's `runtime_env` as the CLI does. |
| 4 | `image_uri` (+ `registry_login_secret`) | Exactly one of `image_uri`/`build_id`; `build_id` becomes Optional+Computed. `ray_version` deferred. |
| 5 | `version_name` | Optional+Computed. Joins the rolled-back revert set; the reused-name 409 becomes an attribute error. |
| 6 | Default rollout timeout | 30m → 60m. |

Every new deploy field is in `serviceDeployFieldsChanged`, the rolled-back revert set
(`withPriorServiceDeployFields`) and the IN_PLACE frozen set (`serviceInPlaceFrozenChanges`).
`TestServiceDeployFieldsAreRevertedOnRollback` and `TestServiceDeployFieldsAreFrozenUnderInPlace`
enumerate the model by reflection, so a future deploy field missing from either set fails.

## Backend facts

**The `config` object** (`backend_shared_utils/models/services.py:100-127`, `extra=forbid`):
`max_uptime_timeout_sec`, `access.use_bearer_token` (default true), `protocols` (HTTP :8000 on,
gRPC :9000 off), `env_vars`, `traffic_policy`.

- **A sent `config` is stored whole.** `compose_service` stores
  `request.config.dict(exclude_unset=True, …)` (`services/api/services_resource.py:693-697`), but
  the router first round-trips the body through `parse_obj(config.dict())`
  (`services_formatter.py:58-61`; pydantic 1.10), which marks every field set. Anything this
  resource does not send resets to its default: on an imported CLI service,
  `max_uptime_timeout_sec` → 0, and gRPC/env vars unless set here. Documented in the resource's
  top-level note.
- **Service-level, applied in the apply transaction.** `existing_service.service_config` is replaced
  when the request carries one (`services_dao.py:901-902`). Enablement is evaluated from the
  request on every ROLLOUT apply (`:926`); `update_bearer_token` regenerates or clears the token
  only on a flip, dropping any rotation secondary (`:927-936`, `:1014-1023`). Before this work the
  resource sent no `config`, so `_should_use_bearer_token` saw no `access` and returned true
  (`:1034-1043`): a Terraform rollout silently re-enabled auth on a service that had it off.
- **No read-back.** GET returns `auth_token`/`secondary_auth_token` but no `config`
  (`services_base_api_models.py:668-712`). Token presence is the only signal, as the CLI's own
  `query_auth_token_enabled = existing_service.auth_token is not None` (`service_sdk.py:373`).
  `grpc_service_names`/`env_vars` are not refreshed; no endpoint returns `service_config`.
- **A toggle is a full rollout.** No endpoint flips auth in place, and `_start_rollout` has no
  "matches current" short-circuit (`services_dao.py:4153-4200`).
- **IN_PLACE ignores `config` and the version name** (`upgrade_service_in_place`, `:1129-1350`;
  single-version uses the primary, `:1234-1247`). All five new deploy inputs are frozen there; the
  provider does not send `version` under IN_PLACE.
- **Feature flag.** `services_v2_generate_auth_token` (`services/dao/config.py:17-19`) can force
  tokens off per cloud/project regardless of the request (`:1028-1032`).
- **gRPC cap.** `validate_config` (`services_base_api_models.py:485`) allows
  `MAX_AWS_RULE_CONDITIONS - RESERVED_AWS_RULE_CONDITIONS` = 5 − 3 = 2 names, minus 1 when
  `access.use_bearer_token` is true (`services/const.py:38-40`). Enforced in `ValidateConfig`. A
  list, not a bool: the AWS compiler builds one path value per name, `/{name}*`
  (`lb_manager/aws/aws_cloud_formation_compiler.py:486-528`).
- **env_vars consumer.** `_extract_env_vars_from_job_or_service_spec` reads
  `service.service_config.env_vars` into the node's Ray environment
  (`ssm_v2/cluster_manager_config_service.py:1484`, `:2453-2459`).

**Image resolution** (`POST /api/v2/builds/get_or_create_build_from_image_uri`,
`builds_router.py:216-243` → `builds_service.py:155-244`):

| Prefix | Behavior |
|---|---|
| `anyscale/image/<name>:<rev>` | Pure lookup; 409 unless `succeeded` |
| `anyscale/ray…` | Pure lookup of the default image; 404 if unknown |
| anything else (BYOD) | Get-or-create an **anonymous** env named after the sanitized URI; reuse the newest `succeeded` build with the same image **and** `registry_login_secret`; else create one |

- Requires `NON_BASE_IMAGE_CREATE` on the org for every branch, lookups included (`:216-234`):
  stated in `image_uri`'s description.
- A registry URI is a separate copy of an image `anyscale_container_image_registry` manages (same
  image, different build ID): the description steers managed images to `build_id`.
- `ray_version` applies only when the env/build is first created
  (`cluster_environments_resource.py:985-997`): a later change silently does nothing, so it is
  deferred.
- `cloud_id` is required on an Azure control plane only (`builds_service.py:164-171`); like the CLI
  (`anyscale_client.py:1181-1208`), the provider probes `GET /api/v2/workos/deployment_infra` and
  sends the compute config's cloud only there.

**Version names** (`version`, default `"v-" + random`, `services_resource.py:735`): unique per
service across all rows, terminated included. The pre-check 409s on a new rollout and on a restart
(`services_dao.py:965-967`, `:3670-3671`, `:1056-1081`), with CLI-worded detail (`:214-219`). An
active rollout with matching config is updated with no name check (`:3679-3701`).

**Timeout.** `services_v2_rollout_duration_threshold_sec` is defined but never read
(`services/dao/config.py:8`, `:21-25`; grep across `backend/`, `backend_shared_utils/`,
`frontend/cli/`, `go/`, `infra/`, `shared_anyscale_utils/`): an unconstrained rollout has no
server-side deadline. 60m matches the in-place supervision bound (`in_place_update.py:21-22`).
One constant governs create, update and delete.

## Design

**`config` body.** `serviceConfigFromModel` builds `access`, `protocols.grpc` and `env_vars` in one
place and every apply sends it. Null and empty `grpc_service_names`/`env_vars` send the same
request, so switching between them is not a deploy (`serviceStringCollectionChanged`).

**`query_auth_token_enabled`.** Read sets it from token presence. A null prior value (state from
before the attribute, not yet refreshed) is not a change. If the plan asks for `true` and the
settled service has no token: Update records `false` and errors ("Bearer Token Not Issued"); Create
only warns, because an error would taint a healthy service. The next refresh records `false`
either way.

**`image_uri`.** `ModifyPlan` (`planServiceBuildID`), when config `build_id` is null and
`image_uri` is set:

1. `GET /api/v2/builds/{state.build_id}` (200 or 201).
2. Satisfied if `docker_image_name == image_uri` and the registry secret matches, or
   `image_uri == "anyscale/image/" + application_template.name + ":" + revision`.
3. Satisfied → plan `build_id` = state (no resolve, no rollout). Otherwise unknown, and the
   rollout-changed state attributes are marked unknown too (see G2-2). An unreadable build keeps
   the current one unless `image_uri`/`registry_login_secret` changed.

Apply resolves an unknown `build_id` before deciding whether to deploy, so a re-resolve that
returns the current build rolls nothing out. A resolved build in `failed` status is an error.
`image_uri`/`registry_login_secret` are never refreshed; import leaves them null (a config using
`image_uri` then shows a one-time in-place update that writes nothing remote). The rolled-back
revert restores them with `build_id`. IN_PLACE names "image_uri (resolves to a new build)".

**`version_name`** (`planServiceVersionName`, after `build_id` is settled):

| Config | Plan rolls out (or restarts) | Planned value |
|---|---|---|
| null | no | state (undoes the framework's unknown on any change) |
| null | yes | unknown; the backend generates it, recorded from the apply response |
| set, ≠ state | — | config; counts as a deploy |
| set, = state | yes | plan-time error "Version Name Must Change" |
| set, = state | no | config |

Under IN_PLACE a null config keeps the state value; a change is frozen. Read refreshes it from
`primary_version.version` while no canary is rolling. A 409 whose body contains
`already exists for service` becomes "Version Name Already Used" on `version_name`. A rolled-back
name is therefore refused at apply, with no remote change; the doc's optional plan-time check via
private state was not built.

## Gate 2 — Framework/Core contract (closed)

| Item | Result | Test |
|---|---|---|
| G2-1 ModifyPlan restores `version_name` to state after the framework marked it unknown | Planned known; no inconsistent-result error | `TestAccServiceResource_VersionNameOmittedStable_MockServer` |
| G2-2 ModifyPlan sets `build_id` unknown on an otherwise empty plan | **First attempt failed**: apply errored "produced an unexpected new value: .primary_version.id". The framework marks Computed attributes unknown only when config differs from state. Fixed by also marking the rollout-changed attributes unknown (`markServiceRolloutAttributesUnknown`, shared with restart) | `TestAccServiceResource_ImageURIDriftDetected_MockServer` |
| G2-3 Update returns an error plus state `false` against a planned `true` | Core keeps the state and shows only the provider's diagnostic, no inconsistent-result error (full error text captured with a non-matching pattern) | `TestAccServiceResource_AuthTokenNotIssuedErrors_MockServer` |
| G2-4 `ExactlyOneOf(build_id, image_uri)` while state holds a Computed `build_id` | Config-level; switching passes | `TestAccServiceResource_BuildIDToImageURINoRollout_MockServer` |
| G2-5 Ephemeral credentials opened after the service's update | Not run; the `anyscale_service_credentials` description does not claim same-run behavior | — |
| G2-6 State written before these attributes refreshes clean | Empty plan, no deploy, values populated | `TestAccServiceResource_PreParityStateRefreshesClean_MockServer` (a wrapper nulls the new attributes in every plan/apply/read of step 1) |

Other mock tests (all match `^TestAcc[A-Za-z]+Resource`): `…_AuthDisabledSendsAccessConfig_MockServer`,
`…_AuthToggleRollsOut_MockServer`, `…_AuthDriftDetected_MockServer`, `…_InPlaceRejectsAuthChange`,
`…_ImportRecoversAuthDisabled`, `…_GrpcServiceNamesSent_MockServer`,
`…_GrpcServiceNamesCapValidated`, `…_InPlaceRejectsGrpcChange`,
`…_EnvVarsSentAsServiceConfig_MockServer`, `…_InPlaceRejectsEnvVarsChange`,
`…_ImageURIResolvesBuild_MockServer`, `…_ImageURIUnchangedNoResolve_MockServer`,
`…_ImportImageURILeavesNull`, `…_InPlaceRejectsImageURIChange`, `…_ImageURIAndBuildIDConflict`,
`…_VersionNameSentAndRefreshed_MockServer`, `…_VersionNameUnchangedWithDeployRejected`,
`…_VersionNameReuseTranslated_MockServer`, `…_InPlaceRejectsVersionNameChange`,
`…_VersionNameRestartNeedsNewName`, `…_ImportRecoversVersionName`. Fixtures: every mock GET carries
`auth_token`; the outcome mock flips the token from the apply body as the backend does, returns
build IDs not derivable from the URI, and generates random version names with the backend's 409 on
reuse.

Each test was mutation-proven: the regression was introduced, the test failed, and the file was
restored byte-identical.

## Gate 1 — real API

**Closed before implementation (2026-10-28, read-only probes):** G1-5 `deployment_infra` → `aws`
for the test org. G1-6 `get_or_create` on `anyscale/ray:2.56.1-slim-py312-cu129` twice → same
`anyscaleray2561-slim-py312-cu129`, `succeeded`, `docker_image_name` = the URI. G1-7 (partial)
`GET /api/v2/builds/{id}` answers **201** with `application_template.name`.

**Image probes, `TestAccServiceResource_RealInfra_ImageURIRegistry`** (no service; closed
2026-10-28):

- **G1-4 closed.** A BYOD `get_or_create` returns a build already `succeeded`; a second call
  returns the same ID; a different `registry_login_secret` returns a new ID. No image pull at
  registration.
- **G1-7 closed.** For a named environment (`cluster_env_name`, as `anyscale image register`
  creates), the build GET's `application_template.name` + `revision` rebuild
  `anyscale/image/<name>:<rev>`, and that alias resolves to the same build. An anonymous
  environment's alias 404s, so the alias rule only ever matches named images.
- **New cost: an archived anonymous environment blocks its URI for the whole org.** After the
  first probe archived `docker-io-rayproject-ray-2-9-0`, every `get_or_create` for
  `docker.io/rayproject/ray:2.9.0` returned `409 "This name is already taken"`: the lookup skips
  archived environments and the create collides with the archived name
  (`cluster_environments_resource.py:521-525`). The provider turns this 409 into "image_uri
  Blocked by an Archived Image", naming the environment and the fixes
  (`TestAccServiceResource_ImageURIArchivedEnvironmentErrors_MockServer`). An archived environment
  can be restored only through the API (`cluster_environments_router.py:383-395`);
  `anyscale image unarchive` (hidden) looks the name up through the list endpoint, which never
  returns anonymous environments. **Recovery confirmed:** `POST
  /api/v2/application_templates/search` with `name.equals`, `include_archived` and
  `include_anonymous` found the archived anonymous environment; `POST …/{id}/unarchive` restored
  it; the next `get_or_create` for the URI returned the original build, `succeeded`. The test org
  is back to its prior state. The probe now archives nothing and reuses one fixed URI
  (`docker.io/rayproject/ray:2.10.0`) and one named environment (`parity-probe-named-image`),
  neither sweepable; `get_or_create` is idempotent, so reruns add nothing.

**Service probes, `TestAccServiceResource_RealInfra_InputsParity`** (closed 2026-10-28; one
service, three cluster starts, 16 min; a rollout took 9 min, a rollback 2.5 min). Tokens were
logged only as presence and an 8-character SHA-256 prefix.

- **image_uri:** `anyscale/ray:2.56.1-slim-py312-cu129` resolved to
  `anyscaleray2561-slim-py312-cu129`; the post-create plan was empty, so the satisfied-check
  against the real build GET (201) holds.
- **G1-1 closed.** GET never has a `config` key. Token: create (true) → `a0e28145`; rollout
  (false) → null; rollout (true) → `c438d611`. Re-enabling issues a new token. The 202 apply
  response already reflects the flip (`has_auth_token` false, then true, in the provider's
  debug line), and each toggle created a new version. **The token flip survives a rolled-back
  rollout**: auth is service-level and changes in the apply transaction. After the rollback the
  revert restores `false`, and the next refresh records `true` from the token.
- **G1-2 closed.** Re-applying the rolled-back name `tfacc-v3` returned the translated
  "Version Name Already Used" with no rollout. An out-of-band PUT of the current primary's name
  with a changed config → 409 with the `:214-219` detail. The same new name and config sent
  twice with the rollout in flight → 202 both times.
- **G1-3:** `GET …/versions` right after the rollback lists the rolled-back version (`tfacc-v3`,
  UNHEALTHY) beside the terminating and running ones. Not used: a plan-time check would also
  need it to keep terminated versions, which this run did not show.
- **G1-8 closed.** IN_PLACE with a changed `ray_serve_config`, a new name and
  `use_bearer_token=false` → 202; the primary kept its name and the token was unchanged. It was
  accepted while the service still reported ROLLING_BACK.
- **grpc_service_names / env_vars:** every apply carrying them returned 202 from the
  `extra=forbid` model, so the shape is accepted. No endpoint returns the stored `config`, so
  storage is confirmed by trace only (`services_resource.py:693-697`, `services_dao.py:901-902`).

Teardown: `CheckDestroy` confirmed GET 404 for the service; `make sweep-dry-run` reports
`swept=0` for services, projects and compute configs.
