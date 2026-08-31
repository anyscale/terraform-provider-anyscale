# Claude Instructions – Terraform Provider Dev

## Purpose

Go-based Terraform Provider for managing Anyscale resources via the Anyscale API v2, built on
`terraform-plugin-framework`. Aim for idiomatic provider code, stable schemas and state ↔ API
mappings, and generated Registry-compatible docs.

---

## Always follow

- **Never print, log, or commit tokens** — including snippets from credentials files. In examples
  use `$ANYSCALE_CLI_TOKEN` and placeholders.
- **Never put agent/shard names in anything that lands in the repo** — no `tfp-architect`,
  `tfp-forge`, `tfp-assayer`, `tfp-scribe`, `tfp-shipwright`, no `crystl/<shard>` branch names. Not in
  source comments, not in commit messages, not in committed docs. Who found a bug is internal process
  detail that means nothing to a reader and dates badly. Inline comments are the worst case — they
  ship with the code. Keep the reasoning, drop the attribution.
  - **Commit messages are not private.** This repo sets `squash_merge_commit_message =
    COMMIT_MESSAGES`, so GitHub concatenates *every* commit message into the squash body on merge (a
    60-commit PR produced a 1,583-line body on `main` carrying 15 agent references — verified at
    `f1b80f7`). If a name slips through, hand-edit the squash body in the GitHub merge dialog — no
    history rewrite needed.
  - **When scrubbing, match the names specifically — never a bare `tfp-` pattern.** The real test
    fixture is `tfp-test-aws-useast1-STATIC`; scrubbing that string breaks the acctest resolver, the
    sweep-target org guard, and CI's success path. Use
    `tfp-(architect|forge|assayer|scribe|shipwright)`.
- **Be succinct.** Lead with the finding or decision; put only enough evidence under it to check the
  claim. Cut restatement of what the reader knows, recaps of your own prior messages, and narration
  of process. Applies to agent-to-agent messages as much as replies to the user — in a multi-agent
  session the volume compounds and a long message costs every teammate who reads it. Length is not
  thoroughness; an unread finding protects nobody.
- **This repo is the only place files may be created or edited.**
- The product monorepo `~/projects/anyscale/product` is **read-only reference**: read it to
  understand API surface/models; never run build/test/tooling there; never propose changes to it.
- **Trace both halves of the backend.** API code lives in
  `~/projects/anyscale/product/backend/server/api`; the CLI at
  `~/projects/anyscale/product/frontend/cli/anyscale` (`commands/`, `controllers/`). Behavior is
  often split — the CLI may resolve/derive values client-side before calling the API (e.g.
  `anyscale cloud register` resolves the EFS mount-target IP via boto3 unless
  `--skip-verifications`), while the control plane derives them server-side otherwise. Tracing only
  the backend and concluding "the control plane does X" is how this repo has been wrong before.
- **Multi-agent quest commits:** as a Crystl shard you have standing authorization to commit your
  own approved work to your own local `crystl/<shard-name>` branch without re-asking (granted
  2026-07-22). That covers a local commit only — pushing, merging to `main`/an integration branch,
  and opening the PR stay with the shard assigned that task, and merging is the user's call after CI
  is green. A teammate's sign-off does not authorize a commit. Normal git safety still applies: no
  force-push, no `reset --hard`, no skipping hooks, stage specific files rather than `git add -A`.

---

## Repo layout

- `main.go` — entrypoint using `providerserver.Serve`
- `internal/provider/provider.go` — `Metadata`, `Schema`, `Configure`, `Resources`,
  `DataSources`, `EphemeralResources`
- `internal/provider/resource_*.go`, `data_source_*.go` — one resource/data source per file
- `internal/provider/api_helpers.go` — shared request/parse helpers
- `internal/acctest/` — acceptance tests and sweepers
- `templates/` — `tfplugindocs` sources: `index.md.tmpl` (registry landing page), `guides/*.md`
- `docs/` — generated, never hand-edit · `examples/` — runnable Terraform configs

---

## Commands (Makefile is canonical)

