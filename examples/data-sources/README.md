# Data Sources Examples

This directory contains examples demonstrating how to use Anyscale data sources to look up existing resources.

## Available Data Sources

### `anyscale_cloud`

Look up an existing Anyscale Cloud by ID or name.

**Use cases:**
- Reference an existing cloud in compute configs
- Get cloud details for validation or outputs
- Create resources in a specific cloud without hardcoding IDs

**Example:**
```terraform
data "anyscale_cloud" "production" {
  name = "production-cloud"
}

resource "anyscale_compute_config" "example" {
  cloud_id = data.anyscale_cloud.production.id
  # ...
}
```

### `anyscale_clouds`

List and filter Anyscale Clouds.

**Use cases:**
- List all clouds in your organization
- Filter clouds by provider (AWS, GCP, AZURE, etc.)
- Filter clouds by region
- Find clouds matching a name pattern
- Identify default clouds or clouds with specific features

**Example:**
```terraform
# List all AWS clouds
data "anyscale_clouds" "aws_clouds" {
  cloud_provider = "AWS"
}

output "aws_cloud_names" {
  value = [for cloud in data.anyscale_clouds.aws_clouds.clouds : cloud.name]
}

# Filter by name pattern
data "anyscale_clouds" "production" {
  name_contains = "production"
}

# Find default cloud
data "anyscale_clouds" "all" {}

locals {
  default_cloud = [for cloud in data.anyscale_clouds.all.clouds : cloud if cloud.is_default][0]
}
```

### `anyscale_project`

Look up an existing Anyscale Project by ID or name.

**Use cases:**
- Reference an existing project when creating project-scoped resources
- Get project details (directory name, collaborators) for outputs or validation
- Look up a project without hardcoding its ID

**Example:**
```terraform
data "anyscale_project" "team_project" {
  name       = "my-team-project"
  cloud_name = "production-cloud"
}

output "project_collaborators" {
  value = data.anyscale_project.team_project.collaborators
}
```

### `anyscale_projects`

List and filter Anyscale Projects.

**Use cases:**
- List every project in a cloud
- Filter projects by a partial name match
- Exclude each cloud's auto-created default project from results

**Example:**
```terraform
# Every non-default project in a cloud
data "anyscale_projects" "cloud_projects" {
  cloud_name       = "production-cloud"
  include_defaults = false
}

output "cloud_project_ids" {
  value = [for p in data.anyscale_projects.cloud_projects.projects : p.id]
}
```

### `anyscale_compute_config`

Look up an existing Anyscale Compute Configuration by ID or name.

**Use cases:**
- Reference existing compute configs
- Use an existing config as a template
- Get config details for documentation
- Verify configuration settings

**Example:**
```terraform
data "anyscale_compute_config" "template" {
  name = "standard-config"
}

# Create a new config based on the same cloud
resource "anyscale_compute_config" "custom" {
  name     = "custom-config"
  cloud_id = data.anyscale_compute_config.template.cloud_id
  region   = data.anyscale_compute_config.template.region
  # ...
}
```

### `anyscale_container_image`

Look up an existing container image by ID or name.

**Use cases:**
- Get the `name_version` handle to pass to job/service submission tooling
- Check an image's current `build_status` before submitting a workload against it
- Reference `image_uri` for inspection outside Anyscale tooling (e.g. `docker pull`)
- Pin to an exact image `digest` when `name_version` (a named revision) isn't a strong
  enough guarantee

**Example:**
```terraform
data "anyscale_container_image" "training" {
  name = "training-image"
}

output "training_image_name_version" {
  value = data.anyscale_container_image.training.name_version
}

output "training_image_digest" {
  value = data.anyscale_container_image.training.digest
}
```

### `anyscale_container_images`

List and filter container images.

**Use cases:**
- List all container images in a project
- Filter by name pattern, creator, or archived status
- Get the latest build status across many images at once, without fetching each one individually

