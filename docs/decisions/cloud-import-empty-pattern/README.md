# Design: importing an `anyscale_cloud` that has no embedded configuration

**Status: shipped in #413 (2026-11-03). Real AWS run of `examples/aws-vm-basic-resource` with the
`:empty` import: plan after import empty.**

## Problem

A cloud can be managed two ways:

- **All-in-one:** `anyscale_cloud` carries `aws_config`/`gcp_config`/`kubernetes_config`,
  `object_storage` and `file_storage`, and creates the cloud's resource itself.
- **Split:** `anyscale_cloud` carries none of those blocks; each resource is a separate
  `anyscale_cloud_resource` that owns them.

`ImportState` cannot tell these apart. It always recovers the configuration blocks from the cloud's
default resource (`findDefaultInCloudResources` + `requiredImportConfigBlocks`). For a split cloud
those blocks belong to the `anyscale_cloud_resource`, so after import the plan shows a one-time
in-place update on `anyscale_cloud` removing them. No replace; applying it changes state only.

Confirmed on real AWS on 2026-11-03: `examples/aws-vm-basic-resource`, apply then plan empty; after
`state rm` and import of the cloud, plan shows 1 in-place update removing `aws_config` and
`object_storage` from `anyscale_cloud.primary`.

The API has no field that records how a cloud was created. `is_empty_cloud` is derived by the
provider from the resource count, and a split cloud with one resource looks exactly like an
all-in-one cloud. So the person importing has to say which shape their configuration has.

## Decision

Add one import ID form. The existing form is unchanged.

| Import ID | Meaning | Configuration blocks |
|---|---|---|
| `<cloud_id>` | unchanged: all-in-one configuration | recovered from the default resource, as today |
| `<cloud_id>:empty` | the configuration manages this cloud without embedded blocks (split pattern) | not recovered; left null |

- `id` in state is always the bare cloud ID. The suffix only selects import behavior; nothing else
  stores or compares it.
- Any other `:<suffix>` is an error naming both accepted forms. An empty cloud ID before `:empty`
  is an error.
- `:empty` skips the resources listing and block recovery entirely. The fail-closed error for a
  failed resources listing applies only to the bare form, which needs the listing.
- Everything Read sets (name, provider, region, compute stack, toggles, computed fields) is the same
  for both forms.
- Works with `terraform import` and with `import` blocks (`id = "cld_...:empty"`).

Alternatives rejected:

- **Infer from the resource count** (more than one resource means split): a split cloud with one
  resource, the common case, would still import wrongly.
- **Never recover blocks:** breaks import for every all-in-one configuration, where the blocks are
  `RequiresReplace`, so omitting them would plan a replacement of the live cloud.
- **Document the one-time update:** accurate but leaves `import` producing a non-empty plan for a
  supported configuration.

## Compatibility

Additive. The bare-ID import is byte-for-byte unchanged, existing state is unaffected, no schema
change and no state upgrade.

## Acceptance criteria

1. Unit: `<id>:empty` sets `id` to the bare ID and recovers no blocks; `<id>` behaves exactly as
   before (existing tests unchanged and passing); `<id>:other` and `:empty` fail with a diagnostic
   naming the accepted forms.
2. Mock `resource.Test`, import step (what import recovered): a split-pattern cloud imported with
   `<id>:empty` and `ImportStateVerify` passes with no ignore for the blocks. Mutation proof:
   treating `:empty` like the bare form makes it fail.
3. Mock `resource.Test`: bare-ID import of an all-in-one cloud still recovers its blocks
   (regression guard).
4. Real AWS (Gate 2): `make test-aws-vm-basic-resource` passes, with the scenario script importing
   any `anyscale_cloud` whose state holds none of `aws_config`, `gcp_config`, `kubernetes_config`
   using `<id>:empty`, and the plan after import is empty.
5. Docs: `examples/resources/anyscale_cloud/import.sh` shows both forms with one line each on when
   to use which; the split-pattern section of the cloud resources guide says to import the cloud
   with `:empty`. Changelog fragment (enhancement).
