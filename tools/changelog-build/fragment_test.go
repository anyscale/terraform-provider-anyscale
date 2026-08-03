package main

import (
	"strings"
	"testing"
)

// TestParseFragmentContent_ValidComponentPrefixes covers every legal
// changelog component prefix documented in .changelog/README.md's "Examples
// by type" section: the bare "provider" token, and "<category>/<name>" for
// each of the four category words. Each case runs the real end-to-end path
// through parseFragmentContent (not the regex in isolation), since that's
// what changelog-gate CI actually exercises against a real fragment file.
func TestParseFragmentContent_ValidComponentPrefixes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"provider", "provider: Remove an unused internal request field from compute-config lookups."},
		{"resource", "resource/anyscale_service: Manage Anyscale Services."},
		{"data-source", "data-source/anyscale_project: Look up an existing Anyscale project by name or ID."},
		{"ephemeral-resource", "ephemeral-resource/anyscale_service_credentials: Fetch a running Service's live auth token without ever writing it to state."},
		{"action", "action/anyscale_system_cluster_terminate: Terminate a System Cluster's underlying compute imperatively."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "```\nrelease-note:added\n" + tc.body + "\n```\n"
			entries, err := parseFragmentContent("1.txt", content)
			if err != nil {
				t.Fatalf("unexpected error for body %q: %v", tc.body, err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			if entries[0].Text != tc.body {
				t.Errorf("got text %q, want %q", entries[0].Text, tc.body)
			}
		})
	}
}

// TestParseFragmentContent_InventedPrefixErrors guards the real near-miss
// that motivated this validation: a draft fragment once used an invented
// "docs/anyscale_organization_user:" prefix that is not one of the
// documented categories. It must be rejected at parse time — before it can
// pass changelog-gate CI and render wrong in CHANGELOG.md — not silently
// accepted.
func TestParseFragmentContent_InventedPrefixErrors(t *testing.T) {
	content := "```\nrelease-note:added\ndocs/anyscale_organization_user: Clarify collaborator import behavior in the docs.\n```\n"
	_, err := parseFragmentContent("846.txt", content)
	if err == nil {
		t.Fatal("expected an error for an invented component prefix, got nil")
	}
	if !strings.Contains(err.Error(), `"docs/anyscale_organization_user"`) {
		t.Errorf("error %q does not name the bad prefix", err.Error())
	}
	for _, want := range []string{"provider", "resource/<name>", "data-source/<name>", "ephemeral-resource/<name>", "action/<name>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not list valid prefix %q", err.Error(), want)
		}
	}
}

// TestParseFragmentContent_WrongCategoryShapeErrors covers prefixes that are
// shaped like a real category but don't match one exactly: a plural form and
// a wrong-cased form. Neither is an alias for the real category — both must
// be rejected.
func TestParseFragmentContent_WrongCategoryShapeErrors(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
	}{
		{"plural", "resources/anyscale_cloud"},
		{"wrong case", "Resource/anyscale_cloud"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "```\nrelease-note:added\n" + tc.prefix + ": something changed.\n```\n"
			_, err := parseFragmentContent("1.txt", content)
			if err == nil {
				t.Fatalf("expected an error for component prefix %q, got nil", tc.prefix)
			}
			if !strings.Contains(err.Error(), tc.prefix) {
				t.Errorf("error %q does not name the bad prefix %q", err.Error(), tc.prefix)
			}
		})
	}
}

// TestParseFragmentContent_NoColonPrefixErrors covers a body with no ": " at
// all — the "lack of one" case the error message is designed to still
// surface usefully (the whole body stands in as the reported prefix).
func TestParseFragmentContent_NoColonPrefixErrors(t *testing.T) {
	content := "```\nrelease-note:fixed\nFix a bug with no component prefix at all\n```\n"
	_, err := parseFragmentContent("1.txt", content)
	if err == nil {
		t.Fatal("expected an error for a body with no component prefix, got nil")
	}
	if !strings.Contains(err.Error(), "Fix a bug with no component prefix at all") {
		t.Errorf("error %q does not surface the body as the found prefix", err.Error())
	}
}