**Example:**
```terraform
data "anyscale_container_images" "recent" {
  name_contains    = "training"
  include_archived = false
}

output "recent_container_images" {
  value = [
    for img in data.anyscale_container_images.recent.container_images : {
      name         = img.name
      name_version = img.name_version
    }
  ]
}
```

### `anyscale_user`

Get the current authenticated user - the identity behind the provider's token. Takes no arguments.

**Use cases:**
- Look up your own user ID, email, or permission level without hardcoding it
- List the clouds and organizations your token can access

**Example:**
```terraform
data "anyscale_user" "current" {}

output "current_user_email" {
  value = data.anyscale_user.current.email
}
```

### `anyscale_organization`

Get the organization the provider's token is connected to. Takes no arguments - an Anyscale API
token is always scoped to exactly one organization, so there is never a set to select from.

**Use cases:**
- Reference the organization's default cloud without hardcoding its ID
- Look up the organization's name or public identifier for outputs

**Example:**
```terraform
data "anyscale_organization" "current" {}

output "organization_default_cloud_id" {
  value = data.anyscale_organization.current.default_cloud_id
}
```

### `anyscale_organization_user`

Look up a specific user in the organization by identity ID, user ID, or email.

**Use cases:**
- Resolve a user's `identity_id` before importing or creating an `anyscale_organization_collaborator`
- Confirm a user exists in the organization before granting project/cloud access

**Example:**
```terraform
data "anyscale_organization_user" "by_email" {
  email = "user@example.com"
}

output "user_identity_id" {
  value = data.anyscale_organization_user.by_email.id
}
```

### `anyscale_organization_users`

List and filter users in the organization.

**Use cases:**
- List every human user in the organization (service accounts excluded by default)
- Filter by a partial email match
- Include service accounts when needed

**Example:**
```terraform
data "anyscale_organization_users" "humans" {}

output "organization_user_emails" {
  value = [for u in data.anyscale_organization_users.humans.users : u.email]
}
```

### `anyscale_service`

Look up an existing Anyscale Service by ID or name - one deployed by the `anyscale_service`
resource (see `examples/resources/anyscale_service/`), or by some other means (e.g. the Anyscale
CLI or console). To create a service or roll out new versions from Terraform, use the
`anyscale_service` resource instead; this data source stays read-only, for lookups that shouldn't
take over a service's lifecycle.

**Use cases:**
- Get a running service's `current_state`, `base_url`, or rollout status for outputs or validation
- Read the primary version's Ray Serve config back with `jsondecode()`
- Look up a service without hardcoding its ID

**Example:**
```terraform
data "anyscale_service" "production" {
  name       = "my-service"
  project_id = "prj_abc123" # only required if the name isn't unique org-wide
}

output "service_state" {
  value = data.anyscale_service.production.current_state
}
```

### `anyscale_services`

List and filter Anyscale Services.

**Use cases:**
- List every service in a project
- Filter by a partial name match, optionally narrowed to a cloud by name
- Spot every non-running, non-terminated service across a project in one pass, without looking
  each one up individually

**Example:**
```terraform
data "anyscale_services" "in_project" {
  project_id = "prj_abc123"
}

output "unhealthy_service_names" {
  value = [
    for s in data.anyscale_services.in_project.services : s.name
    if s.current_state != "RUNNING" && s.current_state != "TERMINATED"
  ]
}
```

## Running the Examples

1. **Set up authentication:**
   ```bash
   export ANYSCALE_CLI_TOKEN="your-token"
   # OR use ~/.anyscale/credentials.json
   ```

2. **Initialize Terraform:**
   ```bash
   terraform init
   ```

3. **Plan the configuration:**
   ```bash
   terraform plan
   ```

4. **Apply (only if creating resources):**
   ```bash
   terraform apply
   ```

## Important Notes

