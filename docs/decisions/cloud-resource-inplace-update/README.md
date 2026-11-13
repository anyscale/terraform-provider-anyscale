# Cloud resource fields that update in place

Status: contract, pre-implementation (2026-11-12). Builds on #481 (AWS subnets in place, v0.40.0) and
#494 (plan-time managed-cloud refusal, v0.42.0).

## Problem

`anyscale_cloud` and `anyscale_cloud_resource` mark almost every config-block attribute
`RequiresReplace`. On `anyscale_cloud` that plans destroying the live cloud. On a primary
`anyscale_cloud_resource` it is a plan-time error (the primary-replace guard). The backend's
`PUT /api/v2/clouds/{id}/resources` accepts many of these fields in place, and the CLI
(`anyscale cloud update -f`) changes them out of band, so users see either a destructive plan or no
path at all.

Already shipped, not part of this contract: Read refreshes AWS subnets (`refreshSubnetDrift`), and
subnets update in place. The original drift ask (#8676e13a, ask 1) is done.

## Backend facts (code read, product monorepo; Gate 1 still required, see below)

- The PUT clones the stored record and overlays the request. An omitted **block** is preserved. A
  scalar or list **inside a sent block** is overwritten, so an empty value clears it.
- Rejected on change (400): provider, compute_stack, region, networking_mode, the AWS control-plane
  role and external_id, and the GCP credential fields (project_id, host_project_id, provider_name,
  vpc_name, control-plane service account).
- Accepted in place: AWS `vpc_id`, `security_group_ids`, `cluster_iam_role_id`,
  `cluster_instance_profile_id`; GCP `subnet_names`, `firewall_policy_names`,
  `cluster_service_account_email`; K8S `zones`, `anyscale_operator_iam_identity`, `redis_endpoint`;
  `object_storage` (bucket, endpoint, region); Azure `tenant_id`.
- memorydb / memorystore: add or change accepted, **removal silently ignored**, and the derived
  ARN/endpoint are **not returned on GET**.
- Every update: 409 while any cluster uses the resource; 400 on an Anyscale-managed resource
  (except adding memorydb/memorystore); no AWS/GCP resource verification server-side; an admin-zone
  redeploy after the DB commit can 500 (partial apply).
- GET returns these lists in stored order (AWS subnets excepted: zone-sorted).

## Decision

### In place (both resources, identical behavior)

| Block | Attribute | API field |
|---|---|---|
| aws_config | `vpc_id` | vpc_id |
| aws_config | `security_group_ids` | security_group_ids |
| aws_config | `dataplane_iam_role_arn` | cluster_iam_role_id |
| aws_config | `cluster_instance_profile_id` | cluster_instance_profile_id |
| gcp_config | `subnet_names` | subnet_names |
| gcp_config | `firewall_policy_names` | firewall_policy_names |
| gcp_config | `dataplane_service_account_email` | cluster_service_account_email |
| kubernetes_config | `zones` | zones |
| kubernetes_config | `anyscale_operator_iam_identity` | anyscale_operator_iam_identity |
| kubernetes_config | `redis_endpoint` | redis_endpoint |
| object_storage | `bucket_name`, `endpoint`, `region` | object_storage.* |

### Still replace (unchanged)

Identity and credentials: `cloud_provider`, `compute_stack`, `region`, `is_private`/`is_private_cloud`,
`controlplane_iam_role_arn`, `external_id`, GCP `project_id`, `host_project_id`, `provider_name`,
`vpc_name`, `controlplane_service_account_email`, `azure_config.tenant_id` (a tenant change is an
identity change; also untestable on real infra today).

### Deferred (separate contract, user decision)

`memorydb_cluster_name` / `memorystore_instance_name` and their derived attributes. Removal is
ignored by the backend and the derived values are absent from GET, so neither an in-place update
nor a Read refresh can be made consistent without its own design. This is the "enable head node
fault tolerance" workflow, and managed clouds allow it; worth doing, not in this change.

### Rules

1. **PUT body = live deployment + overlay.** List the live resource, overlay only the planned
   in-place values, send the rest of the live record unchanged (extend
   `buildCloudResourceUpdateRequest`). Never build a block from plan alone: an omitted scalar in a
   sent block clears it. `object_storage` is sent only when it changed, overlaid on the live block.
   **Absent clears.** Inside a sent block, the backend assigns every field unconditionally from the
   decoded request (`backend/go/appsidecar/cloud_deployment_model.go:41-57, 151-154, 210-212,
   98-108`), so an absent field, `null` and an empty value all clear it. `omitempty` therefore does
   not preserve anything: a removal is sent by leaving the field out. The exceptions are
   `object_storage.region`, where empty resets to the deployment region, and AWS `subnet_ids` /
   GCP `subnet_names`, where empty keeps the old value.
   **Lossless echo.** The same rule means the echoed live block must carry every field GET
   returns, including fields the schema does not model. Today's structs drop
   `kubernetes_config.custom_image_registry` (returned at `:301`, assigned at `:57`) and
   `object_storage.s3_addressing_style`, so echoing them clears them. Echo the live block as raw
   JSON (for example `map[string]any`) and overlay only the changed keys, rather than
   round-tripping it through a typed struct. The same gap affects the shipped K8S `file_storage`
   update path.
2. **One PUT** carries every changed in-place field, subnets and `file_storage` included. Gate on
   plan ≠ state for any in-place field; no PUT on drift alone (existing rule).
3. **Removal clears.** Config value → null on an in-place attribute plans `~ update`, and the PUT
   leaves the field out, which clears it on the backend. Exceptions, refused at plan time:
   removing `kubernetes_config.anyscale_operator_iam_identity` (required on K8S) and removing
   `vpc_id` on a VM stack, and removing GCP `subnet_names` (the backend keeps the old value on an
   empty list, so the plan would show a clear that never happens).
   **Whole-block removal.** `aws_config`, `gcp_config` and `azure_config` still carry an identity
   attribute, so removing the block keeps planning replace (or the primary guard error).
   `kubernetes_config` and `object_storage` have none after this change: removing either block is a
   plan-time error, never a silently skipped update (that would be a perpetual diff). The backend
   preserves an omitted block, and a K8S cloud needs both.
4. **Read refresh, Read only.** Refresh each in-place attribute from the live deployment in Read,
   never in the Create/Update readback (C12). Refresh only when the state value is non-null; a null
   (unmanaged) attribute stays null even if the live record has a value. For lists, if the live set
   equals the state set, keep the state's order (no diff on an out-of-band reorder); otherwise write
   live order. Same shape as `refreshSubnetDrift`.
5. **Managed clouds refused at plan time.** Extend the #494 check to every attribute above, same
   fail-open behavior and refusal body.
6. **`vpc_id` consistency.** A `vpc_id` change with no subnet change (either form) is a plan-time
   error: subnets belong to one VPC, so that config cannot be right. The provider cannot verify
   membership beyond this (no AWS credentials).
7. **Errors.** 409 (active clusters) and managed-cloud 400 reuse the existing translations. A failed
   Update leaves state unchanged and never taints. After a post-commit 500, the next refresh picks
   up the live value for managed attributes, so the plan converges.
8. **Remove `RequiresReplace`** (and the primary guard wrapper) from the in-place attributes only.
   No state upgrade: the schema shape is unchanged.

## Workflow after the change

- Change a security group in config → `~ update in place`, one PUT, empty plan afterwards.
- Someone runs `anyscale cloud update` and changes the firewall policies → `plan` shows an in-place
  update back to config; `apply` reverts it.
- Import: flatten recovers these fields as today. A config that omits a recovered value now plans an
  in-place **clear** instead of a replace (or a primary-guard error). Visible in the plan; mention
  it in the changelog.

## Compatibility

Behavior-changing, additive in effect. Not state-breaking, not config-breaking. Plans that showed
replace (or a plan-time error on a primary resource) now show update in place. Changelog:
`release-note:enhancement` for each resource, noting the import consequence above. Minor bump.

## Verification gates

- **Gate 1 (real API, throwaway tfacc clouds only — never the STATIC fixture):** on an AWS VM cloud,
  PUT changing `security_group_ids` + `cluster_instance_profile_id` + `object_storage.bucket_name`,
  then GET: values persisted, other fields intact, list order preserved. On a GCP VM cloud, the same
  for `firewall_policy_names` + `subnet_names`, then a PUT omitting `firewall_policy_names`
  (GET must return it cleared). On a non-primary resource, one change. On EKS or GKE,
  `kubernetes_config.zones`. Logged request/response in this doc.
- **Gate 2 (Core contract, `resource.Test`):** acceptance criteria 1, 2, 4 and 9 below via the mock
  server, plus `object_storage.region` (its semantic-equal plan modifier must still suppress a
  default-region diff with `RequiresReplace` removed).

## Gate 1 results

**AWS, 2026-11-12. Passed.** A throwaway all-in-one cloud in us-east-2, plus a secondary deployment
created with `anyscale_cloud_resource`. Both were destroyed after the run.

- One PUT, built as the live record plus an overlay, changed three fields at once:
  `security_group_ids` (reordered, with one added), `cluster_instance_profile_id` (null to a value),
  and `object_storage.bucket_name`. Result: 200 in 1.9s.
- GET returned all three changes, with the list in the order sent. VPC, subnets, zones, both role
  ARNs and `external_id` were unchanged.
- On the non-primary deployment, a `security_group_ids` change returned 200 and persisted. Its
  other fields were intact, and the primary deployment was byte-identical before and after.
- The PUT body is a JSON list of deployment objects. A bare object returns 422.

**GCP: blocked.** GCP `add_resource` returns a bare 500 in the test org, on a clean create
sequence. That is a backend fault, still under investigation, so no GCP cloud could be registered
to test against. The GCP removal-clears check is not yet confirmed on the real API. It rests on
the backend code read cited in rule 1.

**K8S `zones`: code read only.** It is stored and returned in the order sent
(`cloud_deployment_model.go:41-49, 293-301`) and is never derived. Not run on EKS or GKE.

## Acceptance criteria

The mock backend must model the real semantics: cleared-if-empty inside a sent block, omitted block
preserved, AWS subnets zone-sorted, memorydb ARN/endpoint absent from GET.

1. Each in-place attribute: a config change plans update (not replace, no guard error); the apply
   sends exactly one PUT whose body has the new value **and** every other live field unchanged; the
   next plan is empty. Both resources.
2. Out-of-band change to a state-managed attribute → plan shows update; apply reverts; then empty.
3. Attribute null in state, live has a value → no diff.
4. Removing an attribute from config → plan update to null; PUT omits the field; then empty plan.
5. Managed cloud, change to any in-place attribute → plan-time error, no PUT.
6. Identity attributes still plan replace (non-primary) or the guard error (primary).
7. Plan-time error for each of: `vpc_id` changed without subnets, `vpc_id` removed,
   `anyscale_operator_iam_identity` removed, GCP `subnet_names` removed, the `kubernetes_config` or
   `object_storage` block removed.
8. 409 on update → error, state unchanged, not tainted.
9. Same list set reordered out of band → empty plan.
10. A cloud with memorydb set: an in-place change to another field succeeds (the echoed record has
    the name but no ARN/endpoint).
11. Unmodeled live fields survive an update: with `custom_image_registry` (K8S) or
    `s3_addressing_style` set on the mock's live record, any in-place change (and a K8S
    `file_storage` change) sends them back unchanged.
12. `object_storage.region` set in config equal to the resource's own region: GET elides it (null,
    `cloud_deployment_model.go` toExternalCloudDeployment), so Read must treat a null live region as
    the deployment region, not as a clear. After apply, a refresh and plan is empty, and stays
    empty on the next refresh. Otherwise it is a perpetual in-place diff.

Test names must match `^TestAcc[A-Za-z]+Resource` (e.g. `TestAccCloudResource_InPlace...`).

## Open questions

- Is the Azure `tenant_id` exclusion right? (Assumed identity; revisit if a user asks.)
- Cloud resource `name`: the DAO writes a non-empty name on update. Rename in place is plausible but
  unverified; out of scope here.