```bash
make build            # build binary where dev_overrides expects it
make install          # build + print expected local binary location
make test             # unit tests
make test-compile     # verify tests compile without running them
make testacc          # acceptance tests (real API calls, needs credentials)
make testacc-cover    # acceptance tests with coverage
make fmt lint         # format / lint
make check            # fmt + vet + lint + test-compile
make ci               # deps + check + test
make docs             # regenerate docs from schema + templates/
make docs-validate
make validate-examples  # terraform fmt + tflint + validate across examples/
make sweep            # delete leaked test resources
make sweep-dry-run    # log what would be deleted
pre-commit run --all-files
```

### Local testing with dev_overrides

`~/.terraformrc` points Terraform at the locally built binary.

- **`terraform init` cannot install this provider** — it is not in the public registry, and
  dev_overrides substitutes your local build. Don't expect init to touch `anyscale/anyscale`.
- **Init only skips it safely on Terraform >= 1.15.0.** Earlier versions still query the registry
  for a dev-overridden provider with no registry presence and hard-fail, contradicting
  dev_overrides' own warning text. At >= 1.15 init is safe and sometimes necessary to run for
  unrelated reasons (locking another provider, fetching modules) — it installs every other provider
  normally and reports the overridden one as "not installed as part of init." This is why
  `ci.yml`/`scheduled-acctest.yml` pin `1.15.7`; see the comment on ci.yml's Setup Terraform step.
- Rebuild (`make build`) before every `terraform plan`/`apply`.

```bash
make build
cd examples/aws-vm-basic/ && terraform plan && terraform apply   # no init needed
```

---

## Coding conventions

- Use the shared helpers in `api_helpers.go` (e.g. generic `DoRequestAndParse[T]`) instead of
  hand-rolling request → read → close → status-check → unmarshal.
- **Nullable/optional API field → Computed attribute:** parse into a `*string` and set state with
  `types.StringPointerValue(...)`, so absent/`null` becomes Terraform `null`, never `""`. The
  null-vs-empty-string distinction is a user-facing contract; collapsing it is a bug.
- Report config/auth problems with `resp.Diagnostics.AddError`. No panics, no fatal logs.
- Schema `MarkdownDescription` strings are what `tfplugindocs` publishes — explain non-obvious
  behavior (why a data source takes no arguments, why an attribute can be `null`, what a value is
  for), don't just label the field. `anyscale_organization` is a good example.
- **Optimize published docs for brevity. Clear and direct beats complete.** Both humans and AI agents
  read these pages, and both need signal rather than narrative — a reader scanning for what an
  attribute does should not have to parse a paragraph of accumulated caveat to find it.
  - **An attribute's description covers that attribute.** Anything explaining a *shared model* —
    lifecycle, refresh semantics, how a block behaves as a whole — belongs in `templates/guides/*.md`
    with a link from the attribute, not restated per attribute.
  - **Two smells that mean the text belongs in a guide:** the same explanation appears on more than
    one attribute (or on the same attribute of two resources — that is four copies), or a single
    description runs past roughly 60 words. Neither is a hard limit; both are prompts to check
    whether you are documenting the field or the model.
  - **Omit provider internals.** A practitioner needs "this is not refreshed from the API", not that
    framework Blocks cannot be `Computed`. Keep the *consequence*, drop the mechanism.
  - **Rewrite, never append.** These strings reach their worst state by accretion — each fix adds a
    clause and none removes one. When behavior changes, restate the whole description as it now is.
  - **What earns length:** anything that changes what a practitioner does. A value that can be
    silently overwritten, an attribute rejected at plan time on one provider, an exception to a rule
    stated elsewhere. Cut narration and history; keep consequences.
- **`docs/` is output, not source.** Doc edits belong in the schema's `MarkdownDescription`, in
  `templates/index.md.tmpl` (landing page), or in `templates/guides/*.md` — then `make docs`. Editing
  `docs/` directly gets overwritten on the next regeneration.

### Authentication

Resolution order, used by the provider **and** acceptance tests:

1. `token` argument on the provider block
2. `ANYSCALE_CLI_TOKEN`
3. `~/.anyscale/credentials.json` (Anyscale CLI format, from `anyscale login`)

Centralize resolution in a helper (e.g. `resolveToken(ctx, config)`), initialize one shared API
client, and attach it to both `resp.ResourceData` and `resp.DataSourceData`.

### Connection-level identity → singleton data source

Values invariant across every resource a given provider/token sees (organization identity, other
connection-level metadata) belong in a zero-argument "current X" data source, **not** mirrored as an
attribute on individual resources. Precedents: `anyscale_user` and `anyscale_organization`, both
from `GET /api/v2/userinfo`, both argument-free with no plural variant.

