package provider

import (
	"context"
	"fmt"
	"math/big"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// DynamicMapToInterface converts a types.Map with types.DynamicType elements
// to a map[string]interface{} for use with the Anyscale API.
// This is used for the flags field which supports mixed types (strings, numbers, booleans).
func DynamicMapToInterface(ctx context.Context, dynamicMap types.Map) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics

	if dynamicMap.IsNull() || dynamicMap.IsUnknown() {
		return nil, diags
	}

	result := make(map[string]interface{})
	elements := dynamicMap.Elements()

	for key, value := range elements {
		dynamicValue, ok := value.(types.Dynamic)
		if !ok {
			diags.AddError(
				"Type Conversion Error",
				fmt.Sprintf("Expected types.Dynamic for key '%s', got %T", key, value),
			)
			continue
		}

		underlyingValue := dynamicValue.UnderlyingValue()

		switch v := underlyingValue.(type) {
		case types.String:
			if !v.IsNull() && !v.IsUnknown() {
				result[key] = v.ValueString()
			}
		case types.Number:
			if !v.IsNull() && !v.IsUnknown() {
				bigFloat := v.ValueBigFloat()
				// Check if it's an integer
				if bigFloat.IsInt() {
					intVal, _ := bigFloat.Int64()
					result[key] = intVal
				} else {
					float64Val, _ := bigFloat.Float64()
					result[key] = float64Val
				}
			}
		case types.Bool:
			if !v.IsNull() && !v.IsUnknown() {
				result[key] = v.ValueBool()
			}
		case types.Int64:
			if !v.IsNull() && !v.IsUnknown() {
				result[key] = v.ValueInt64()
			}
		default:
			diags.AddWarning(
				"Unsupported Type",
				fmt.Sprintf("Key '%s' has unsupported type %T, skipping", key, underlyingValue),
			)
		}
	}

	return result, diags
}

// InterfaceMapToDynamic converts a map[string]interface{} from the API
// to a types.Map with types.DynamicType elements for use in Terraform state.
func InterfaceMapToDynamic(ctx context.Context, input map[string]interface{}) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics

	if len(input) == 0 {
		return types.MapNull(types.DynamicType), diags
	}

	elements := make(map[string]attr.Value)

	for key, value := range input {
		var dynamicValue types.Dynamic

		switch v := value.(type) {
		case string:
			dynamicValue = types.DynamicValue(types.StringValue(v))
		case float64:
			dynamicValue = types.DynamicValue(types.NumberValue(big.NewFloat(v)))
		case int64:
			dynamicValue = types.DynamicValue(types.Int64Value(v))
		case int:
			dynamicValue = types.DynamicValue(types.Int64Value(int64(v)))
		case bool:
			dynamicValue = types.DynamicValue(types.BoolValue(v))
		case nil:
			// Skip null values
			continue
		default:
			diags.AddWarning(
				"Unsupported API Value Type",
				fmt.Sprintf("Key '%s' has unsupported type %T, skipping", key, v),
			)
			continue
		}

		elements[key] = dynamicValue
	}

	if len(elements) == 0 {
		return types.MapNull(types.DynamicType), diags
	}

	mapValue, mapDiags := types.MapValue(types.DynamicType, elements)
	diags.Append(mapDiags...)
	return mapValue, diags
}

// Float64MapToInterface converts a types.Map with Float64Type elements
// to a map[string]interface{} for use with the Anyscale API.
func Float64MapToInterface(ctx context.Context, float64Map types.Map) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics

	if float64Map.IsNull() || float64Map.IsUnknown() {
		return nil, diags
	}

	result := make(map[string]interface{})
	elements := float64Map.Elements()

	for key, value := range elements {
		float64Value, ok := value.(types.Float64)
		if !ok {
			diags.AddError(
				"Type Conversion Error",
				fmt.Sprintf("Expected types.Float64 for key '%s', got %T", key, value),
			)
			continue
		}

		if !float64Value.IsNull() && !float64Value.IsUnknown() {
			result[key] = float64Value.ValueFloat64()
		}
	}

	return result, diags
}