- **Data sources are read-only** - They query existing infrastructure but don't create or modify anything
- **Authentication required** - You need valid Anyscale credentials
- **Name lookups** - If multiple resources have the same name, the most recently created one is returned
- **Anonymous configs** - Compute configs created without a name can only be looked up by ID
- **Container image name lookups** - Same rule as clouds: if multiple non-archived images share a
  name, the most recently created one is returned, and Terraform logs a warning when this happens
- **Service name lookups are the one exception** - `anyscale_service` names are unique only within
  a project, not organization-wide. An ambiguous name lookup fails with an error asking you to set
  `project_id` (and/or `cloud_id`) to disambiguate, instead of silently returning the most recently
  created match the way every other by-name lookup above does

## Common Patterns

### Pattern 1: Environment Separation

```terraform
# Development
data "anyscale_cloud" "dev" {
  name = "dev-cloud"
}

# Production
data "anyscale_cloud" "prod" {
  name = "prod-cloud"
}

# Use appropriate cloud based on workspace
locals {
  cloud_id = terraform.workspace == "prod" ? data.anyscale_cloud.prod.id : data.anyscale_cloud.dev.id
}
```

### Pattern 2: Configuration Templates

```terraform
# Look up a standard config
data "anyscale_compute_config" "standard" {
  name = "company-standard"
}

# Create team-specific configs based on the standard
resource "anyscale_compute_config" "team_a" {
  name                     = "team-a-config"
  cloud_id                 = data.anyscale_compute_config.standard.cloud_id
  idle_termination_minutes = data.anyscale_compute_config.standard.idle_termination_minutes
  # Customize as needed
}
```

### Pattern 3: Multi-Region Deployment

```terraform
data "anyscale_cloud" "us_east" {
  name = "us-east-cloud"
}

data "anyscale_cloud" "us_west" {
  name = "us-west-cloud"
}

resource "anyscale_compute_config" "east_config" {
  cloud_id = data.anyscale_cloud.us_east.id
  # ...
}

resource "anyscale_compute_config" "west_config" {
  cloud_id = data.anyscale_cloud.us_west.id
  # ...
}
```

## Troubleshooting

**"Cloud Not Found"**
- Verify the cloud name or ID is correct
- Check you have access to the cloud in your Anyscale account
- Ensure authentication is properly configured

**"Multiple clouds with same name"**
- This is a warning, not an error
- The most recently created cloud will be used
- Consider using ID lookup for more precise control
- Or rename clouds to be unique

**"No compute config found"**
- Verify the config exists and isn't archived
- Anonymous configs can only be looked up by ID
- Check spelling and casing of the name

**"No service found" / "found N services named ..."**
- Verify the service name or ID is correct, and that it's running in an org your token can reach
- If the error asks you to disambiguate, set `project_id` (and/or `cloud_id`) - unlike the other
  by-name lookups above, this one errors on a duplicate name instead of guessing the most recent

## See Also

- [Provider Documentation](../../docs/index.md)
- [Cloud Data Source](../../docs/data-sources/cloud.md)
- [Clouds Data Source](../../docs/data-sources/clouds.md)
- [Project Data Source](../../docs/data-sources/project.md)
- [Projects Data Source](../../docs/data-sources/projects.md)
- [Compute Config Data Source](../../docs/data-sources/compute_config.md)
- [Container Image Data Source](../../docs/data-sources/container_image.md)
- [Container Images Data Source](../../docs/data-sources/container_images.md)
- [Container Images guide](../../docs/guides/container-images.md)
- [User Data Source](../../docs/data-sources/user.md)
- [Organization Data Source](../../docs/data-sources/organization.md)
- [Organization User Data Source](../../docs/data-sources/organization_user.md)
- [Organization Users Data Source](../../docs/data-sources/organization_users.md)
- [Service Data Source](../../docs/data-sources/service.md)
- [Services Data Source](../../docs/data-sources/services.md)
- [Anyscale Documentation](https://docs.anyscale.com/)
