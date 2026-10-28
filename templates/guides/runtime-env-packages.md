---
page_title: "Runtime Environment Packages: Deploying Local Code"
subcategory: "Concepts & Reference"
description: |-
  How anyscale_runtime_env_package decides when code changed, what it uploads, where the object lives, and why it is never deleted.
---

# Runtime Environment Packages: Deploying Local Code

[`anyscale_runtime_env_package`](../resources/runtime_env_package.md) zips a local directory,
uploads it to the cloud's storage bucket, and outputs a `uri`. Put that `uri` in a service's
`ray_serve_config` as `runtime_env.working_dir` (or in `py_modules`). This is the Terraform
equivalent of pointing `anyscale service deploy` at a local directory.

## When a plan shows a change

Every `terraform plan` reads every included file and hashes it. The hash covers each file's
relative path, contents, and exec bit, and each empty directory. It ignores modification times,
ownership, and where the directory sits on disk, so a fresh `git clone` of the same commit plans
nothing.

When the hash changes, the package plans an update with `uri` known after apply, and every
service that references `uri` plans a rollout in the same plan. The object name is the hash
(`pkg_<sha256>.zip`), so each distinct tree is a separate, immutable object.

If files change between `plan` and `apply`, including when you apply a saved plan, the apply
fails before uploading anything. Terraform reports this as "Provider produced inconsistent final
plan" for `content_hash`. Run `terraform plan` again.

## What is uploaded

Everything under `source_dir` except:

- `.git`, `.venv`, `venv`, and `__pycache__`, anywhere in the tree;
- anything matched by a `.gitignore` in `source_dir` or a subdirectory (each applies to its own
  subtree), which keeps files such as `.env` out of the bucket;
- anything matched by `excludes`, which uses the same pattern syntax, relative to `source_dir`.

Symlinks are followed and stored as their target's contents. The environment variables
`RAY_OVERRIDE_RUNTIME_ENV_DEFAULT_EXCLUDES` and `ANYSCALE_DISABLE_GITIGNORE_EXCLUSION` are
ignored, so the hash does not depend on the machine that runs Terraform.

Each file of 100 MiB or more, and a package over 500 MiB in total, gets a plan warning. Large
data belongs in storage the cluster reads directly, not in a `working_dir`.

## Storage is never deleted

Destroying the resource, or changing its content, leaves earlier objects in the bucket. No
Anyscale API deletes them, and they may still be in use: during a rollout the old version keeps
starting replicas from the old object, and another configuration may package identical content.
To reclaim space, add a lifecycle rule to the bucket for the `runtime_env_packages/` prefix, with
an expiry longer than any version you might roll back to.

If the object disappears anyway, the next plan shows the package being created again under the
same name. The referencing service shows an update, but apply deploys no new version, because the
resolved `uri` is unchanged.

## `uri` or `relative_path`

- Use `uri` as `working_dir` when the service's compute config uses a single cloud resource.
- When the compute config spans several cloud resources, list them all in `cloud_resource_ids`
  and use `relative_path` as `relative_working_dir`. Anyscale resolves it to the bucket of the
  resource each cluster runs on.

## Supported clouds

AWS and GCP clouds (S3 and Google Cloud Storage buckets). Azure clouds, and clouds whose storage is a generic
S3-compatible endpoint, fail at apply with an "Unsupported Cloud Storage" error.

Set `cloud_id` to the cloud of the compute config that runs the code. Anyscale does not stop a
service from naming another cloud's object; whether the cluster can read it depends on that
bucket's permissions, and a failure shows up only when the service starts.

Uploading needs permission to update the cloud's storage. Refreshing needs permission to read it.

## Migrating from the CLI

`anyscale service deploy` names packages after an MD5 of the zip bytes, which include file
modification times. The provider's names differ, so the first apply after importing a
CLI-deployed service shows one `working_dir` change and rolls out identical code once.

## Without this resource

You can build the zip with `archive_file` and upload it with `aws_s3_object` or
`google_storage_bucket_object`. That needs your own write access to the bucket, which you don't
have on Anyscale-hosted clouds, and you then own the naming. Use a content-derived object name
(for example from `output_sha256`): a remote `working_dir` whose contents change under the same
URI can serve stale code, because Ray caches packages by URI.