// InterfaceMapToFloat64 converts a map[string]interface{} from the API
// to a types.Map with Float64Type elements for use in Terraform state.
func InterfaceMapToFloat64(ctx context.Context, input map[string]interface{}) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics

	if len(input) == 0 {
		return types.MapNull(types.Float64Type), diags
	}

	elements := make(map[string]attr.Value)

	for key, value := range input {
		var float64Val float64

		switch v := value.(type) {
		case float64:
			float64Val = v
		case int:
			float64Val = float64(v)
		case int64:
			float64Val = float64(v)
		case nil:
			// Skip null values
			continue
		default:
			diags.AddWarning(
				"Type Conversion Warning",
				fmt.Sprintf("Key '%s' has type %T, attempting conversion to float64", key, v),
			)
			continue
		}

		elements[key] = types.Float64Value(float64Val)
	}

	if len(elements) == 0 {
		return types.MapNull(types.Float64Type), diags
	}

	mapValue, mapDiags := types.MapValue(types.Float64Type, elements)
	diags.Append(mapDiags...)
	return mapValue, diags
}

// StringMapToInterface converts a types.Map with StringType elements
// to a map[string]interface{} for use with the Anyscale API.
func StringMapToInterface(ctx context.Context, stringMap types.Map) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics

	if stringMap.IsNull() || stringMap.IsUnknown() {
		return nil, diags
	}

	result := make(map[string]interface{})
	elements := stringMap.Elements()

	for key, value := range elements {
		stringValue, ok := value.(types.String)
		if !ok {
			diags.AddError(
				"Type Conversion Error",
				fmt.Sprintf("Expected types.String for key '%s', got %T", key, value),
			)
			continue
		}

		if !stringValue.IsNull() && !stringValue.IsUnknown() {
			result[key] = stringValue.ValueString()
		}
	}

	return result, diags
}

// InterfaceMapToString converts a map[string]interface{} from the API
// to a types.Map with StringType elements for use in Terraform state.
func InterfaceMapToString(ctx context.Context, input map[string]interface{}) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics

	if len(input) == 0 {
		return types.MapNull(types.StringType), diags
	}

	elements := make(map[string]attr.Value)

	for key, value := range input {
		if value == nil {
			continue
		}

		strVal := fmt.Sprintf("%v", value)
		elements[key] = types.StringValue(strVal)
	}

	if len(elements) == 0 {
		return types.MapNull(types.StringType), diags
	}

	mapValue, mapDiags := types.MapValue(types.StringType, elements)
	diags.Append(mapDiags...)
	return mapValue, diags
}

// StringListToInterface converts a types.List with StringType elements
// to a []string for use with the Anyscale API.
func StringListToInterface(ctx context.Context, stringList types.List) ([]string, diag.Diagnostics) {
	var diags diag.Diagnostics

	if stringList.IsNull() || stringList.IsUnknown() {
		return nil, diags
	}

	elements := stringList.Elements()
	result := make([]string, 0, len(elements))

	for i, value := range elements {
		stringValue, ok := value.(types.String)
		if !ok {
			diags.AddError(
				"Type Conversion Error",
				fmt.Sprintf("Expected types.String at index %d, got %T", i, value),
			)
			continue
		}

		if !stringValue.IsNull() && !stringValue.IsUnknown() {
			result = append(result, stringValue.ValueString())
		}
	}

	return result, diags
}

// InterfaceListToString converts a []interface{} from the API
// to a types.List with StringType elements for use in Terraform state.
func InterfaceListToString(ctx context.Context, input []interface{}) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics

	if len(input) == 0 {
		return types.ListNull(types.StringType), diags
	}

	elements := make([]attr.Value, 0, len(input))

	for _, value := range input {
		if value == nil {
			continue
		}

		strVal := fmt.Sprintf("%v", value)
		elements = append(elements, types.StringValue(strVal))
	}

	if len(elements) == 0 {
		return types.ListNull(types.StringType), diags
	}

	listValue, listDiags := types.ListValue(types.StringType, elements)
	diags.Append(listDiags...)
	return listValue, diags
}