Gotcha: `userinfo` types `organizations` as a *list*, but the handler always returns exactly one
element (the token-scoped org). Trace the real handler, not the model's type, before assuming a
field can hold more than one value.

### Compatibility

- Terraform **>= 1.10** — the floor set by Ephemeral Resources (`anyscale_service_credentials`),
  currently the highest real requirement. Re-verify against actually-adopted primitives before
  citing this number elsewhere.
- The `terraform-plugin-framework` version currently in `go.mod`.
- **Two distinct numbers — don't conflate them.** 1.10 is the *user-facing* floor (README, docs). CI
  and the local dev loop need **>= 1.15** purely for the dev_overrides `init` behavior above; a
  Registry user never hits that path, so the CI pin is not the provider's floor. `sweep.yml`
  deliberately stays at 1.10.0 — it only calls the Anyscale API, never plan/apply.
- A native primitive raises the floor required *to use it*. Whether that moves the globally
  documented floor or stays a per-feature callout is a user decision — confirm the exact number
  against framework source/CHANGELOG, then ask.

---

## Framework-first: prefer native primitives

Native primitives carry semantics Core and downstream tooling already understand; an ad-hoc
reimplementation copies the mechanism and loses the contract.

- **Ephemeral Resources** (TF 1.10+) — for API-returned secrets that must never reach state.
  `Sensitive` does **not** keep a value out of state; omit it or use an ephemeral resource.
  Adopted: `anyscale_service_credentials`.
- **Write-only arguments** (TF 1.11+) — for input secrets that shouldn't persist. Adding a new one
  is additive; converting an existing Sensitive-in-state argument is breaking. Not yet adopted.
- **Actions** (TF 1.14+) — for imperative side-effects outside declarative CRUD. Layer an Action on
  a resource; never bend CRUD to carry an imperative verb. Model 1:1 to real backend operations —
  no synthesized composite verbs. Framework GA-vs-preview status is genuinely ambiguous as of
  framework v1.19.0; re-check before relying on either label. `anyscale_system_cluster_terminate`
  was built and real-infra confirmed, then **deferred** rather than bump the floor to 1.14 — see
  `docs/deferred/actions-adoption/README.md`.
- **`timeouts{}` block** over an ad-hoc duration attribute (v0.19.0 precedent).
- **Plan modifiers / `Default` / state upgraders / validators** over imperative fix-ups in CRUD.

Supporting rules:

- **Only expose a surface with a real end-user consumption path.** Trace backend *and* CLI *and*
  public SDK *and* (if console-adjacent) the web UI — "the API returns it" is not enough.
  `workload_service_url_auth` was built and real-infra confirmed before this check found it was
  console-only cookie plumbing; removed before merge. Contrast `auth_token`, which the CLI prints as
  a ready-to-use `curl` command — that shipped.
- **Hand-write the version floor into the doc page.** `tfplugindocs` renders a compatibility badge
  only for write-only attributes — never for an Ephemeral Resource or Action. Put the floor (and,
  for an Action, its preview status) in the top-level `MarkdownDescription`.
- **Don't demo auto-triggering a destructive Action.** Show
  `terraform apply -invoke=action.<type>.<name>`, not a `lifecycle.action_trigger` block that wires
  it to another resource's lifecycle.
- **Extend tooling to fit a new primitive** rather than filing it under the nearest existing
  category — e.g. `tools/changelog-build/fragment.go` gained real `new-ephemeral-resource` and
  `new-action` types instead of misfiling an ephemeral resource as `new-resource`.
- **Sweep while you're there:** when touching a resource, check whether a hand-rolled timeout,
  imperative Update side-effect, Sensitive-in-state secret, or manual migration now has a native
  replacement, and migrate it under the same breaking-vs-additive and gate discipline.

---

## Import round-trip safety (backend-derived fields)

`terraform import` must yield a **no-op plan** for a realistic config, never destroy-and-recreate.
The recurring bug class: the backend auto-derives fields from a "source" input the user did supply
(`_populate_missing_derived_values`), persists them, and returns them on `GET`. When `ImportState`'s
`flatten*` helpers (`cloud_config_flatten.go` / `requiredImportConfigBlocks`) recover such a field
into a slot marked `RequiresReplace`, a config that set only the source input plans a **replacement
of the live cloud**: config-absent vs. state-present is a diff, and the attribute forces replace.

