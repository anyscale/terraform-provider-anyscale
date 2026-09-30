package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func loadPair(t *testing.T, name string) (*tfjson.ProviderSchema, *tfjson.ProviderSchema) {
	t.Helper()
	dir := filepath.Join("testdata", name)
	b, err := loadProvider(filepath.Join(dir, "base.json"), defaultProvider)
	if err != nil {
		t.Fatalf("loading base: %v", err)
	}
	h, err := loadProvider(filepath.Join(dir, "head.json"), defaultProvider)
	if err != nil {
		t.Fatalf("loading head: %v", err)
	}
	return b, h
}

// want is one expected change: severity, rule, and its rendered
// "subject: message" line.
type want struct {
	sev  Severity
	rule Rule
	line string
}

// TestDiff_Fixtures runs every testdata pair and asserts the exact set of
// changes. Each breaking rule has at least one pair whose only breaking
// change comes from that rule, so disabling the rule fails its case.
func TestDiff_Fixtures(t *testing.T) {
	const r = "resource/anyscale_widget"
	cases := map[string][]want{
		"identical": nil,

		// Breaking.
		"breaking_resource_removed":                {{Breaking, RuleSchemaRemoved, r + ": resource removed"}},
		"breaking_data_source_removed":             {{Breaking, RuleSchemaRemoved, "data-source/anyscale_widget: data-source removed"}},
		"breaking_ephemeral_resource_removed":      {{Breaking, RuleSchemaRemoved, "ephemeral-resource/anyscale_widget_token: ephemeral-resource removed"}},
		"breaking_resource_renamed":                {{NonBreaking, RuleSchemaAdded, "resource/anyscale_gadget: new resource"}, {Breaking, RuleSchemaRemoved, r + ": resource removed"}},
		"breaking_attribute_removed":               {{Breaking, RuleAttributeRemoved, r + ": attribute size removed"}},
		"breaking_nested_attribute_removed":        {{Breaking, RuleAttributeRemoved, r + ": attribute settings.mode removed"}},
		"breaking_block_attribute_removed":         {{Breaking, RuleAttributeRemoved, r + ": attribute network.subnet_ids removed"}},
		"breaking_block_removed":                   {{Breaking, RuleBlockRemoved, r + ": block network removed"}},
		"breaking_new_required_attribute":          {{Breaking, RuleNewRequired, r + ": new required attribute owner"}},
		"breaking_new_required_provider_attribute": {{Breaking, RuleNewRequired, "provider: new required attribute org"}},
		"breaking_new_required_block":              {{Breaking, RuleNewRequired, r + ": new required block placement (min_items 1)"}},
		"breaking_optional_to_required":            {{Breaking, RuleOptionalToRequired, r + ": attribute size changed from optional to required"}},
		"breaking_configurable_removed":            {{Breaking, RuleConfigurableRemoved, r + ": attribute size changed from optional to computed; configs that set it are rejected"}},
		"breaking_type_changed":                    {{Breaking, RuleTypeChanged, r + ": attribute size type changed from string to number"}},
		"breaking_element_type_changed":            {{Breaking, RuleTypeChanged, r + ": attribute tags type changed from [list,string] to [list,number]"}},
		"breaking_type_to_nested_attribute":        {{Breaking, RuleTypeChanged, r + ": attribute tags type changed from [list,string] to nested list attribute"}},
		"breaking_block_nesting_changed":           {{Breaking, RuleNestingChanged, r + ": block network nesting changed from list to set"}},
		"breaking_attribute_nesting_changed":       {{Breaking, RuleNestingChanged, r + ": attribute settings nesting changed from single to list"}},
		"breaking_block_to_attribute":              {{Breaking, RuleNestingChanged, r + ": network changed from a block to an attribute"}},
		"breaking_attribute_to_block":              {{Breaking, RuleNestingChanged, r + ": settings changed from an attribute to a block"}},
		"breaking_min_items_raised":                {{Breaking, RuleMinItemsRaised, r + ": block network min_items raised from 0 to 1"}},
		"breaking_max_items_lowered":               {{Breaking, RuleMaxItemsLowered, r + ": block network max_items lowered from 2 to 1"}},
		"breaking_computed_removed":                {{Breaking, RuleComputedRemoved, r + ": attribute id is no longer computed (was computed-only)"}},
		"breaking_optional_computed_to_optional":   {{Breaking, RuleOptComputedToOpt, r + ": attribute region changed from optional+computed to optional; server-populated values now diff"}},
		"breaking_sensitive_removed":               {{Breaking, RuleSensitiveRemoved, r + ": attribute secret is no longer sensitive"}},

		// Needs review.
		"review_version_bump":      {{NeedsReview, RuleVersionChanged, r + ": schema version changed from 0 to 1; confirm a state upgrader handles every prior version"}},
		"review_deprecation_added": {{NeedsReview, RuleDeprecatedAdded, r + ": attribute size deprecated"}},
		"review_computed_added":    {{NeedsReview, RuleComputedAdded, r + ": attribute size changed from optional to optional+computed"}},
		"review_sensitive_added":   {{NeedsReview, RuleSensitiveAdded, r + ": attribute size is now sensitive; outputs that reference it must be marked sensitive"}},

		// Non-breaking.
		"nonbreaking_new_resource":           {{NonBreaking, RuleSchemaAdded, "resource/anyscale_gizmo: new resource"}},
		"nonbreaking_new_optional_attribute": {{NonBreaking, RuleAttributeAdded, r + ": new optional attribute color"}},
		"nonbreaking_new_computed_attribute": {{NonBreaking, RuleAttributeAdded, r + ": new computed attribute created_at"}},
		"nonbreaking_new_optional_block":     {{NonBreaking, RuleBlockAdded, r + ": new optional block placement"}},
		"nonbreaking_description_changed": {
			{NonBreaking, RuleDescriptionChanged, r + ": schema description changed"},
			{NonBreaking, RuleDescriptionChanged, r + ": attribute size description changed"},
		},
	}

	dirs, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if _, ok := cases[d.Name()]; !ok {
			t.Errorf("testdata/%s has no expectation in this table", d.Name())
		}
	}

	for name, expected := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := loadPair(t, name)
			got := normalize(Diff(base, head))
			exp := make([]want, len(expected))
			copy(exp, expected)
			sortWants(exp)
			if !reflect.DeepEqual(got, exp) {
				t.Errorf("changes mismatch\n got: %v\nwant: %v", got, exp)
			}
		})
	}
}

