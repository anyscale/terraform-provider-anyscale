---
page_title: "Services: Deploy, Roll Out, Stop"
subcategory: "Concepts & Reference"
description: |-
  How anyscale_service deploys, what triggers a rollout, what happens when one fails, and how to stop, restart and import a service.
---

# Services: Deploy, Roll Out, Stop

[`anyscale_service`](../resources/service.md) deploys a Ray Serve application and rolls out a new
version whenever the inputs that define it change. With the default `ROLLOUT` strategy,
`terraform apply` waits for the rollout and fails if Anyscale rolls it back, so a green apply
means the new version is serving.

## First deploy

A service needs a name, a Ray Serve config, an image, and a compute config:

- **Image:** set `image_uri` (`anyscale/ray:<tag>`, `anyscale/image/<name>:<revision>`, or a
  registry URI) or `build_id`, not both. Use `build_id` for images managed by
  `anyscale_container_image_build` or `anyscale_container_image_registry`; a registry URI in
  `image_uri` registers a separate copy. `image_uri` needs permission to create images in the
  organization, even for base images. See [Container images](./container-images.md).
- **Compute config:** `compute_config_id` takes the `config_id` output of
  `anyscale_compute_config`, not its `id`. It determines the cloud.
- **Code:** package a local directory with `anyscale_runtime_env_package` and use its `uri` as
  `runtime_env.working_dir` in `ray_serve_config`. A code change then plans a rollout. A branch
  archive URL never does, because its address does not change. See
  [Runtime environment packages](./runtime-env-packages.md).

## Rollouts

A change to `ray_serve_config`, `build_id`, an `image_uri` that resolves to a different build,
`compute_config_id`, `connection_ids`,
`description`, `version_name`, `query_auth_token_enabled`, `grpc_service_names` or `env_vars`
rolls out a new version. `tags`, `rollout_strategy`, `max_surge_percent` and `timeouts` do not.

A rollout always goes to 100% and the apply waits for it to reach `RUNNING`. Holding at a partial
canary and promoting later is not supported. `max_surge_percent` only paces the rollout, and the
backend rejects it unless Anyscale support has enabled it for your cloud.

## Version names

`version_name` labels a version. Unset, Anyscale generates one and records it. Set, it is also a
redeploy trigger: changing it rolls out a new version.

A name can be used once per service, including by failed, rolled-back and terminated versions.
Set one only if you change it with every rollout; reusing one fails the apply with "Version Name
Already Used" before anything changes.

## Rollout strategy

`ROLLOUT` (default) starts a new cluster and shifts traffic to it. `IN_PLACE` upgrades the
running cluster, which is faster but freezes everything except `ray_serve_config`: any other
change that would roll out a new version is rejected at plan time. Create ignores the strategy, so
it is safe to set from the start. Changing only the strategy does not redeploy.

Terraform cannot detect a rolled-back or out-of-band `IN_PLACE` upgrade, because the version does not
change. [Failed rollouts](#failed-rollouts) and [out-of-band changes](#out-of-band-changes)
describe `ROLLOUT`.

## Failed rollouts

If the new version fails, Anyscale rolls back to the previous one, which keeps serving. The apply
fails with "Service Rollout Rolled Back", naming both versions. The change stays in the plan, so
the next `terraform apply` tries it again. If you set `version_name`, change it before retrying:
the failed version used the name. Fix the cause first: the error gives the reason when Anyscale
reports one, and otherwise points to the service's events.

If the rollout is still running when the timeout expires, or you interrupt the apply, it continues
on Anyscale. The next refresh settles it: a success needs no further change, and a rollback plans
the change again.

## Rolling back

To go back to the previous version, revert the configuration and apply. That is a new rollout
with the old config, so a `version_name` you set must be a new one. To abort a rollout in
progress, use `anyscale service rollback`; Terraform then plans the change again.

## Out-of-band changes

A deploy or rollback made with the Anyscale CLI or console shows on the next plan as a
`ray_serve_config` difference (and `build_id` or `compute_config_id` if they changed). Applying
redeploys what the configuration says. To keep the change, copy it into the configuration.

## Stopping and restarting

`stopped = true` terminates the service and keeps its id, hostname and record. `false` (default)
restarts it under the same id as a new version. A service terminated outside Terraform, or by
Anyscale after its cluster failed, refreshes to `stopped = true`, so the next plan restarts it
unless the configuration says `true`.

- A service cannot be created with `stopped = true`.
- Change deployed inputs and restart in one apply, but not while stopped or in the apply that
  stops it: set `stopped = false`, deploy, then stop again. `tags`, `timeouts` and the rollout
  settings change freely.
- A restart is a new version, so a `version_name` you set must change too, or the plan fails with
  "Version Name Must Change".
- A service restarted outside Terraform while the configuration says `stopped = true` is stopped
  again on the next apply.
- A restart cannot be rolled back. A failed restart fails the apply like a failed create.
- Stopping and restarting use the `update` timeout.

`terraform destroy` terminates the service and deletes the record for good.

## Query auth

`query_auth_token_enabled` (default `true`) requires requests to carry a bearer token. Changing it
is a full rollout, and re-enabling issues a new token. Read the token with the
[`anyscale_service_credentials`](../ephemeral-resources/service_credentials.md) ephemeral
resource, which never writes it to state. If an organization or cloud setting blocks tokens, the
apply fails with "Bearer Token Not Issued".

## gRPC service names and env vars

`grpc_service_names` lists the gRPC services to route by path prefix, at most one while query auth
is on and two while it is off. Configure Ray Serve's side in `grpc_options` in `ray_serve_config`.
`env_vars` sets environment variables on the cluster nodes; it does not copy them into each
application's `runtime_env`, as the CLI does. Neither can be read back from Anyscale, so drift in
them is not detected.

## Owning config for imported services

Every rollout sends the service's whole `config`, and Anyscale resets any setting you leave out.
A service deployed with the CLI and then imported loses what this resource does not set, such as
`max_uptime_timeout_sec`, gRPC routing, or environment variables, on its first rollout from
Terraform. Set `grpc_service_names` and `env_vars` to match before the first apply.

## Timeouts

`timeouts` defaults to `60m` for `create`, `update` and `delete`. Anyscale sets no deadline on a
rollout, so this is the only limit. When it expires the apply fails but the rollout continues on
Anyscale and is settled on the next refresh (see [Failed rollouts](#failed-rollouts)).

## Import

```shell
terraform import anyscale_service.example service2_abc123
```

The ID is the service ID. After import, `ray_serve_config` holds the live version's config
(possibly normalized by Anyscale), so the first plan shows any difference from yours. A
`connection_ids` list equal to the live set is not a change. `image_uri` and
`registry_login_secret` are null, so a configuration that sets them plans one in-place update that
changes nothing on Anyscale. `grpc_service_names` and `env_vars` are also null, but declaring
either rolls out a new version, which is what writes them. See
[Owning config for imported services](#owning-config-for-imported-services) before the first
apply.
