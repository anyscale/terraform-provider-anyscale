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

## See Also

- [Provider Documentation](../../docs/index.md)
- [Cloud Data Source](../../docs/data-sources/cloud.md)
- [Compute Config Data Source](../../docs/data-sources/compute_config.md)
- [Anyscale Documentation](https://docs.anyscale.com/)
