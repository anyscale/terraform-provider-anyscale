package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// MemoryQuantityType is required_resources.memory's custom string type.
//
// The Anyscale API stores and returns memory as a raw byte count, but the
// schema accepts a Kubernetes-style unit string too (e.g. "4Gi") - the API
// can never echo back the user's original unit-string format, only the
// number. required_resources.memory is a plain (non-Computed) string
// attribute, so without this type, Terraform Core would reject a config of
// "4Gi" once state ends up holding "4294967296" - the exact "provider
// produced inconsistent result after apply" crash class F4 (custom_resources)
// already taught this repo to fix, found again here via a real acceptance
// test run against the integrated branch.
//
// StringSemanticEquals treats two memory strings as equal whenever they
// parse (parseMemoryToBytes) to the same byte count, which the framework
// uses to keep the planned/prior value in state instead of adopting a
// differently-formatted-but-equal one - this fixes Create/Update's crash AND
// Read's refresh diff AND (unlike a manual mask-to-prior fix) the
// no-signal-yet cold-import case, since Terraform's post-import plan compares
// the freshly-imported state to config through this same semantic check.
type MemoryQuantityType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable = MemoryQuantityType{}
)

func (t MemoryQuantityType) Equal(o attr.Type) bool {
	other, ok := o.(MemoryQuantityType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

func (t MemoryQuantityType) String() string {
	return "MemoryQuantityType"
}

func (t MemoryQuantityType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return MemoryQuantityValue{StringValue: in}, nil
}

func (t MemoryQuantityType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}

	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T for MemoryQuantityType", attrValue)
	}

	stringValuable, diags := t.ValueFromString(ctx, stringValue)
	if diags.HasError() {
		return nil, fmt.Errorf("unexpected error converting StringValue to MemoryQuantityValue: %v", diags)
	}

	return stringValuable, nil
}

func (t MemoryQuantityType) ValueType(_ context.Context) attr.Value {
	return MemoryQuantityValue{}
}

// MemoryQuantityValue is MemoryQuantityType's associated value type.
type MemoryQuantityValue struct {
	basetypes.StringValue
}

var (
	_ basetypes.StringValuable                   = MemoryQuantityValue{}
	_ basetypes.StringValuableWithSemanticEquals = MemoryQuantityValue{}
)

// MemoryQuantityValueNull, MemoryQuantityValueUnknown, and
// NewMemoryQuantityValue construct MemoryQuantityValue the same way
// types.StringNull/StringUnknown/StringValue do for a plain string, for call
// sites building one directly rather than through the schema/ValueFromString.
func MemoryQuantityValueNull() MemoryQuantityValue {
	return MemoryQuantityValue{StringValue: basetypes.NewStringNull()}
}

func MemoryQuantityValueUnknown() MemoryQuantityValue {
	return MemoryQuantityValue{StringValue: basetypes.NewStringUnknown()}
}

func NewMemoryQuantityValue(value string) MemoryQuantityValue {
	return MemoryQuantityValue{StringValue: basetypes.NewStringValue(value)}
}

func (v MemoryQuantityValue) Type(_ context.Context) attr.Type {
	return MemoryQuantityType{}
}

func (v MemoryQuantityValue) Equal(o attr.Value) bool {
	other, ok := o.(MemoryQuantityValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals is only invoked by the framework on two known values
// (unknown/null are handled before this is ever called), so parse errors
// here mean a genuinely malformed stored value, not an absent one - treated
// as not-equal (never silently swallow a real parse failure into a false
// "no diff").
func (v MemoryQuantityValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	newValue, ok := newValuable.(MemoryQuantityValue)
	if !ok {
		diags.AddError(
			"Semantic Equality Check Error",
			fmt.Sprintf("An unexpected value type was received while performing semantic equality checks. "+
				"Please report this to the provider developers.\n\n"+
				"Expected Value Type: %T\nGot Value Type: %T", v, newValuable),
		)
		return false, diags
	}

	priorBytes, err := parseMemoryToBytes(v.ValueString())
	if err != nil {
		return false, diags
	}

	newBytes, err := parseMemoryToBytes(newValue.ValueString())
	if err != nil {
		return false, diags
	}

	return priorBytes == newBytes, diags
}
