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

By default a rollout goes to 100% and the apply waits for it to reach `RUNNING`. To hold the new
version at a partial share first, see [Canary rollouts](#canary-rollouts). `max_surge_percent`
only paces the rollout, and the backend rejects it unless Anyscale support has enabled it for
your cloud.

## Canary rollouts

`canary_percent` (0-100) holds a new version at a share of traffic so you can test it before
committing. Unset, every rollout goes to 100%.

```hcl
resource "anyscale_service" "app" {
  # ... the new build_id or ray_serve_config ...

  # Apply 1: the new version starts and serves no traffic.
  canary_percent = 0
  # Apply 2: change only this line. The same version takes 25%.
  # canary_percent = 25
  # Apply 3: remove the attribute to finish the rollout.
}
```

Each apply waits until the canary is healthy and carries the requested share. Traffic moves
gradually, so this can take a minute or more.

- **Finishing.** Remove `canary_percent` (the rollout completes with Anyscale's automatic ramp)
  or set it to `100` (an immediate cutover, like `anyscale service deploy --canary-percent 100`).
  `100` completes only where Anyscale auto-completes manual rollouts. Elsewhere the apply fails
  after a 5-minute grace period saying the rollout is held at 100%; remove the attribute to
  finish it.
- **Changing the deploy while a canary is held.** Terraform rolls the held canary back, then
  deploys the new configuration, and the plan warns about it. If you revert to the configuration
  from before the canary, it only rolls back, unless `version_name` changed: a new name deploys
  a new version.
- **`version_name`.** Changing only the name of a held canary is rejected at plan time, because
  Anyscale ignores it.
- **Canary fails during the apply.** Terraform rolls it back and reports "Service Rollout
  Rolled Back", as for any [failed rollout](#failed-rollouts). A canary that fails later is not
  acted on: the service shows `UNHEALTHY`. Change the configuration to recover.
- **No canary in flight.** Changing `canary_percent` only records it. It applies to the next
  deploy.
- **Create and restart.** There is no previous version to split with, so it has no effect. The
  value is kept for the next deploy.
- **Not with `IN_PLACE`.** A non-null `canary_percent` is rejected at plan time.
- **Canaries started elsewhere.** Terraform only tracks a canary it started, or one it found at
  import. Finish or roll back one started with the CLI or console before applying; otherwise a
  deploy change whose configuration differs from the held canary fails with a "rollout already
  in progress" error.
- **Import.** A held canary is recovered into `canary_percent`. A configuration that omits it
  then plans a change to null, which completes that rollout.

### Example: staged rollout from CI

Drive each stage with one apply by making `canary_percent` a variable. Unset, it is `null`, which
finishes the rollout. These are fragments of a configuration that already defines the service.

```hcl
variable "canary_percent" {
  type    = number
  default = null
}

resource "anyscale_service" "app" {
  # ... name, compute_config_id, build_id, ray_serve_config ...
  canary_percent = var.canary_percent
}

output "service_url" {
  value = anyscale_service.app.base_url
}
```

The steps, each one `terraform apply`:

1. Change the new version's inputs (for example `build_id`) and apply with `canary_percent=0`.
   The canary starts, the apply waits until it is healthy, and it serves no traffic. Only its
   health can be checked at this stage.
2. Apply with `canary_percent=10`, changing nothing else. The same version takes 10% of traffic,
   and the apply returns once it does. Watch error rates and latency on your monitoring.
3. Raise the percentage in further applies (for example 50), again changing only the percent.
   Changing the inputs or `version_name` while a canary is held rolls it back or is rejected.
4. Apply with no value to finish.

To abort, revert the inputs and apply: the held canary is rolled back and nothing is
redeployed. A canary that fails during an apply is rolled back by Terraform and the apply
fails with "Service Rollout Rolled Back", which fails the job. A canary that fails afterwards
is not acted on: check `canary_version.current_state` for `UNHEALTHY` in your monitoring step
and abort as above.

A GitHub Actions workflow that triggers it. The configuration lives in the repository root with
a remote backend (a runner has no state of its own); add the `ANYSCALE_CLI_TOKEN` secret and your
backend credentials.

```yaml
name: service-rollout
on:
  # Stage 0: merging the new build_id (or other input) to main
  # deploys it as a canary with no traffic.
  push:
    branches: [main]
  # Later stages: click "Run workflow" and pick a stage.
  workflow_dispatch:
    inputs:
      stage:
        description: "Canary percent, or finish"
        type: choice
        options: ["0", "10", "50", "finish"]
        default: "10"

concurrency: service-rollout

jobs:
  apply:
    runs-on: ubuntu-latest
    environment: production # add required reviewers for a manual gate
    env:
      ANYSCALE_CLI_TOKEN: ${{ secrets.ANYSCALE_CLI_TOKEN }}
      STAGE: ${{ inputs.stage || '0' }}
    steps:
      - uses: actions/checkout@v4
      - uses: hashicorp/setup-terraform@v3
        with:
          terraform_version: "1.10.5"
      - run: terraform init -input=false
      - name: Apply stage
        run: |
          if [ "$STAGE" = "finish" ]; then
            terraform apply -input=false -auto-approve
          else
            terraform apply -input=false -auto-approve \
              -var "canary_percent=$STAGE"
          fi
      - name: Smoke test
        if: env.STAGE != 'finish'
        run: ./scripts/smoke-test.sh "$(terraform output -raw service_url)"
```

How a rollout runs:

1. **Merge the change.** The push applies stage `0`: the canary starts and takes no traffic. The
   job's smoke test can check only that it is healthy.
2. **Shift traffic.** In the Actions tab, run the workflow on `main` with stage `10`. Look at your
   monitoring, then run it again with `50`. Run each stage on the same commit; merging
   another change in between starts a new rollout.
3. **Finish.** Run it with `finish`.
4. **Abort.** Revert the change on `main`. The reverted configuration matches the one from
   before the canary, so the push rolls the held canary back and redeploys nothing.

`./scripts/smoke-test.sh` is a placeholder for your own checks. `concurrency` stops two stages
applying at once.

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
resource, which never writes it to state. If an organization or cloud setting blocks tokens, an
update fails with "Bearer Token Not Issued" (a create only warns).

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