Known source→derived pairs on the cloud resources: `subnet_ids`→`zones`;
`file_storage_id`→`file_storage.mount_targets` and GCP `mount_path`;
`memorydb_cluster_name`→`memorydb_cluster_arn`+`memorydb_cluster_endpoint`;
`memorystore_instance_name`→`memorystore_endpoint`.

Per-field status changes, so check the schema and the "Import round-trip gaps" section in
`WORKBENCH.md` rather than trusting a status line here. As of `9dd5ecc` the derived slots that
absorb config-omission (`Optional+Computed`) are `mount_targets`, `mount_path`,
`memorydb_cluster_arn`, and `memorystore_endpoint`.

**`kubernetes_config.zones` was listed here as the one remaining exposed slot. It is not an instance
of this bug class — do not "fix" it.** The shape facts are true (`Optional` + `RequiresReplace`, no
`Computed`, on both `anyscale_cloud` and `anyscale_cloud_resource`, and `flattenKubernetesConfig`
does recover it at import). The *derivation* that makes that shape dangerous does not exist for it.
Traced on both halves, each with a positive control proving the search could match real code:

- Backend: `_populate_missing_derived_values`
  (`backend/server/api/base/resources/clouds_resource.py:2572`) dispatches only on AWS and GCP and
  writes only `aws_config`/`gcp_config`. No server-side assignment to `kubernetes_config.zones`
  exists anywhere. (Control: `aws_config.zones = …` is found at `:2619`.)
- CLI: no client-side derivation of Kubernetes zones. (Control: the AWS subnet→AZ resolution in
  `frontend/cli/anyscale/util.py:409` and `:918` is found.)
- Provider: K8S zones are sent only when the practitioner declares them
  (`resource_cloud_resource.go:1318`), and `stringListOrNull` maps an empty API list to **null**, so
  an undeclared value cannot come back as a phantom diff.

So a config that omits `zones` imports to null and plans clean, and one that sets them round-trips.
The genuinely exposed case is narrower and is not this bug class: importing a cloud created outside
Terraform *with* zones set, into a config that omits them — ordinary unwritten-config drift, which
`plan` shows before you apply. Changing the attribute would be a schema change on a
`RequiresReplace` attribute in exchange for nothing.

The `subnet_ids`→`zones` pair listed above is misfiled for the adjacent reason: the backend really
does derive it, but on `aws_config`, and the provider models **no** `zones` attribute there.

**Treat the derived-field import bug class as closed.** Re-open it only for a *new* source→derived
pair, verified on both halves.

**The adjacent question — list *order* sensitivity — is also settled, and negative.** Terraform lists
are order-sensitive, so a recovered `RequiresReplace` list whose element order differed from config
would force replacement on import. It does not happen: the backend preserves order end to end, and
nothing sorts. `security_group_ids` → `aws_security_groups`, `firewall_policy_names` →
`gcp_firewall_policy_ids`, and the GCP subnet list → `gcp_subnet_ids` are all
`Column(PSQL_JSONB())` (`server/database/models/models.py:1220`, `:1246`, `:1226`/`:1250`), and JSON
array element order is preserved by definition — that part is documented, undisputed PostgreSQL
behavior, so it is asserted rather than re-tested. Writes go through `list(...)`
(`cloud_resources_dao.py:317`) and protobuf `repeated`/`extend()` (`:841`); the read helper
`optional_sqlalchemy_json_to_optional_list_of_strings` (`server/util.py:344`) returns `list(obj)`
with no sort. A controlled search found **no** sorting applied to any of these lists (control: a
`sorted(` call *is* found at `resource_tags_dao.py:123`, so the pattern matches real code).
`kubernetes_config.zones` is immune for the stronger reason above — the backend never writes it at
all. Corroborating: the backend's own drift check compares
`resource.security_group_ids == record.aws_security_groups` (`cloud_resources_dao.py:259`), a Python
list equality that is itself order-sensitive, so unstable order would break the control plane before
it broke us.

**Before recovering any field in `flatten*`, ask: does the backend derive it from another input, and
is the attribute `RequiresReplace`?** If yes, choose a fix — and **check block-vs-attribute first,
because it decides which fixes exist:**