// DynamicToInterface converts a types.Dynamic value to map[string]interface{}
// for use with the Anyscale API. The Dynamic value is expected to contain a map.
func DynamicToInterface(ctx context.Context, dynamicValue types.Dynamic) (map[string]interface{}, error) {
	if dynamicValue.IsNull() || dynamicValue.IsUnknown() {
		return nil, nil
	}

	// Get the underlying value from Dynamic
	underlying := dynamicValue.UnderlyingValue()

	// The underlying value should be an Object or Map
	switch v := underlying.(type) {
	case types.Object:
		// Convert Object to map[string]interface{}
		attrs := v.Attributes()
		result := make(map[string]interface{})
		for key, val := range attrs {
			result[key] = convertAttrValueToInterface(val)
		}
		return result, nil
	case types.Map:
		// Convert Map to map[string]interface{}
		elements := v.Elements()
		result := make(map[string]interface{})
		for key, val := range elements {
			result[key] = convertAttrValueToInterface(val)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("expected Dynamic to contain Object or Map, got %T", underlying)
	}
}

// convertAttrValueToInterface converts an attr.Value to interface{}
func convertAttrValueToInterface(val attr.Value) interface{} {
	switch v := val.(type) {
	case types.String:
		if !v.IsNull() && !v.IsUnknown() {
			return v.ValueString()
		}
	case types.Number:
		if !v.IsNull() && !v.IsUnknown() {
			bigFloat := v.ValueBigFloat()
			if bigFloat.IsInt() {
				intVal, _ := bigFloat.Int64()
				return intVal
			}
			float64Val, _ := bigFloat.Float64()
			return float64Val
		}
	case types.Bool:
		if !v.IsNull() && !v.IsUnknown() {
			return v.ValueBool()
		}
	case types.List:
		if !v.IsNull() && !v.IsUnknown() {
			elements := v.Elements()
			result := make([]interface{}, 0, len(elements))
			for _, elem := range elements {
				result = append(result, convertAttrValueToInterface(elem))
			}
			return result
		}
	case types.Map:
		if !v.IsNull() && !v.IsUnknown() {
			elements := v.Elements()
			result := make(map[string]interface{})
			for key, elem := range elements {
				result[key] = convertAttrValueToInterface(elem)
			}
			return result
		}
	case types.Object:
		if !v.IsNull() && !v.IsUnknown() {
			attrs := v.Attributes()
			result := make(map[string]interface{})
			for key, attr := range attrs {
				result[key] = convertAttrValueToInterface(attr)
			}
			return result
		}
	case types.Dynamic:
		if !v.IsNull() && !v.IsUnknown() {
			return convertAttrValueToInterface(v.UnderlyingValue())
		}
	}
	return nil
}

// InterfaceToDynamic converts a map[string]interface{} from the API
// to a types.Dynamic value for Terraform state.
func InterfaceToDynamic(ctx context.Context, input map[string]interface{}) (types.Dynamic, error) {
	if len(input) == 0 {
		return types.DynamicNull(), nil
	}

	// Convert the map to a types.Object with Dynamic-typed attributes
	attrs := make(map[string]attr.Value)
	attrTypes := make(map[string]attr.Type)

	for key, value := range input {
		attrValue, attrType := interfaceToAttrValue(value)
		attrs[key] = attrValue
		attrTypes[key] = attrType
	}

	objValue, diags := types.ObjectValue(attrTypes, attrs)
	if diags.HasError() {
		return types.DynamicNull(), fmt.Errorf("failed to create object: %v", diags)
	}

	return types.DynamicValue(objValue), nil
}

// interfaceToAttrValue converts an interface{} value to an attr.Value
func interfaceToAttrValue(value interface{}) (attr.Value, attr.Type) {
	if value == nil {
		return types.StringNull(), types.StringType
	}

	switch v := value.(type) {
	case string:
		return types.StringValue(v), types.StringType
	case float64:
		// Check if it's actually an integer
		if v == float64(int64(v)) {
			return types.NumberValue(big.NewFloat(v)), types.NumberType
		}
		return types.NumberValue(big.NewFloat(v)), types.NumberType
	case int:
		return types.NumberValue(big.NewFloat(float64(v))), types.NumberType
	case int64:
		return types.NumberValue(big.NewFloat(float64(v))), types.NumberType
	case bool:
		return types.BoolValue(v), types.BoolType
	case []interface{}:
		elements := make([]attr.Value, 0, len(v))
		var elemType attr.Type = types.StringType // Default
		for _, elem := range v {
			elemValue, et := interfaceToAttrValue(elem)
			elements = append(elements, elemValue)
			elemType = et // Use the type of the last element
		}
		listValue, _ := types.ListValue(elemType, elements)
		return listValue, types.ListType{ElemType: elemType}
	case map[string]interface{}:
		attrs := make(map[string]attr.Value)
		attrTypes := make(map[string]attr.Type)
		for key, val := range v {
			attrValue, attrType := interfaceToAttrValue(val)
			attrs[key] = attrValue
			attrTypes[key] = attrType
		}
		objValue, _ := types.ObjectValue(attrTypes, attrs)
		return objValue, types.ObjectType{AttrTypes: attrTypes}
	default:
		return types.StringValue(fmt.Sprintf("%v", value)), types.StringType
	}
}
