package acctest

// Regression tests for the compute-config-import-parity quest's validator
// agenda (approved-design.md V1-V3, F4). Each of these is a plan-time
// ConfigValidator with no backend dependency, so `terraform validate` against
// a freshly-built binary (reusing buildProviderBinaryForCLICheck/
// runTerraformValidate/tfValidateJSON from warning_diagnostics_acc_test.go) is
// the right tool - same reasoning as that file's own header comment:
// resource.Test's reattach path does not reliably surface warning-level
// diagnostics, and none of these four need a real API call to validate.
//
// Final severity ruling (quest chat, all evidence-gated):
//   - V1 max_nodes >= 1 / >= min_nodes: HARD error (backend already 422s this
//     today; the client validator is free hardening, non-breaking).
//   - V2 instance_type + required_resources both set: WARNING (backend
//     accepts both side-by-side with no deterministic preference - assayer
//     live-confirmed; redundant but discoverable in the user's own config).
//   - V3 unrecognized market_type string: WARNING (backend has no market_type
//     field at all; our own missing switch default silently coerced to
//     ON_DEMAND - discoverable, low-stakes).
//   - F4 fractional custom_resources: HARD error (escalated from an initial
//     warning-floor lean after assayer found a real apply against this
//     crashes Terraform Core outright - "provider produced inconsistent
//     result after apply" - since custom_resources is backend-typed as a
//     plain integer and gets silently truncated (2.5 -> 2) before the
//     Core consistency check ever runs. A plan-time reject replaces a
//     confusing crash with a clear diagnostic; nothing that works today is
//     broken, so this is Fixed/non-breaking despite being a hard validator).

import (
	"fmt"
	"strings"
	"testing"
)

func TestAccComputeConfigResource_MaxNodesLessThanMinNodesRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := `
resource "anyscale_compute_config" "test" {
  name     = "v1-max-min-check"
  cloud_id = "cld_validate_only_fake"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      instance_type = "m5.xlarge"
      min_nodes     = 5
      max_nodes     = 1
    }
  ]
}
`
	result := runTerraformValidate(t, binDir, resourceHCL)

	if result.Valid {
		t.Fatal("expected validate to fail for max_nodes (1) < min_nodes (5), got valid=true")
	}
	found := false
	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one error diagnostic, got: %+v", result.Diagnostics)
	}
}

func TestAccComputeConfigResource_InstanceTypeAndRequiredResourcesWarns(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := `
resource "anyscale_compute_config" "test" {
  name     = "v2-instance-type-and-required-resources-check"
  cloud_id = "cld_validate_only_fake"

  head_node = {
    instance_type = "m5.large"
    required_resources = {
      cpu = 4
    }
  }
}
`
	result := runTerraformValidate(t, binDir, resourceHCL)

	// This must NOT be a hard error - the backend accepts both fields set
	// side-by-side (assayer live-confirmed), so rejecting it would be a real
	// behavior change against a config that applies successfully today.
	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			t.Errorf("instance_type + required_resources both set must warn, not error (backend accepts this today) - got error: %s: %s", d.Summary, d.Detail)
		}
	}

	// NOTE: `terraform validate` under dev_overrides always emits its own
	// boilerplate "Provider development overrides are in effect" WARNING,
	// unrelated to this validator - a bare "any warning exists" check would
	// pass even if this validator does not exist at all (confirmed: it does
	// pass against the pre-fix tree). Require the warning to actually
	// mention the two attributes in question, not just any warning.
	found := false
	for _, d := range result.Diagnostics {
		if d.Severity != "warning" {
			continue
		}
		if strings.Contains(d.Summary, "instance_type") || strings.Contains(d.Detail, "instance_type") ||
			strings.Contains(d.Summary, "required_resources") || strings.Contains(d.Detail, "required_resources") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning diagnostic mentioning instance_type/required_resources, got: %+v", result.Diagnostics)
	}
}

func TestAccComputeConfigResource_UnknownMarketTypeWarns(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := `
resource "anyscale_compute_config" "test" {
  name     = "v3-market-type-check"
  cloud_id = "cld_validate_only_fake"

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      instance_type = "m5.xlarge"
      market_type   = "NOT_A_REAL_MARKET_TYPE"
    }
  ]
}
`
	result := runTerraformValidate(t, binDir, resourceHCL)

	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			t.Errorf("an unrecognized market_type must warn, not error (non-breaking per the ruling) - got error: %s: %s", d.Summary, d.Detail)
		}
	}

	// Same dev_overrides-boilerplate-warning trap as the V2 test above:
	// require the warning to actually name market_type or the bogus value,
	// not just any warning at all.
	found := false
	for _, d := range result.Diagnostics {
		if d.Severity != "warning" {
			continue
		}
		if strings.Contains(d.Summary, "market_type") || strings.Contains(d.Detail, "market_type") ||
			strings.Contains(d.Detail, "NOT_A_REAL_MARKET_TYPE") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning diagnostic mentioning market_type or the unrecognized value, got: %+v", result.Diagnostics)
	}
}

func TestAccComputeConfigResource_FractionalCustomResourceRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := `
resource "anyscale_compute_config" "test" {
  name     = "f4-fractional-custom-resource-check"
  cloud_id = "cld_validate_only_fake"

  head_node = {
    instance_type = "m5.large"
    resources = {
      "my-custom-fraction" = 2.5
    }
  }
}
`
	result := runTerraformValidate(t, binDir, resourceHCL)

	if result.Valid {
		t.Fatal("expected validate to fail for a fractional custom_resources amount (2.5) - " +
			"the backend stores custom_resources as a plain integer and silently truncates a " +
			"fractional value, which previously crashed the apply with a Terraform Core " +
			"'inconsistent result' error; this must now be rejected at plan time instead")
	}
	found := false
	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one error diagnostic for the fractional custom resource, got: %+v", result.Diagnostics)
	}
}

// TestAccComputeConfigResource_WholeNumberCustomResourceAllowed is the
// negative-space companion to the fractional-rejection test above: F4's
// validator must reject non-integers WITHOUT rejecting the ordinary, valid
// whole-number case it's meant to keep working.
func TestAccComputeConfigResource_WholeNumberCustomResourceAllowed(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	binDir := buildProviderBinaryForCLICheck(t)

	resourceHCL := fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "f4-whole-number-custom-resource-check"
  cloud_id = "cld_validate_only_fake"

  head_node = {
    instance_type = "m5.large"
    resources = {
      "my-custom-whole" = %d
    }
  }
}
`, 4)
	result := runTerraformValidate(t, binDir, resourceHCL)

	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			t.Errorf("a whole-number custom resource amount must validate cleanly, got error: %s: %s", d.Summary, d.Detail)
		}
	}
}
