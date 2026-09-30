package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// Severity classifies one schema change. The gate fails only on Breaking.
type Severity int

const (
	Breaking Severity = iota
	NeedsReview
	NonBreaking
)

func (s Severity) String() string {
	switch s {
	case Breaking:
		return "breaking"
	case NeedsReview:
		return "needs review"
	default:
		return "non-breaking"
	}
}

// Rule identifies which classification rule produced a Change. Each breaking
// rule has its own fixture under testdata/ so disabling it fails a test.
type Rule string

const (
	// Breaking.
	RuleSchemaRemoved       Rule = "schema-removed"
	RuleAttributeRemoved    Rule = "attribute-removed"
	RuleBlockRemoved        Rule = "block-removed"
	RuleNewRequired         Rule = "new-required"
	RuleOptionalToRequired  Rule = "optional-to-required"
	RuleConfigurableRemoved Rule = "configurable-removed"
	RuleTypeChanged         Rule = "type-changed"
	RuleNestingChanged      Rule = "nesting-changed"
	RuleMinItemsRaised      Rule = "min-items-raised"
	RuleMaxItemsLowered     Rule = "max-items-lowered"
	RuleComputedRemoved     Rule = "computed-removed"
	RuleOptComputedToOpt    Rule = "optional-computed-to-optional"
	RuleSensitiveRemoved    Rule = "sensitive-removed"

	// Needs review.
	RuleVersionChanged   Rule = "version-changed"
	RuleDeprecatedAdded  Rule = "deprecated-added"
	RuleComputedAdded    Rule = "computed-added"
	RuleSensitiveAdded   Rule = "sensitive-added"
	RuleWriteOnlyChanged Rule = "write-only-changed"

	// Non-breaking.
	RuleSchemaAdded        Rule = "schema-added"
	RuleAttributeAdded     Rule = "attribute-added"
	RuleBlockAdded         Rule = "block-added"
	RuleRequiredRelaxed    Rule = "required-relaxed"
	RuleItemsRelaxed       Rule = "items-relaxed"
	RuleDeprecatedRemoved  Rule = "deprecated-removed"
	RuleDescriptionChanged Rule = "description-changed"
)

// Change is one classified schema difference. Subject names the schema
// ("resource/anyscale_cloud", "provider"); Message is the human-readable
// remainder, already carrying the dotted attribute/block path.
type Change struct {
	Severity Severity
	Rule     Rule
	Subject  string
	Message  string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %s", c.Subject, c.Message)
}

type differ struct {
	changes []Change
}

func (d *differ) add(sev Severity, rule Rule, subject, format string, args ...any) {
	d.changes = append(d.changes, Change{Severity: sev, Rule: rule, Subject: subject, Message: fmt.Sprintf(format, args...)})
}

// Diff compares one provider's schema between base and head. Both must be
// non-nil; the caller decides what a missing provider means.
func Diff(base, head *tfjson.ProviderSchema) []Change {
	d := &differ{}

	d.compareSchema("provider", wrapConfig(base.ConfigSchema), wrapConfig(head.ConfigSchema), true)

	d.compareSchemaMap("resource", base.ResourceSchemas, head.ResourceSchemas)
	d.compareSchemaMap("data-source", base.DataSourceSchemas, head.DataSourceSchemas)
	d.compareSchemaMap("ephemeral-resource", base.EphemeralResourceSchemas, head.EphemeralResourceSchemas)
	d.compareSchemaMap("list-resource", base.ListResourceSchemas, head.ListResourceSchemas)
	d.compareSchemaMap("action", actionsAsSchemas(base.ActionSchemas), actionsAsSchemas(head.ActionSchemas))
	d.compareFunctions(base.Functions, head.Functions)

	return d.changes
}

func wrapConfig(s *tfjson.Schema) *tfjson.Schema {
	if s == nil {
		return &tfjson.Schema{}
	}
	return s
}

// actionsAsSchemas adapts action schemas (a block with no version) to the
// generic Schema shape so they share the resource comparison.
func actionsAsSchemas(in map[string]*tfjson.ActionSchema) map[string]*tfjson.Schema {
	if in == nil {
		return nil
	}
	out := make(map[string]*tfjson.Schema, len(in))
	for k, v := range in {
		s := &tfjson.Schema{}
		if v != nil {
			s.Block = v.Block
		}
		out[k] = s
	}
	return out
}