- **Leave it null (don't recover)** — for an optional/auxiliary field a valid config may omit.
  Non-breaking; null matches a config that omits it.
- **Model it `Computed`** — for a pure backend-derived output whose slot is an **Attribute**: it
  recovers the real value *and* absorbs config-omission without a diff. But framework **Blocks
  (`ListNestedBlock`/`SingleNestedBlock`/`SetNestedBlock`) cannot be `Computed` at all** — only
  Attributes can. Making a Block `Computed`-capable means converting it to a
  `ListNestedAttribute`, a **breaking HCL change** (`block { ... }` → `block = [{ ... }]`). There is
  no in-between.

Two constraints on every fix:

- **Recover only in `ImportState`, never in `Read`/Create.** The config blocks (`aws_config`,
  `gcp_config`, `kubernetes_config`, `object_storage`, `file_storage`) are deliberately not
  Read-refreshed; populating one outside `ImportState` triggers "provider produced inconsistent
  result after apply" (the C12 regression). Consequence: a recovered value is a frozen import-time
  snapshot — later backend changes won't reach state and won't surface in `plan`.
- **An `ImportState`-only fix does not self-heal existing state.** State imported under a buggy
  version keeps the bad value; neither upgrading nor `apply -refresh-only` corrects it. Affected
  users must re-import (`terraform state rm`, then `terraform import`). Ship every such fix with
  that migration note (precedents: `anyscale_project` collaborators, `anyscale_cloud`
  `mount_targets`).

---

## Design Verification Policy: real-execution gate

At **design-confirmation** time — not just before shipping — get real logged confirmation for any
part of a design that depends on either gate. They catch different failure modes; neither
substitutes for the other.

- **Gate 1 — API response shape.** Whenever correctness depends on what an endpoint actually
  returns *in a specific scenario* (not merely that it exists). Default to a read-only call against
  the static test cloud; escalate to a real create+import only if read-only can't answer it.
- **Gate 2 — Framework/Core contract.** Whenever the design relies on specific plugin-framework or
  Core behavior for a plan modifier, schema shape, or state transition (e.g. whether a modifier may
  rewrite `resp.PlanValue` for a given attribute). Confirm with a real `resource.Test` plan/apply —
  framework source describes the mechanism without revealing every constraint Core enforces at plan
  time, and a unit test built on that source shares its blind spot.

"Done" means a logged request/response or real acceptance-test output cited in the design doc — not
"should behave like X" reasoning, and not a second source-trace restated as independent
verification. If correctness genuinely rests only on documented, undisputed behavior, say so
explicitly and skip the rest.

- **Carry the confirmed wire shape into the mocks.** A fixture returning something the real API
  would never send in that scenario can pass against a broken fix and prove nothing.
- This is separate from the **ship-time** gate (`make build`/`test`/`docs` green, changelog wording
  checked against the merged diff, real-infra end-to-end before tagging). Assign each gate as an
  explicit line item when scoping a design's test criteria.

---

## Testing

Unit tests for schema validation and model conversions; acceptance tests via `resource.Test`.

### Test strength and CI execution

- **Prove a mutation-proof test.** Temporarily introduce the regression, confirm the test FAILS,
  then revert (byte-diff clean). Code review alone doesn't establish that a test protects anything.
- **Match the CI shard name regex or the test silently never runs.** `.github/workflows/ci.yml`
  shards acceptance tests: `acctest-data` runs `^TestAcc[A-Za-z]+DataSource`, `acctest-resource`
  runs `^TestAcc[A-Za-z]+Resource`. A non-matching name neither runs nor fails. Confirm new tests
  genuinely RUN (not SKIP) by reading the shard's job log, not the green checkmark.
- **Naming corollaries.** An Ephemeral Resource's acceptance test runs through `resource.Test` (via
  `echoprovider`), so its name must still end in `Resource` (e.g. `TestAccFooEphemeralResource`, not
  `TestAccEphemeralFoo`). An Action has no acceptance-test tooling in `terraform-plugin-testing` at
  all — only a mocked Go unit test is possible, so it must **not** carry the `TestAcc` prefix.