func normalize(changes []Change) []want {
	out := make([]want, 0, len(changes))
	for _, c := range changes {
		out = append(out, want{c.Severity, c.Rule, c.String()})
	}
	sortWants(out)
	return out
}

func sortWants(ws []want) {
	sort.Slice(ws, func(i, j int) bool {
		if ws[i].sev != ws[j].sev {
			return ws[i].sev < ws[j].sev
		}
		return ws[i].line < ws[j].line
	})
}

func TestDiff_ActionsAndFunctions(t *testing.T) {
	base := &tfjson.ProviderSchema{
		ActionSchemas: map[string]*tfjson.ActionSchema{"anyscale_do": {Block: &tfjson.SchemaBlock{}}},
		Functions:     map[string]*tfjson.FunctionSignature{"parse": {}, "gone": {}},
	}
	head := &tfjson.ProviderSchema{
		Functions: map[string]*tfjson.FunctionSignature{"parse": {DeprecationMessage: "use x"}},
	}
	got := normalize(Diff(base, head))
	exp := []want{
		{Breaking, RuleSchemaRemoved, "action/anyscale_do: action removed"},
		{Breaking, RuleSchemaRemoved, "function/gone: function removed"},
		{NeedsReview, RuleDeprecatedAdded, "function/parse: function deprecated"},
	}
	sortWants(exp)
	if !reflect.DeepEqual(got, exp) {
		t.Errorf("changes mismatch\n got: %v\nwant: %v", got, exp)
	}
}

func TestRun_ExitCodesAndSummary(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		wantCode int
		contains []string
	}{
		{"identical", "identical", exitClean, []string{"no schema changes"}},
		{"breaking", "breaking_block_attribute_removed", exitBreaking, []string{
			"**1 breaking**", "### Breaking",
			"- `resource/anyscale_widget`: attribute network.subnet_ids removed (`attribute-removed`)",
		}},
		{"review passes", "review_version_bump", exitClean, []string{"### Needs review"}},
		{"non-breaking passes", "nonbreaking_new_optional_attribute", exitClean, []string{"Non-breaking (1)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := filepath.Join(t.TempDir(), "summary.md")
			var stdout, stderr bytes.Buffer
			code := run([]string{
				"-base", filepath.Join("testdata", tc.dir, "base.json"),
				"-head", filepath.Join("testdata", tc.dir, "head.json"),
				"-summary", summary,
			}, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr: %s)", code, tc.wantCode, stderr.String())
			}
			written, err := os.ReadFile(summary)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != stdout.String() {
				t.Errorf("summary file differs from stdout")
			}
			for _, s := range tc.contains {
				if !strings.Contains(stdout.String(), s) {
					t.Errorf("output missing %q:\n%s", s, stdout.String())
				}
			}
			if !strings.Contains(stdout.String(), "RequiresReplace") {
				t.Errorf("output missing the plan-modifier limitation note")
			}
		})
	}
}

func TestRun_InputErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	noVersion := filepath.Join(dir, "noversion.json")
	otherProvider := filepath.Join(dir, "other.json")
	for path, content := range map[string]string{
		bad:           "{not json",
		noVersion:     `{"provider_schemas":{}}`,
		otherProvider: `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/null":{}}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	good := filepath.Join("testdata", "identical", "base.json")
	for name, args := range map[string][]string{
		"missing flags":   {"-base", good},
		"missing file":    {"-base", filepath.Join(dir, "nope.json"), "-head", good},
		"invalid json":    {"-base", good, "-head", bad},
		"no format":       {"-base", noVersion, "-head", good},
		"provider absent": {"-base", good, "-head", otherProvider},
		"unknown flag":    {"-nope"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != exitInput {
				t.Errorf("exit %d, want %d", code, exitInput)
			}
		})
	}
}
