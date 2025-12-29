# Testing Guide

## Overview

This Terraform provider includes comprehensive unit and acceptance tests that validate both provider logic and actual API integration.

## Test Types

### Unit Tests

Located in files like `models_test.go`, `resource_cloud_test.go`, and `resource_cloud_resource_test.go`, these tests:

- **JSON marshaling/unmarshaling** - Verify API request/response models serialize correctly
- **Helper functions** - Test ID parsing, random string generation, credential handling
- **Data transformations** - Validate expansion/flattening of Terraform schema to/from API models
- **Validation logic** - Test provider detection, config validation, etc.

**Running unit tests:**
```bash
go test ./internal/provider -v -run 'Test[^A]'
```

### Acceptance Tests

Located in `resource_cloud_acc_test.go` and `resource_cloud_resource_acc_test.go`, these tests perform **end-to-end validation** including:

#### What They Test

1. **CRUD Operations** - Create, Read, Update, Delete resources via Terraform
2. **API Validation** - After each operation, verify resources exist in the Anyscale API
3. **Attribute Validation** - Confirm resource fields match expected values in the API
4. **Import Testing** - Verify resources can be imported into Terraform state
5. **Update Testing** - Test mutable field updates propagate correctly

#### API Validation Functions

**`testAccCheckCloudExistsInAPI(resourceName)`**
- Makes a `GET /api/v2/clouds/{id}` call
- Verifies the cloud exists and returns HTTP 200
- Confirms the cloud ID matches Terraform state

**`testAccCheckCloudAttributes(resourceName, name, provider, region)`**
- Fetches cloud from API
- Validates name, provider, and region match expected values
- Returns error if any attribute mismatches

**`testAccCheckCloudResourceExistsInAPI(resourceName, expectedName)`**
- Lists cloud deployments via `GET /api/v2/clouds/{id}/resources`
- Searches for the specific resource by name
- Confirms resource exists in the API response

**`testAccCheckCloudResourceAttributes(resourceName, expectedName, computeStack)`**
- Fetches cloud resources from API
- Validates resource name, compute_stack, and IDs
- Verifies cloud_resource_id and cloud_deployment_id are set

#### Running Acceptance Tests

**Prerequisites:**
- Anyscale API authentication (token or credentials file)
- Set `TF_ACC=1` environment variable

```bash
# Run all acceptance tests
TF_ACC=1 go test ./internal/provider -v -run TestAcc

# Run specific test
TF_ACC=1 go test ./internal/provider -v -run TestAccCloudResource_AWS_Basic

# Run with custom API URL
TF_ACC=1 ANYSCALE_API_URL=https://staging.anyscale.com go test ./internal/provider -v -run TestAcc
```

## Test Configuration

### Authentication

Tests use the same authentication flow as the provider:
1. `ANYSCALE_CLI_TOKEN` environment variable
2. `~/.anyscale/credentials.json` file

### API Client Helper

The `getTestClient()` helper function creates an authenticated API client for validation:

```go
func getTestClient() (*Client, error) {
    apiURL := os.Getenv("ANYSCALE_API_URL")
    if apiURL == "" {
        apiURL = "https://console.anyscale.com"
    }

    token := os.Getenv("ANYSCALE_CLI_TOKEN")
    if token == "" {
        token, _ = GetAuthToken()
    }

    return NewClientWithToken(apiURL, token), nil
}
```

## Test Coverage

### Resource: anyscale_cloud

| Test Case | Coverage |
|-----------|----------|
| `TestAccCloudResource_AWS_Basic` | AWS VM cloud with embedded config, create/update/import |
| `TestAccCloudResource_AWS_EmptyCloud` | AWS empty cloud pattern with auto-generated credentials |
| `TestAccCloudResource_GCP_Basic` | GCP VM cloud with embedded config |
| `TestAccCloudResource_AWS_K8S` | AWS K8S cloud with kubernetes_config |

### Resource: anyscale_cloud_resource

| Test Case | Coverage |
|-----------|----------|
| `TestAccCloudResourceResource_AWS_VM` | AWS VM resource attached to empty cloud |
| `TestAccCloudResourceResource_GCP_VM` | GCP VM resource attached to empty cloud |
| `TestAccCloudResourceResource_AWS_K8S` | AWS K8S resource with kubernetes_config |
| `TestAccCloudResourceResource_WithFileStorage` | Resource with file storage configuration |

## Important Notes

### Test Data

The test configurations use placeholder values that may not work with actual infrastructure:
- VPC IDs like `vpc-test123`
- Subnet IDs like `subnet-test1`
- IAM roles like `arn:aws:iam::123456789012:role/...`

**These tests rely on the Anyscale API accepting placeholder values for testing purposes.**

### Empty Cloud Pattern

Empty clouds are created with auto-generated placeholder credentials:
- **AWS**: `arn:aws:iam::000000000000:role/anyscale-placeholder-{random}`
- **GCP**: JSON with placeholder project_id, provider_id, and service_account_email

### Cleanup

The Terraform testing framework automatically cleans up resources after each test. However, if tests fail or are interrupted, you may need to manually delete test resources:

```bash
# List clouds with "tfacc-test" prefix
anyscale cloud list --name tfacc-test

# Delete test clouds
anyscale cloud delete <cloud-id>
```

## Adding New Tests

### Unit Test Example

```go
func TestMyNewFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    MyInput
        expected MyOutput
    }{
        {
            name:     "basic case",
            input:    MyInput{Field: "value"},
            expected: MyOutput{Result: "expected"},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := myFunction(tt.input)
            if result != tt.expected {
                t.Errorf("got %v, want %v", result, tt.expected)
            }
        })
    }
}
```

### Acceptance Test Example

```go
func TestAccMyNewResource(t *testing.T) {
    if os.Getenv("TF_ACC") == "" {
        t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
        return
    }

    resource.Test(t, resource.TestCase{
        ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
        Steps: []resource.TestStep{
            {
                Config: testAccMyResourceConfig(),
                Check: resource.ComposeAggregateTestCheckFunc(
                    resource.TestCheckResourceAttr("my_resource.test", "name", "test"),
                    // Add API validation
                    testAccCheckMyResourceExistsInAPI("my_resource.test"),
                ),
            },
        },
    })
}
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      # Unit tests (always run)
      - name: Unit Tests
        run: go test ./internal/provider -v -run 'Test[^A]'

      # Acceptance tests (only on main branch with credentials)
      - name: Acceptance Tests
        if: github.ref == 'refs/heads/main'
        env:
          TF_ACC: "1"
          ANYSCALE_CLI_TOKEN: ${{ secrets.ANYSCALE_CLI_TOKEN }}
        run: go test ./internal/provider -v -run TestAcc -timeout 30m
```

## Troubleshooting

### "cloud not found in API" Error

**Cause**: Race condition where Terraform state is updated before API propagation completes.

**Solution**: This is rare but can happen. The test framework handles this gracefully.

### "failed to get auth token" Error

**Cause**: Missing or invalid authentication credentials.

**Solution**:
- Set `ANYSCALE_CLI_TOKEN` environment variable
- Or run `anyscale login` to create `~/.anyscale/credentials.json`

### "API returned error status 403" Error

**Cause**: Token doesn't have permission for the operation.

**Solution**: Use a token with appropriate workspace permissions.

### Tests Hang or Timeout

**Cause**: API operations taking longer than expected.

**Solution**: Increase timeout with `-timeout` flag:
```bash
TF_ACC=1 go test ./internal/provider -v -run TestAcc -timeout 30m
```
