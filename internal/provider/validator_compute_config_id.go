package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// computeConfigIDPrefix prefixes every compute config (compute template) ID. The backend mints
// these IDs in exactly one place, from IDTypes.compute_templates = "cpt" joined with "_".
const computeConfigIDPrefix = "cpt_"

// computeConfigIDValidator rejects a known value that is not a compute config ID. The common
// mistake it catches is wiring anyscale_compute_config.<x>.id, which is the config's stable name,
// where the version-specific config_id belongs.
type computeConfigIDValidator struct{}

func (v computeConfigIDValidator) Description(_ context.Context) string {
	return fmt.Sprintf("must be a compute config ID starting with %q", computeConfigIDPrefix)
}

func (v computeConfigIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v computeConfigIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if strings.HasPrefix(value, computeConfigIDPrefix) {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid Compute Config ID",
		fmt.Sprintf("%q is not a compute config ID (expected the %q prefix). "+
			"If it comes from an anyscale_compute_config resource, use its config_id output, not id: "+
			"id is the config's stable name, config_id is the version-specific ID this argument needs.",
			value, computeConfigIDPrefix))
}