func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func (d *differ) compareSchemaMap(kind string, base, head map[string]*tfjson.Schema) {
	for _, name := range sortedKeys(base, head) {
		subject := kind + "/" + name
		b, inBase := base[name]
		h, inHead := head[name]
		switch {
		case inBase && !inHead:
			d.add(Breaking, RuleSchemaRemoved, subject, "%s removed", kind)
		case !inBase && inHead:
			d.add(NonBreaking, RuleSchemaAdded, subject, "new %s", kind)
		default:
			d.compareSchema(subject, wrapConfig(b), wrapConfig(h), false)
		}
	}
}

func (d *differ) compareSchema(subject string, base, head *tfjson.Schema, isProvider bool) {
	if !isProvider && base.Version != head.Version {
		d.add(NeedsReview, RuleVersionChanged, subject,
			"schema version changed from %d to %d; confirm a state upgrader handles every prior version", base.Version, head.Version)
	}
	d.compareBlock(subject, "", blockOrEmpty(base.Block), blockOrEmpty(head.Block))
}

func blockOrEmpty(b *tfjson.SchemaBlock) *tfjson.SchemaBlock {
	if b == nil {
		return &tfjson.SchemaBlock{}
	}
	return b
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// where names the location of a block-level change: the dotted block path,
// or "schema" for the root block.
func where(path string) string {
	if path == "" {
		return "schema"
	}
	return "block " + path
}

func (d *differ) compareBlock(subject, path string, base, head *tfjson.SchemaBlock) {
	if !base.Deprecated && head.Deprecated {
		d.add(NeedsReview, RuleDeprecatedAdded, subject, "%s deprecated", where(path))
	} else if base.Deprecated && !head.Deprecated {
		d.add(NonBreaking, RuleDeprecatedRemoved, subject, "%s no longer deprecated", where(path))
	}
	if base.Description != head.Description || base.DescriptionKind != head.DescriptionKind {
		d.add(NonBreaking, RuleDescriptionChanged, subject, "%s description changed", where(path))
	}

	for _, name := range sortedKeys(base.Attributes, head.Attributes, attrKeysOf(base.NestedBlocks), attrKeysOf(head.NestedBlocks)) {
		p := join(path, name)
		bA, bIsAttr := base.Attributes[name]
		hA, hIsAttr := head.Attributes[name]
		bB, bIsBlock := base.NestedBlocks[name]
		hB, hIsBlock := head.NestedBlocks[name]
		inBase := bIsAttr || bIsBlock
		inHead := hIsAttr || hIsBlock

		switch {
		case bIsAttr && hIsAttr:
			d.compareAttribute(subject, p, bA, hA)
		case bIsBlock && hIsBlock:
			d.compareBlockType(subject, p, bB, hB)
		case bIsAttr && hIsBlock:
			d.add(Breaking, RuleNestingChanged, subject, "%s changed from an attribute to a block", p)
		case bIsBlock && hIsAttr:
			d.add(Breaking, RuleNestingChanged, subject, "%s changed from a block to an attribute", p)
		case inBase && !inHead && bIsAttr:
			d.add(Breaking, RuleAttributeRemoved, subject, "attribute %s removed", p)
		case inBase && !inHead:
			d.add(Breaking, RuleBlockRemoved, subject, "block %s removed", p)
		case hIsAttr:
			d.attributeAdded(subject, p, hA)
		default:
			if hB.MinItems > 0 {
				d.add(Breaking, RuleNewRequired, subject, "new required block %s (min_items %d)", p, hB.MinItems)
			} else {
				d.add(NonBreaking, RuleBlockAdded, subject, "new optional block %s", p)
			}
		}
	}
}

// attrKeysOf returns a map with the same keys as blocks, typed so sortedKeys
// can union it with attribute maps.
func attrKeysOf(blocks map[string]*tfjson.SchemaBlockType) map[string]*tfjson.SchemaAttribute {
	out := make(map[string]*tfjson.SchemaAttribute, len(blocks))
	for k := range blocks {
		out[k] = nil
	}
	return out
}

func (d *differ) attributeAdded(subject, path string, a *tfjson.SchemaAttribute) {
	if a.Required {
		d.add(Breaking, RuleNewRequired, subject, "new required attribute %s", path)
		return
	}
	d.add(NonBreaking, RuleAttributeAdded, subject, "new %s attribute %s", modeString(a), path)
}

func modeString(a *tfjson.SchemaAttribute) string {
	switch {
	case a.Required:
		return "required"
	case a.Optional && a.Computed:
		return "optional+computed"
	case a.Optional:
		return "optional"
	case a.Computed:
		return "computed"
	default:
		return "unset-mode"
	}
}

func configurable(a *tfjson.SchemaAttribute) bool { return a.Required || a.Optional }

func (d *differ) compareAttribute(subject, path string, b, h *tfjson.SchemaAttribute) {
	// Mode (required / optional / computed).
	if !b.Required && h.Required {
		d.add(Breaking, RuleOptionalToRequired, subject, "attribute %s changed from %s to required", path, modeString(b))
	}
	if configurable(b) && !configurable(h) {
		d.add(Breaking, RuleConfigurableRemoved, subject, "attribute %s changed from %s to %s; configs that set it are rejected", path, modeString(b), modeString(h))
	}
	if b.Computed && !b.Optional && !b.Required && !h.Computed {
		d.add(Breaking, RuleComputedRemoved, subject, "attribute %s is no longer computed (was computed-only)", path)
	}
	if b.Optional && b.Computed && h.Optional && !h.Computed {
		d.add(Breaking, RuleOptComputedToOpt, subject, "attribute %s changed from optional+computed to optional; server-populated values now diff", path)
	}
	if b.Required && !h.Required && configurable(h) {
		d.add(NonBreaking, RuleRequiredRelaxed, subject, "attribute %s changed from required to %s", path, modeString(h))
	}
	if b.Optional && !b.Computed && h.Optional && h.Computed {
		d.add(NeedsReview, RuleComputedAdded, subject, "attribute %s changed from optional to optional+computed", path)
	}

	// Flags.
	if b.Sensitive && !h.Sensitive {
		d.add(Breaking, RuleSensitiveRemoved, subject, "attribute %s is no longer sensitive", path)
	} else if !b.Sensitive && h.Sensitive {
		d.add(NeedsReview, RuleSensitiveAdded, subject, "attribute %s is now sensitive; outputs that reference it must be marked sensitive", path)
	}
	if b.WriteOnly != h.WriteOnly {
		d.add(NeedsReview, RuleWriteOnlyChanged, subject, "attribute %s write_only changed from %t to %t", path, b.WriteOnly, h.WriteOnly)
	}
	if !b.Deprecated && h.Deprecated {
		d.add(NeedsReview, RuleDeprecatedAdded, subject, "attribute %s deprecated", path)
	} else if b.Deprecated && !h.Deprecated {
		d.add(NonBreaking, RuleDeprecatedRemoved, subject, "attribute %s no longer deprecated", path)
	}
	if b.Description != h.Description || b.DescriptionKind != h.DescriptionKind {
		d.add(NonBreaking, RuleDescriptionChanged, subject, "attribute %s description changed", path)
	}

	// Type.
	bn, hn := b.AttributeNestedType, h.AttributeNestedType
	switch {
	case bn == nil && hn == nil:
		if !b.AttributeType.Equals(h.AttributeType) {
			d.add(Breaking, RuleTypeChanged, subject, "attribute %s type changed from %s to %s", path, typeString(b), typeString(h))
		}
	case bn == nil || hn == nil:
		d.add(Breaking, RuleTypeChanged, subject, "attribute %s type changed from %s to %s", path, typeString(b), typeString(h))
	default:
		if bn.NestingMode != hn.NestingMode {
			d.add(Breaking, RuleNestingChanged, subject, "attribute %s nesting changed from %s to %s", path, bn.NestingMode, hn.NestingMode)
		}
		d.compareNestedAttributes(subject, path, bn.Attributes, hn.Attributes)
	}
}

func typeString(a *tfjson.SchemaAttribute) string {
	if a.AttributeNestedType != nil {
		return fmt.Sprintf("nested %s attribute", a.AttributeNestedType.NestingMode)
	}
	return ctyString(a.AttributeType)
}

func (d *differ) compareNestedAttributes(subject, path string, base, head map[string]*tfjson.SchemaAttribute) {
	for _, name := range sortedKeys(base, head) {
		p := join(path, name)
		b, inBase := base[name]
		h, inHead := head[name]
		switch {
		case inBase && inHead:
			d.compareAttribute(subject, p, b, h)
		case inBase:
			d.add(Breaking, RuleAttributeRemoved, subject, "attribute %s removed", p)
		default:
			d.attributeAdded(subject, p, h)
		}
	}
}

func (d *differ) compareBlockType(subject, path string, b, h *tfjson.SchemaBlockType) {
	if b.NestingMode != h.NestingMode {
		d.add(Breaking, RuleNestingChanged, subject, "block %s nesting changed from %s to %s", path, b.NestingMode, h.NestingMode)
	}
	if h.MinItems > b.MinItems {
		d.add(Breaking, RuleMinItemsRaised, subject, "block %s min_items raised from %d to %d", path, b.MinItems, h.MinItems)
	} else if h.MinItems < b.MinItems {
		d.add(NonBreaking, RuleItemsRelaxed, subject, "block %s min_items lowered from %d to %d", path, b.MinItems, h.MinItems)
	}
	// max_items 0 means unlimited.
	switch {
	case h.MaxItems != 0 && (b.MaxItems == 0 || h.MaxItems < b.MaxItems):
		d.add(Breaking, RuleMaxItemsLowered, subject, "block %s max_items lowered from %s to %d", path, maxString(b.MaxItems), h.MaxItems)
	case h.MaxItems != b.MaxItems:
		d.add(NonBreaking, RuleItemsRelaxed, subject, "block %s max_items raised from %d to %s", path, b.MaxItems, maxString(h.MaxItems))
	}
	d.compareBlock(subject, path, blockOrEmpty(b.Block), blockOrEmpty(h.Block))
}

func maxString(n uint64) string {
	if n == 0 {
		return "unlimited"
	}
	return fmt.Sprint(n)
}

func (d *differ) compareFunctions(base, head map[string]*tfjson.FunctionSignature) {
	for _, name := range sortedKeys(base, head) {
		subject := "function/" + name
		b, inBase := base[name]
		h, inHead := head[name]
		switch {
		case inBase && !inHead:
			d.add(Breaking, RuleSchemaRemoved, subject, "function removed")
			continue
		case !inBase:
			d.add(NonBreaking, RuleSchemaAdded, subject, "new function")
			continue
		}
		if !b.ReturnType.Equals(h.ReturnType) {
			d.add(Breaking, RuleTypeChanged, subject, "return type changed from %s to %s", ctyString(b.ReturnType), ctyString(h.ReturnType))
		}
		if sig(b) != sig(h) {
			d.add(Breaking, RuleTypeChanged, subject, "parameters changed from (%s) to (%s)", sig(b), sig(h))
		}
		if b.DeprecationMessage == "" && h.DeprecationMessage != "" {
			d.add(NeedsReview, RuleDeprecatedAdded, subject, "function deprecated")
		} else if b.DeprecationMessage != "" && h.DeprecationMessage == "" {
			d.add(NonBreaking, RuleDeprecatedRemoved, subject, "function no longer deprecated")
		}
		if b.Description != h.Description || b.Summary != h.Summary {
			d.add(NonBreaking, RuleDescriptionChanged, subject, "function description changed")
		}
	}
}

// sig renders a function's parameter types positionally (names are not part
// of the calling contract) plus any variadic parameter.
func sig(f *tfjson.FunctionSignature) string {
	var parts []string
	for _, p := range f.Parameters {
		parts = append(parts, paramString(p))
	}
	if f.VariadicParameter != nil {
		parts = append(parts, "..."+paramString(f.VariadicParameter))
	}
	return strings.Join(parts, ", ")
}

func paramString(p *tfjson.FunctionParameter) string {
	s := ctyString(p.Type)
	if p.IsNullable {
		s += "?"
	}
	return s
}

func ctyString(t any) string {
	b, err := json.Marshal(t)
	if err != nil {
		return "<unprintable type>"
	}
	return strings.ReplaceAll(string(b), `"`, "")
}
