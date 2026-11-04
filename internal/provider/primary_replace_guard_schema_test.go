package provider

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// Plan modifier types on anyscale_cloud_resource that never set RequiresReplace. Every other
// modifier must be wrapped in a primary-replace guard, so that a replace-forcing modifier
// added later cannot silently let a change replace a cloud's primary resource (and start
// deleting the cloud). A new modifier type fails this test until it is either wrapped or,
// if it never forces replacement, added here.
var cloudResourceNonReplacingModifierTypes = map[string]bool{
	"stringplanmodifier.useStateForUnknownModifier": true,
	"boolplanmodifier.useStateForUnknownModifier":   true,
	"provider.bucketNameSemanticEqualPlanModifier":  true,
	"provider.fileStorageDerivedListModifier":       true,
	"provider.fileStorageDerivedStringModifier":     true,
}

var cloudResourceGuardTypes = map[string]bool{
	"provider.primaryReplaceGuardString": true,
	"provider.primaryReplaceGuardBool":   true,
	"provider.primaryReplaceGuardList":   true,
	"provider.primaryReplaceGuardMap":    true,
}

func TestCloudResourceSchema_EveryReplaceModifierIsPrimaryGuarded(t *testing.T) {
	var resp resource.SchemaResponse
	NewCloudResourceResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}

	modifiers := map[string][]string{} // attribute path -> modifier types
	var walkAttrs func(prefix string, attrs map[string]schema.Attribute)
	walkAttrs = func(prefix string, attrs map[string]schema.Attribute) {
		for name, a := range attrs {
			p := prefix + name
			var types []string
			switch a := a.(type) {
			case schema.StringAttribute:
				for _, m := range a.PlanModifiers {
					types = append(types, fmt.Sprintf("%T", m))
				}
			case schema.BoolAttribute:
				for _, m := range a.PlanModifiers {
					types = append(types, fmt.Sprintf("%T", m))
				}
			case schema.ListAttribute:
				for _, m := range a.PlanModifiers {
					types = append(types, fmt.Sprintf("%T", m))
				}
			case schema.MapAttribute:
				for _, m := range a.PlanModifiers {
					types = append(types, fmt.Sprintf("%T", m))
				}
			case schema.ListNestedAttribute:
				for _, m := range a.PlanModifiers {
					types = append(types, fmt.Sprintf("%T", m))
				}
				walkAttrs(p+".", a.NestedObject.Attributes)
			case schema.Int64Attribute, schema.SetAttribute, schema.ObjectAttribute,
				schema.SingleNestedAttribute, schema.SetNestedAttribute,
				schema.MapNestedAttribute, schema.NumberAttribute, schema.Float64Attribute,
				schema.Int32Attribute, schema.Float32Attribute, schema.DynamicAttribute:
				t.Errorf("%s: attribute type %T is not covered by this test or by a guard wrapper; add both", p, a)
			}
			modifiers[p] = types
		}
	}
	walkAttrs("", resp.Schema.Attributes)
	for name, b := range resp.Schema.Blocks {
		switch b := b.(type) {
		case schema.SingleNestedBlock:
			walkAttrs(name+".", b.Attributes)
			if len(b.Blocks) > 0 {
				t.Errorf("%s: nested blocks inside blocks are not walked; extend this test", name)
			}
		case schema.ListNestedBlock:
			walkAttrs(name+".", b.NestedObject.Attributes)
		case schema.SetNestedBlock:
			walkAttrs(name+".", b.NestedObject.Attributes)
		default:
			// The timeouts block carries no plan modifiers.
		}
	}

	paths := make([]string, 0, len(modifiers))
	for p := range modifiers {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	guarded := 0
	for _, p := range paths {
		for _, typ := range modifiers[p] {
			switch {
			case cloudResourceGuardTypes[typ]:
				guarded++
			case cloudResourceNonReplacingModifierTypes[typ]:
			default:
				t.Errorf("%s: plan modifier %s is neither primary-guarded nor known never to force replacement; "+
					"wrap it with guardPrimary* or, if it never sets RequiresReplace, add it to cloudResourceNonReplacingModifierTypes", p, typ)
			}
		}
	}
	// Positive control: the walk must actually reach the wrapped modifiers.
	if guarded < 30 {
		t.Errorf("found %d guarded modifiers, want at least 30: the schema walk is not reaching them", guarded)
	}
}
