# Container Image + Compute Config Example

Builds a container image and defines a compute config side by side, then surfaces the two
identifiers you'd hand to a job or service submission command. This is the smallest complete
picture of "I want to run a Ray workload on a custom image with a specific instance shape."

## What this creates

- An `anyscale_container_image_build` that builds a small image from an inline Containerfile
  (a comment shows how to register an existing image with `anyscale_container_image_registry`
  instead, if you already have one built)
- An `anyscale_compute_config` with a head node and an autoscaling worker group

## Why aren't these wired together?

They're deliberately independent resources. There's no attribute on `anyscale_compute_config`
that references a container image, and no `depends_on` in `main.tf` — Anyscale doesn't require or
support tying a compute config to a specific image at the infrastructure level. Instead, you pick
which image and which compute config to use *at job or service submission time*, via the Anyscale
CLI, SDK, or web console — a step that happens outside Terraform. This example's outputs are shaped
to hand you exactly the two values that submission step needs.

See the [Container Images guide](../../docs/guides/container-images.md) for the full explanation,
including why `name_version` (not `image_uri`) is the identifier to use.

## Usage

```bash
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars with your own cloud_id

terraform plan
terraform apply
```

## Outputs

| Output | Description |
| --- | --- |
| `container_image_name_version` | `name:revision` handle for the built image — pass this to job/service submission |
| `container_image_uri` | Raw, pullable image reference (e.g. for `docker pull`) |
| `container_image_build_status` | Current build status |
| `compute_config_name_version` | `name:revision` handle for the compute config — pass this alongside the image handle above |
| `compute_config_id` | ID of the created compute config |

## See also

- [Container Images guide](../../docs/guides/container-images.md)
- [`anyscale_container_image_build` resource docs](../../docs/resources/container_image_build.md)
- [`anyscale_container_image_registry` resource docs](../../docs/resources/container_image_registry.md)
- [`anyscale_compute_config` resource docs](../../docs/resources/compute_config.md)