- **Import-recovery fixes need TWO tests — a Create→Import→re-apply→assert-no-op sequence cannot
  prove one.** An `ImportState` step without `ImportStatePersist: true` (the default) runs in a
  **throwaway working directory**; `terraform-plugin-testing`'s own doc comment on that field says the
  imported state "is discarded at the end of the test step that is verifying import behavior" (see the
  `importStatePersist` branch in `testStepNewImportState`, `helper/resource/testing_new_import_state.go`).
  A later step therefore plans against whatever **Create** left, never against what import recovered —
  it cannot catch an import-recovery bug however its comment reads. This repo carries the empirical
  disproof: a deliberately broken build with `regionSemanticEqualPlanModifier` removed still passed
  that three-step form (see the header of `resource_cloud_import_object_storage_region_acc_test.go`).
  Use instead:
  - **Test A — what import actually recovered.** Assert inside the import step via `ImportStateCheck`,
    which runs in the same throwaway directory and so sees the real imported values. Add
    `ImportStateVerify: true` where a byte-compare is meaningful; a legitimate divergence goes in
    `ImportStateVerifyIgnore` **and** gets an explicit `ImportStateCheck` assertion, never a silent
    exclusion. A cold-import-only test cannot use `ImportStateVerify` at all — there is no prior state.
  - **Test B — planning against the recovered shape.** Two sequential `Config`-only steps (no import),
    which *do* carry state forward, reconstructing the state shape import would produce (e.g. a field
    omitted, then declared) and asserting the plan action on the second. This proves "no spurious
    replace."
  - `ImportStatePersist: true` is not a drop-in fix: `terraform import` refuses an address already
    managed in that working directory (`Error: Resource already managed by Terraform`). It works only
    for a genuinely cold import — import as the first step, resource pre-seeded out of band — as in
    `TestAccComputeConfigResource_ImportRecoversWriteOnlyFields_RealAPI`.
  - The three-step form still proves something real but lesser: that Create's own state survives a
    same-config re-apply. Keep such tests if useful, but label them refresh/plan stability, never
    import coverage.
- **A fixture that cannot represent the failure cannot detect it.** The mock/backend MUST return the
  derived field the fix concerns: the v0.15.2 `mount_targets` import test passed only because its mock
  omitted `mount_targets` entirely — that omission is exactly why the bug shipped green.
- **Never mutate a shared protected fixture.** For throwaway real-infra checks, stand up a
  dedicated narrowly-scoped IAM role (same policies, fresh trust policy, new `external_id`) and tear
  it down — don't touch the static cloud's own role, even temporarily.

### Real infrastructure is pre-authorized

Real EKS and GKE clouds (and what they provision) may be created for acceptance and example testing
without asking, provided **everything is torn down within 24 hours**. Covers `make testacc` and the
scenario targets below. Real **AKS** infrastructure is **not** covered — hold Azure creation until
told otherwise.

### Test cloud selection

Tests resolve a cloud at runtime with no manual setup. Optional overrides:

- `ANYSCALE_TEST_CLOUD_ID` — pin by ID (validated to exist)
- `ANYSCALE_TEST_CLOUD_NAME` — pin by name (must resolve uniquely)

Otherwise: fall back to the pinned default cloud **name** `tfp-test-aws-useast1-STATIC` (a
manually-created known-good fixture) resolved to an ID at runtime, then auto-discovery
(`tf-acc-*`-style prefix), then ephemeral creation. The default lives in the resolver, not a wrapper
script, so local runs, agents, and CI behave identically.

By NAME, not ID, deliberately: the cloud's ID is never committed to this repo. Do not "simplify"
this by hardcoding the ID.

Cleanup: ephemeral clouds are destroyed by default; `ANYSCALE_TEST_KEEP=1` keeps one and prints its
ID/name (never tokens).

### User fixtures for organization_user / invitation tests

These real-infra tests are opt-in by env var and skip cleanly when unset, because they are
destructive (member delete removes a real org member; a role change alters real access) or
rate-limited (invitations) — never point them at a shared or borrowed identity.

- `ANYSCALE_TEST_USER_EMAIL` — an existing accepted org member dedicated to testing, with no clouds
  assigned (these surfaces manage org-level role, not cloud access). Used by organization_user
  import/read/update checks and the org_user/org_users data sources.
- `ANYSCALE_TEST_INVITE_EMAIL` — a fresh, never-invited address under the same disposable identity,
  used for the invitation lifecycle test (including a mixed-case variant). Invalidate invitations
  the tests create.

