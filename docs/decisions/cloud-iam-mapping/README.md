# Design: `anyscale_cloud_iam_mapping` resource and data source

**Status: design, ungated.** Measured at `3c6a55e`. Every wire fact below is read off the product
monorepo and cited to `file:line`. Two facts are explicitly marked as still requiring a logged
real-execution capture before implementation lands — see [Verification gates](#verification-gates).

## What this manages

Anyscale's **Cloud IAM mapping** lets an operator route dataplane workloads to different cloud IAM
identities based on workload attributes — "jobs in project X assume role A, everything else assumes
the cloud default." Public docs call it Cloud IAM mapping; the console exposes it at
Cloud → Settings → IAM mapping; the CLI reaches it through `anyscale cloud config get/update`.

Runtime effect, for grounding: the first matching rule replaces the compute metadata's account, and
on AWS it clears `Ec2.IamInstanceProfileArn` so lookup falls back to name-based resolution — meaning
an AWS rule `value` must be an IAM **role name** that has an identically-named instance profile
(`go/infra/config/compute/compute.go:350-384`, `TODO(CI-1969)` in that block). If no rule matches
and `fallback_rule = FAIL`, cluster creation is rejected (`compute.go:378-384`).

## Scope: cloud, not organization

This was raised as a possible organization-level surface. It is not, and the evidence is not close.
Recorded here because the naming follows from it:

- Proto: `DataplaneIAMMapping dataplane_iam_mapping = 46` on **`CloudResourceRecord`**
  (`backend/db/proto/data_service.proto:441`; message at `:472-478`). Not on any Organization
  message.
- API: `GET`/`PUT /api/v2/clouds/{cloud_id}/deployment/{cloud_deployment_id}/config`
  (`backend/server/api/product/routers/clouds_router.py:920-925`, `:937-944`). Addressed by cloud
  and deployment. No `org_id` anywhere in the call.
- CLI: `anyscale cloud config get/update` takes a cloud name or id
  (`frontend/cli/anyscale/commands/cloud_commands.py:1241`, `:1407`).
- Storage: a JSONB blob on `cloud_resources` (`backend/server/database/models/models.py:1231`,
  class at `:1213`). No IAM-mapping table or column exists.

An organization-scoped resource would have no endpoint to read or write, and would have to accept a
`cloud_id` to function at all.

**Naming.** The schema attribute is `cloud_resource_id`, **not** `cloud_deployment_id`. The API
spells this concept "deployment"; this provider renamed it to "cloud resource" in v0.13.0 and
`anyscale_cloud` already exposes `cloud_resource_id`. Provider-internal consistency wins over
matching the API's spelling, and the doc page states the equivalence so a reader moving between the
CLI and Terraform is not confused.

## Ownership boundary

One resource instance owns **the entire IAM mapping of one cloud deployment**. It is authoritative
and a singleton per deployment: the write path replaces the mapping wholesale, so two instances
targeting the same deployment will overwrite each other on every apply and produce a permanently
non-empty plan. This is a design constraint, not a documentation note — the doc page must say so
plainly.

It does **not** own the cloud, the deployment, or any other part of the deployment config.
`anyscale_cloud` and `anyscale_cloud_resource` remain the owners of everything else.

### The one-field blast radius

`backend/go/appsidecar/cloud_config_service.go:56-58` clones the existing record and assigns exactly
two fields from the request:

```go
newCloudResource := proto.Clone(req.GetExistingCloudResource()).(*datapb.CloudResourceRecord)
newCloudResource.DataplaneIamMapping = change.DataplaneIamMapping
newCloudResource.UserTagAnnotationPrefix = change.UserTagAnnotationPrefix
```

So the PUT is narrowly scoped — it does not rewrite all 46 fields of the record. But
`user_tag_annotation_prefix` is collateral: **a PUT that omits it wipes it.** Any write this
resource performs must read the current value and resend it unchanged, or setting an IAM rule
silently destroys an unrelated cloud setting.

`user_tag_annotation_prefix` is deliberately **not** modelled as an attribute here — it is not this
resource's to own. It is read and echoed back as preservation only.

## Schema

### Arguments

| Attribute | Kind | Notes |
| --- | --- | --- |
| `cloud_id` | Required, `RequiresReplace` | The cloud. Identity — changing it means a different object. |
| `cloud_resource_id` | Optional + Computed, `RequiresReplace` | The deployment. When omitted, resolves to the cloud's primary (`is_default`) deployment and is written to state. Computed so omission does not diff against the resolved value. |
| `rules` | Optional, `ListNestedAttribute` | **Ordered.** See [Order is load-bearing](#order-is-load-bearing). |
| `rules[*].selector` | Required | Kubernetes label-selector syntax. |
| `rules[*].value` | Required | Cloud IAM identity. On AWS, a role name with a matching instance profile. |
| `fallback_rule` | Optional | `CLOUD_DEFAULT` or `FAIL`. Required when `rules` is non-empty; must be absent when `rules` is empty. See [the silent-ignore hazard](#fallback_rule-silent-ignore). |

### Computed

| Attribute | Notes |
| --- | --- |
| `id` | `<cloud_id>/<cloud_resource_id>`. |
| `mode` | Server-derived, always `CUSTOMER_MANAGED` when a mapping exists. Never an argument. The proto enum carries a second value (`data_service.proto:480-487`) that this endpoint cannot currently produce, which is why the attribute exists at all. |

### Not modelled, deliberately

- **`cloud_provider` and `compute_stack`** — the PUT *requires* both
  (`cloud_config_model.go:77-78`, `:90-91`), validates them, and then **throws them away**;
  `cloud_config_service.go:56-58` never writes them back. They are transport ceremony. The provider
  derives them from the cloud and sends them. Exposing them as configuration would invite a
  practitioner to set a value that cannot take effect.
- **`default_rule`** — renamed to `fallback_rule` and actively rejected when rules are present
  (`cloud_config_model.go:141-143`).
- **`mode` as an argument** — input is never read (`toInternalCloudConfig` ignores
  `externalSpec.DataplaneIAMMapping.Mode`); it is hard-set at `cloud_config_model.go:136-138` and
  re-stamped on read at `:194-195`.
- **A `spec_yaml` / raw-JSON passthrough** — the CLI's only path is `--spec-file` with a whole YAML
  document (`cloud_commands.py:1443-1447`). That is a transport detail. Typed HCL or nothing.
- **`user_tag_annotation_prefix`** — preserved on write, not owned. See above.

### Order is load-bearing

`rules` is a **list**, and its order is semantic: the first matching rule wins
(`go/infra/config/compute/compute.go:350-361`). It must **not** be modelled as a set, and must not
be sorted or normalized. This is the opposite of the usual ruling on collections in this provider,
so it is recorded here explicitly: converting `rules` to a set would silently change which identity
a workload receives.

Selector normalization is a non-issue: email selectors are FNV-64 hashed only for parsing and
matching (`go/infra/config/cloud/util.go:33-41`, `:56-71`); the stored string retains the original
text (`cloud_config_model.go:130-133`). No semantic-equality plan modifier is required.

### `fallback_rule` silent-ignore

`fallback_rule` is **required when `rules` is non-empty** (`cloud_config_model.go:150-151`) and
**silently ignored when `rules` is empty** — the entire block at `:136-156` is gated on
`len(rules) > 0`, and `backend/go/appsidecar/cloud_config_service_test.go:276-287` asserts that a
fallback-rule-only request returns an unmodified clone.

Left unhandled, a config setting `fallback_rule` with no `rules` produces "provider produced
inconsistent result after apply": the value is in the plan, the server drops it, and Read returns it
unset. **Catch this at plan time with schema validation, not by absorbing it as Computed.** An
absorbed value would hide from the practitioner that their setting does nothing.

### Validation, all available at plan time

These are enforced server-side as 400s (`cloud_config_service.go:53` →
`clouds_resource.py:1795-1800`). Mirroring them as schema validators costs no API call and turns a
round-trip failure into a plan-time diagnostic:

| Rule | Source |
| --- | --- |
| Selector must be valid label-selector syntax | `cloud_config_model.go:98-101` |
| Selector must be selectable | `:103-106` |
| Selector keys restricted to `workload-type`, `project`, `user` | `:107-119` |
| `workload-type` values restricted to `job`, `service`, `workspace` | `:110-113` |
| `value` must be non-empty | `:122-124` |
| `fallback_rule` in `CLOUD_DEFAULT`, `FAIL` | `:152-153` |

Server-side validation of the inner shape is otherwise absent: the Python HTTP model is
`spec: Optional[Dict[str, Any]]` (`backend/server/api/base/models/clouds.py:878-881`) passed
straight through (`clouds_resource.py:1919-1920`); the real schema is the Go struct reached over
gRPC. Unknown top-level fields are tolerated, not rejected
(`cloud_config_service.go:44-49`) — so the provider is forward-compatible against new sibling
fields, but gains nothing from the server on typos.

## Lifecycle

**Create** — `PUT` with the desired rules. There is no `POST`; the endpoint is an upsert. Read the
current config first to preserve `user_tag_annotation_prefix`, and to obtain `cloud_provider` /
`compute_stack` for the required-but-discarded payload fields.

**Read** — `GET` the config. Real refresh, so **real drift detection**: an out-of-band change made
in the console or CLI appears in `plan`. This is the decisive advantage over modelling the mapping
as a block on `anyscale_cloud_resource`, whose config blocks are deliberately not Read-refreshed
(the C12 regression), and which would therefore have made drift permanently invisible.

`dataplane_iam_mapping` has **no `omitempty`** (`cloud_config_model.go:19`), so `GET` always emits
the key — as `{}` when unset (the CLI's own documented example output shows
`"dataplane_iam_mapping": {}`, `cloud_commands.py:1236`). Map `{}` to null/empty rather than letting
it materialize as an empty-rules diff, per the repo's null-vs-empty contract.

**Update** — same `PUT`. Every rule and `fallback_rule` is mutable in place; resend the full desired
list. No replacement is needed for any mapping change, and none should be used —
`RequiresReplace` belongs only on the two identity attributes.

**Delete** — a **real revert**, not a state-only removal. A `PUT` whose spec omits `rules` nils the
mapping, because `internalSpec.DataplaneIamMapping` is only assigned inside the `len(rules) > 0`
branch (`cloud_config_model.go:155`).

> **Implementation trap.** An *empty* spec short-circuits to a **no-op, not a wipe**
> (`cloud_config_service.go:33-37`). Destroy must send a **non-empty** spec — `cloud_provider` and
> `compute_stack` populated — with `rules` omitted. Sending an empty spec makes destroy silently do
> nothing while reporting success.

## Import

- Primary form: `terraform import anyscale_cloud_iam_mapping.example <cloud_id>/<cloud_resource_id>`
- Also accepted: bare `<cloud_id>`, resolving the cloud's primary (`is_default`) deployment via
  `findDefaultInCloudResources` (`internal/provider/cloud_helpers.go:312`).

Resolution must **error explicitly** on zero or multiple primaries rather than guessing — matching
the CLI's own behavior (`cloud_controller.py:2920-2941`). Never send the legacy `default` alias:
the server resolves it with a deprecation warning (`clouds_resource.py:1826-1839`, `:1883-1896`).

Post-import expectation: a config declaring `cloud_id` and the live rules plans clean. This resource
is not exposed to the derived-field import bug class — it recovers no field that another input
derives, and `rules` is recovered from a real `GET` rather than reconstructed.

## Timeouts, retry, partial failure

`timeouts{}` block with Create/Update/Delete, following `anyscale_cloud_access`
(`internal/provider/resource_cloud_access.go:481`).

**A 500 from this endpoint does not mean the write did not happen.** `PUT` commits the resource
JSONB (`clouds_resource.py:1926-1928`), then loops all cloud resources propagating the mapping to
machine pools (`:1937-2002`); an RPC failure there raises 500 **after** the commit has landed
(`:2003-2010`). Retry is safe because the `PUT` is a full replace and therefore idempotent, but the
provider must not report or assume "no change was made" on a 500. The honest diagnostic tells the
practitioner the write may have partially applied and that a refresh will show the true state.

Machine-pool propagation is a **no-op for non-PCP clouds**, independently confirmed by two lanes.
Ordinary AWS/GCP/K8s clouds require no machine pool, so this design does **not** build on the
Global Resource Scheduler surface that remains deliberately disabled at
`internal/provider/provider.go:154`, `:189-190`.

## Data source

`anyscale_cloud_iam_mapping` (data source) — read-only, keyed the same way.

The C12 constraint that stops the *resources* from refreshing config blocks does not apply: a data
source contributes nothing to a resource plan, every attribute is Computed, and it is re-read each
plan. `docs/decisions/cloud-data-source-config-blocks/README.md` reasons this out for exactly this
shape and is the precedent to follow.

**Overlap to resolve before shipping:** that ADR plans to expose cloud config blocks on the
*existing* `anyscale_cloud` data source. Shipping a standalone IAM-mapping data source as well gives
two ways to read one value. Default recommendation is the standalone data source, paired 1:1 with
the resource because practitioners expect that pairing — but the overlap is a deliberate decision to
make, not an accident to discover later.

## Compatibility classification

**Purely additive.** A new resource and a new data source; no existing schema, state, or plan
changes. No state upgrader, no migration, no `Version:` bump on any existing resource. Nothing here
can change the plan of a configuration that does not adopt it.

No deprecation or removal, so per repo policy no migration guide is implied — and whether one is
warranted is the user's call, not this document's.

## Acceptance criteria

Written against observable behavior, so they can be exercised without reference to implementation:

1. **Create + read.** A config with two rules and `fallback_rule = FAIL` applies, and a second
   apply of the same config produces an empty plan.
2. **Order is preserved.** Rules applied in order `[A, B]` read back as `[A, B]`. Reversing them in
   config produces a non-empty plan. (A set-modelled `rules` would fail this — that is the point.)
3. **Update in place.** Changing a rule's `value`, adding a rule, and changing `fallback_rule` each
   plan as an in-place update, never a replacement.
4. **Identity replacement.** Changing `cloud_id` or `cloud_resource_id` plans a replacement.
5. **Destroy reverts for real.** After destroy, a fresh read shows no mapping — asserted by reading
   the API, not by absence from state. This is the test that catches the empty-spec no-op trap: a
   destroy that silently did nothing still leaves state clean.
6. **`user_tag_annotation_prefix` survives.** Set it out of band, apply an IAM-mapping change, and
   assert it is unchanged. This is the highest-value test in the set — it is the one that catches
   the collateral-wipe footgun, and it must be written so that it FAILS against a write path that
   omits the field.
7. **`fallback_rule` with no rules is rejected at plan time**, with a diagnostic naming the
   conflict — not accepted-then-silently-dropped.
8. **Invalid selector key / `workload-type` value is rejected at plan time**, without an API call.
9. **Import round-trip.** Both `<cloud_id>/<cloud_resource_id>` and bare `<cloud_id>` import to a
   no-op plan for a config declaring the live rules. Per repo policy this needs the **two-test**
   shape — an `ImportStateCheck` assertion inside the import step, plus a separate two-`Config`-step
   test for planning against the recovered shape. A three-step
   Create→Import→re-apply sequence cannot prove import recovery.
10. **Ambiguous primary errors.** A cloud with zero or multiple `is_default` deployments produces an
    explicit diagnostic on bare-`cloud_id` import, not a silent pick.

Mock fixtures must return `dataplane_iam_mapping` as `{}` for the unset case and must carry
`user_tag_annotation_prefix`, or criteria 6 and the null-vs-empty handling cannot fail against a
broken implementation.

## Verification gates

Per the repo's real-execution policy, two facts need a logged capture before implementation lands —
both are Gate 1 (API response shape), and neither is satisfied by a second source-trace:

1. **Destroy-to-empty on a real cloud.** That a non-empty spec omitting `rules` clears the mapping
   and returns 200. Source-traced and reported clean by one lane; wanted as a logged
   request/response because the empty-spec no-op sits one mistake away.
2. **`user_tag_annotation_prefix` preservation.** That a read-modify-write PUT carrying the field
   leaves it intact, and — as the negative control — that one omitting it does clear it. The second
   half is what proves the test can fail.

Testability, per the validation lane: the **data source** may safely read the shared static test
cloud. The **resource's** Create/Update/Destroy must run against a dedicated ephemeral cloud — an
authoritative overwrite of a shared fixture's IAM mapping is exactly the shared-fixture mutation the
repo forbids.

## Alternatives rejected

**A nested block on `anyscale_cloud` / `anyscale_cloud_resource`.** Rejected. It models ownership
correctly — the deployment does own this field — but those resources' config blocks are deliberately
not Read-refreshed, so the mapping would have no drift detection at all and would sit in state as a
frozen import-time snapshot. IAM mapping is precisely the kind of setting that gets changed
out-of-band; a surface that cannot show it drifted is worse than no surface. A separate resource
buys a real `Read`, which is the whole argument.

Worth noting the fix is not available either: making a framework **Block** `Computed` is impossible,
and converting it to a `ListNestedAttribute` to get there is a breaking HCL change.

**Modelling `rules` as a set.** Rejected — first-match-wins makes order semantic.

**Exposing `cloud_provider` / `compute_stack` as arguments.** Rejected — required by the payload,
discarded by the server, so any value a practitioner set would be a lie.

**A raw `spec` / `spec_yaml` attribute mirroring the CLI.** Rejected — transport detail as
configuration, and it would defeat every plan-time validation above.

**State-only destroy.** Rejected — an actual revert is available, and the org-role precedent for
state-only destroy exists only because that API genuinely has no delete path. This one does.

## Open questions

1. **Standalone data source vs. an `iam_mapping` attribute on the `anyscale_cloud` data source** —
   see [Data source](#data-source). The only item still genuinely open.

### Closed

- **Name — settled: `anyscale_cloud_iam_mapping`, cloud-scoped.** An organization-level name was
  proposed and is contradicted by every trace. A dedicated search for a *separate* org-level IAM
  mapping surface (SSO/SAML/OIDC group or claim mapping) returned a clean negative with positive
  controls proving the search would have found one: it locates org-scoped fields where they exist
  (`sso_mode`, and the `idp_*` SAML columns at
  `backend/server/database/models/models.py:1315-1338`), and it locates `dataplane_iam_mapping`
  where it exists. `grep -rniE "org(anization)?_?iam_?mapping|OrganizationIAMMapping"` over the
  monorepo returns zero hits. The org-scoped `go/infra/proto/iam_service.proto` is SpiceDB RBAC
  relationships and contains no "mapping" concept at all.

  That search also confirmed there is **one** configurable IAM-mapping model, not several competing
  ones. The other things sharing the name are downstream and not configurable: a derived pod
  annotation `anyscale.com/iam-mapping` (`go/infra/instancemanager/k8s/adapter.go:48-49`,
  `:509-518`), the Helm/operator setting keyed on it
  (`go/infra/kubernetes_manager/helm/chart/values.yaml:134-137`), and a docs/UI label.

- **Expose `mode` as Computed — yes.** Resolved by a fact that changes the answer: the proto enum
  `DataplaneIAMMappingMode` has **two** values, `CUSTOMER_MANAGED` and `ANYSACLE_MANAGED` (sic —
  the typo is in the proto) at `backend/db/proto/data_service.proto:480-487`. Only
  `CUSTOMER_MANAGED` is currently reachable through this endpoint, but a second mode existing in the
  data model means a Computed `mode` is forward-compatible, where omitting it would make a future
  second mode a new attribute. Computed-only, so the provider passes through whatever the server
  returns and never has to reproduce the misspelling in user-facing configuration.

## Also worth knowing

**AIOA clouds.** The console hides the IAM mapping tab entirely when `cloud.isAioa`
(`frontend/web/src/pages/clouds/CloudSettings.tsx:74-76`, `:94-95`). Whether the endpoint rejects or
merely ignores a mapping write on an AIOA cloud is **not established** — no code path was traced
that gates the API on it. Not a blocker for the design, but if the resource is pointed at an AIOA
cloud the failure mode is unknown, so it should not be claimed to work there. Worth one read-only
probe before the doc page makes any claim either way.
