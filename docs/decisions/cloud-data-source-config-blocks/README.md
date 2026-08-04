# Design: exposing cloud configuration on the `anyscale_cloud` data source

**Status: design, ungated.** Measured at `7496dec`. Unlike the sibling designs in this directory
this one needs no live capture — every fact below is read off code already in the repository, and
the one external dependency (what the deployment endpoint returns) is already consumed by shipped
code paths.

## The gap

`anyscale_cloud` reports a cloud's identity and operational metadata — provider, region, status,
`compute_stack`, `availability_zones`, `external_id`, `cloud_resource_id` — and **none of its
infrastructure configuration**. There is no `aws_config`, `gcp_config`, `azure_config`,
`kubernetes_config`, `object_storage`, or `file_storage` on the data source, though all six exist on
the `anyscale_cloud` and `anyscale_cloud_resource` *resources*.

So a practitioner can look up an existing cloud and learn that it is an AWS cloud in `us-east-1`,
but not which VPC, subnets, IAM roles, or bucket it uses. Referencing an existing Anyscale cloud's
networking from elsewhere in a configuration — the ordinary reason to reach for a data source — is
not possible today.

## Why the resource's constraint does not apply here

The reason the *resources* do not carry these blocks in `Read` is specific and well documented: the
config blocks are deliberately not Read-refreshed, because populating one outside `ImportState`
triggers "provider produced inconsistent result after apply" (the C12 regression). That is a
**plan-consistency** constraint — it exists because a managed resource's post-apply state must match
the plan Terraform computed from configuration.

**A data source has no such constraint.** It contributes nothing to a resource plan, every attribute
is `Computed`, and it is re-read on each plan. There is no prior plan for a refreshed value to
contradict. The blocker that stops the resource genuinely does not stop the data source, and this
asymmetry is the whole reason the gap is closable at all.

## The cost is zero additional API calls

This is the fact that decides the priority. `anyscale_cloud`'s read path **already** fetches
everything required:

- `data_source_cloud.go:287` calls `listCloudResources(ctx, d.client, cloudID)` unconditionally —
  it must, to compute `is_empty_cloud`.
- `data_source_cloud.go:294` calls `findDefaultInCloudResources(resources)`, which returns a
  `*CloudDeploymentResult` (`cloud_helpers.go:312`).
- `*CloudDeploymentResult` is exactly the type the existing flatten layer consumes —
  `requiredImportConfigBlocks(ctx, provider string, defaultResource *CloudDeploymentResult)` at
  `cloud_config_flatten.go:565`.

The API call is already paid, the response is already parsed, and the flattening code is already
written and exercised by `ImportState`. What is missing is only the schema and the assignment.

## The trap: do not reuse `requiredImportConfigBlocks`

The obvious implementation is to call `requiredImportConfigBlocks` and copy its
`map[string]types.Object` onto the data source model. **That would ship a silent Azure blind spot.**

That helper is named for import and is *required-blocks-only* by design. Its own comment
(`cloud_config_flatten.go:302-307`) records the consequence: `azure_config` is optional even for
K8S, so the helper "never recovers it at import", and there is deliberately **no
`flattenAzureConfig`** function at all — only `azureConfigAttrTypes()`, used to build a null object.

The two surfaces want different policies, and conflating them is the error:

| | policy | rationale |
|---|---|---|
| `ImportState` | recover only blocks a valid config **must** declare | recovering an optional block a config omits creates a diff on a `RequiresReplace` attribute |
| data source | report **whatever the cloud actually has** | nothing is being planned against; omission is just missing information |

**Ruling: the data source calls the individual `flatten*` helpers directly under a
report-what-exists policy, and a `flattenAzureConfig` must be written to complete the set.** Reusing
the import wrapper inherits a restriction that exists for a reason that does not apply here.

## Model as Attributes, not Blocks

On the resources these are `SingleNestedBlock`s, which is correct for author-written HCL. On a data
source they are pure outputs and must be `schema.SingleNestedAttribute` with `Computed: true`.

This is not merely stylistic. Framework Blocks cannot be `Computed` at all, so a Block here could
not be populated as an output in the first place. It also keeps the data source consistent with how
it already models composite output (`availability_zones` is a `ListAttribute`, not a block).

Note the consequence for readers migrating between surfaces: the resource is written
`aws_config { ... }` and the data source is read `data.anyscale_cloud.x.aws_config.vpc_id`. That
difference is inherent to block-versus-attribute and should be stated in the
`MarkdownDescription`, not left for a practitioner to discover.

## Scope: singular only, and that asymmetry is already this repo's documented pattern

**Add to `anyscale_cloud`. Do not add to `anyscale_clouds`.**

The plural data source does not fetch deployments per item, so adding config blocks there would
introduce an N+1 across an unbounded list. That trade is already settled on this exact pair, in
`data_source_clouds.go:123`:

> The per-cloud `cloud_resource_id` is deliberately omitted here to avoid an extra API call per
> cloud in the list — use the `anyscale_cloud` data source [...] to look it up.

So this is not a new inconsistency being introduced; it is the existing, documented one applied
consistently to an adjacent field. `anyscale_projects` omits collaborators for the same reason
(`data_source_projects.go:44`, `:79`, `:104`), and `anyscale_services` deliberately does *not*
(`data_source_services.go:53`) because its list response already carries the detail at no extra
call. The rule these three share: **include it in the plural when the list response already has it;
omit it when it costs a call per item.** Cloud config costs a call per item, so it is omitted.

`anyscale_clouds`' description must gain a sentence naming config blocks alongside
`cloud_resource_id`, or the omission reads as an oversight rather than a decision.

## Open question for the user, not for the implementer

Whether to expose configuration this way at all is a product judgment, not purely a technical one.
The blocks contain infrastructure identifiers — VPC IDs, subnet IDs, IAM role ARNs, bucket names,
`external_id`. None is a credential, and `external_id` is *already* exposed on this data source
today, so nothing here newly discloses a secret. But it does make a cloud's full topology readable
by any token that can read the cloud, which is worth an explicit decision rather than an assumption.

## Acceptance criteria

- Mock-server acceptance test per provider (`AWS`, `GCP`, K8S) asserting the corresponding block is
  populated and that the *other* providers' blocks are null. A single-provider test cannot catch a
  dispatch bug that returns the wrong block.
- An Azure case specifically, since that is the one the import wrapper drops — it is the regression
  this design exists to avoid, so it must be the test that fails first if the wrapper is reused.
- A cloud with **no** attached resource (`is_empty_cloud = true`): every block must be null, not
  absent-and-erroring. `findDefaultInCloudResources` returns nil there and the code must handle it,
  the same way `cloud_resource_id` already does at `data_source_cloud.go:296`.
- Naming: `TestAccCloudDataSource...` — CI shards data-source tests on
  `-run '^TestAcc[A-Za-z]+DataSource'` (`.github/workflows/ci.yml:197`). Confirm the tests RUN
  rather than SKIP by reading the shard's job log.

Additive only: new Computed attributes on one data source, no change to any resource, no state
migration. Needs a changelog fragment as an `added` entry.