Same reasoning as `ANYSCALE_TEST_CLOUD_NAME`: the literal address is never committed. Use a real
disposable plus-alias in an inbox you control (`you+tfprovidertest@yourdomain.com`) so invitation
mail lands somewhere safe. Never a colleague's account or any identity whose role you can't afford
to have changed.

### Naming and sweepers

- All test-created resources MUST use `acctest.UniqueName(t, slug)` → `tfacc-<slug>-<rand>`. Never
  hardcode literal names; concurrent CI runs collide. Legacy `tf-test-`/`tfprovider-` prefixes are
  still swept for compatibility but must not be used in new code.
- Sweepers in `internal/acctest/sweeper_*.go` delete resources matching a sweepable prefix that are
  older than `ANYSCALE_SWEEP_MIN_AGE` (default 2h) — the age guard prevents racing live tests. A
  daily job (`.github/workflows/sweep.yml`) runs `make sweep` at 03:00 UTC against the test org.
- **New resource type that creates real backend state → add
  `internal/acctest/sweeper_<type>_test.go`** following `sweeper_project_test.go`. If it lives under
  a cloud, add it to the cloud sweeper's `Dependencies` so it sweeps first.
- Example-based targets wrap apply/destroy in a bash EXIT trap, so destroy fires on apply failure or
  ctrl-C. If you still suspect a leak, run `make sweep-dry-run`.

### Scenario tests (end-to-end apply/destroy over `examples/`)

```bash
make test-primary          # or: test-primary-aws / -gcp / -vm / -k8s
make test-aws-vm-basic     # test-aws-vm-full, test-aws-eks-basic
make test-gcp-vm-basic     # test-gcp-vm-full, test-gcp-gke-basic
```

These run real `terraform apply` **and** `destroy` — check credentials and cloud quotas first.

---

## Policies

### Changelog

`changelog-gate` requires **either** a `.changelog/<PR#>.txt` fragment **or** the `skip-changelog`
label on every PR. Full policy and fragment format:
[CONTRIBUTING.md](CONTRIBUTING.md#changelog-fragments).

- Use `skip-changelog` when nothing requires a new provider version — CI/tooling, tests, internal
  docs, or examples-only edits **outside** `examples/resources/`, `examples/data-sources/`, and
  `examples/provider/`. Those three feed `tfplugindocs` and reach published doc pages, so changes
  there ARE provider-facing.
- Provider changes (schemas, resources/data sources, observable behavior, user-facing bug fixes)
  need a fragment; it folds into the next version bump at release time.
- Unsure? Add a fragment — that's the safe default.

### Deprecation / migration guides

When a change deprecates or removes a user-facing attribute, resource, or data source, **ask the
user** whether it warrants a migration guide instead of writing one unprompted. A breaking change
alone doesn't imply one — the `cloud_deployment_id` → `cloud_resource_id` removal (v0.13.0)
intentionally skipped it, since there were no production users to migrate. That call is the user's
every time.

---

## Anyscale API reference

- OpenAPI/Swagger docs at <https://console.anyscale.com/api/v2/docs> are the primary reference for
  endpoints and schemas.
- Example requests use `Authorization: Bearer $ANYSCALE_CLI_TOKEN`.

### Always prefer `api/v2`

- `api/v2/...` is the current generation — fastest to receive fields, and the convergence target.
  Default to it for all new resources, data sources, acctest helpers, and sweepers.
- `ext/v0/...` is older and may lag (missing fields, stale shapes). Add no new `ext/v0` calls; when
  you touch code that uses it, prefer migrating.
- **Migration is not a pure rename.** Trace each call site against the real backend model (request
  *and* response) first — some are field-identical aliases, others are genuine code changes, and a
  mismatch can fail **silently**. Known example: list/search pagination goes in the request **body**
  on `ext/v0` but as URL **query parameters** on `api/v2`; getting that wrong silently truncates
  results (a sweep that misses candidates and leaks resources) instead of erroring. Watch for
  differing defaults too — `api/v2` compute-config search defaults to latest-version-only where
  `ext/v0` effectively returned all. Migrate related call sites together, not piecemeal.

<!-- crystl-cli:begin v2.163.0 -->
@AGENTS.md
<!-- crystl-cli:end -->
