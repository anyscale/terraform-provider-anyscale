package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFragmentContent_BareFence(t *testing.T) {
	content := "```\nrelease-note:added\nresource/anyscale_cloud: Add support for GCP Filestore in the file_storage block.\n```\n"
	entries, err := parseFragmentContent("42.txt", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Type != TypeAdded {
		t.Errorf("got type %q, want %q", entries[0].Type, TypeAdded)
	}
	want := "resource/anyscale_cloud: Add support for GCP Filestore in the file_storage block."
	if entries[0].Text != want {
		t.Errorf("got text %q, want %q", entries[0].Text, want)
	}
}

func TestParseFragmentContent_InfoStringFence(t *testing.T) {
	content := "```release-note:breaking-change\nresource/anyscale_cloud: `foo` now requires `bar`; to migrate, set `bar` explicitly.\n```\n"
	entries, err := parseFragmentContent("7.txt", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != TypeBreakingChange {
		t.Fatalf("got %+v, want one breaking-change entry", entries)
	}
}

func TestParseFragmentContent_MultipleBlocksPerFile(t *testing.T) {
	content := "```\nrelease-note:new-resource\nresource/anyscale_service: Manage Anyscale Services.\n```\n\n```\nrelease-note:fixed\nresource/anyscale_cloud: Fix apply drift on compute_stack.\n```\n"
	entries, err := parseFragmentContent("9.txt", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Type != TypeNewResource || entries[1].Type != TypeFixed {
		t.Fatalf("got types %q, %q", entries[0].Type, entries[1].Type)
	}
}

func TestParseFragmentContent_UnknownTypeErrors(t *testing.T) {
	content := "```\nrelease-note:enhancement\nsomething\n```\n"
	if _, err := parseFragmentContent("1.txt", content); err == nil {
		t.Fatal("expected an error for an unrecognized type, got nil")
	}
}

func TestParseFragmentContent_EmptyBodyErrors(t *testing.T) {
	content := "```\nrelease-note:added\n```\n"
	if _, err := parseFragmentContent("1.txt", content); err == nil {
		t.Fatal("expected an error for an empty body, got nil")
	}
}

func TestParseFragmentContent_UnterminatedFenceErrors(t *testing.T) {
	content := "```\nrelease-note:added\nsomething"
	if _, err := parseFragmentContent("1.txt", content); err == nil {
		t.Fatal("expected an error for an unterminated fence, got nil")
	}
}

func TestRenderSection_OrderAndGrouping(t *testing.T) {
	entries := []Entry{
		{Type: TypeFixed, Text: "fix one"},
		{Type: TypeBreakingChange, Text: "break one"},
		{Type: TypeAdded, Text: "add one"},
		{Type: TypeAdded, Text: "add two"},
	}
	got := RenderSection(entries)
	want := "### Breaking Changes\n\n- break one\n\n### Added\n\n- add one\n- add two\n\n### Fixed\n\n- fix one\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderSection_Empty(t *testing.T) {
	if got := RenderSection(nil); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestRenderSection_Idempotent(t *testing.T) {
	entries := []Entry{
		{Type: TypeAdded, Text: "add one"},
		{Type: TypeSecurity, Text: "sec one"},
	}
	first := RenderSection(entries)
	second := RenderSection(entries)
	if first != second {
		t.Errorf("RenderSection is not deterministic:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestFold_InsertsBetweenUnreleasedAndNextHeading(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	got, err := Fold(changelog, "### Added\n\n- new thing\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFold_EmptyBodyLeavesCleanUnreleased(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	got, err := Fold(changelog, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFold_NoNextHeadingReplacesToEOF(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\nstale text\n"
	got, err := Fold(changelog, "### Added\n\n- new thing\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFold_MissingMarkerErrors(t *testing.T) {
	if _, err := Fold("# Changelog\n\nno marker here\n", "x"); err == nil {
		t.Fatal("expected an error when ## [Unreleased] is missing, got nil")
	}
}

func TestFold_RunningTwiceWithSameInputIsIdempotent(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	body := "### Added\n\n- new thing\n"
	first, err := Fold(changelog, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := Fold(changelog, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first != second {
		t.Errorf("Fold is not idempotent on identical input:\nfirst:\n%q\nsecond:\n%q", first, second)
	}
}

func TestFinalize_RenamesUnreleasedAndInsertsFreshOne(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	newChangelog, notes, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantChangelog := "# Changelog\n\n## [Unreleased]\n\n## [0.2.0] - 2026-08-01\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	if newChangelog != wantChangelog {
		t.Errorf("changelog:\ngot:\n%q\nwant:\n%q", newChangelog, wantChangelog)
	}
	wantNotes := "### Added\n\n- new thing"
	if notes != wantNotes {
		t.Errorf("notes:\ngot:\n%q\nwant:\n%q", notes, wantNotes)
	}
}

func TestFinalize_ReleaseNotesMatchChangelogSection(t *testing.T) {
	// Acceptance criterion 5: GitHub Release body must byte-match the
	// CHANGELOG.md section for that version once rendered back with the heading.
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- a bug\n"
	newChangelog, notes, err := Finalize(changelog, "1.2.3", "2026-01-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	heading := "## [1.2.3] - 2026-01-01"
	if !strings.Contains(newChangelog, heading+"\n\n"+notes) {
		t.Errorf("changelog section for %s does not byte-match the returned release notes.\nchangelog:\n%q\nnotes:\n%q", heading, newChangelog, notes)
	}
}

func TestExtract_ReturnsSectionBodyUnmodified(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [0.2.0] - 2026-08-01\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	got, err := Extract(changelog, "0.2.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "### Added\n\n- new thing"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtract_LastSectionInFile(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [0.1.1] - 2026-07-06\n\n### Fixed\n\n- a bug\n"
	got, err := Extract(changelog, "0.1.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "### Fixed\n\n- a bug"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtract_MatchesFinalizeOutputForTheSameVersion(t *testing.T) {
	// This is the property release.yml depends on: what a fresh checkout
	// extracts for a tag must equal what `make tag` originally committed.
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- a bug\n\n## [0.1.1] - 2026-07-06\n\nold\n"
	finalized, notesFromFinalize, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	notesFromExtract, err := Extract(finalized, "0.2.0")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if notesFromFinalize != notesFromExtract {
		t.Errorf("Finalize notes and Extract notes diverge:\nfinalize:\n%q\nextract:\n%q", notesFromFinalize, notesFromExtract)
	}
}

func TestExtract_UnknownVersionErrors(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [0.1.1] - 2026-07-06\n\nstuff\n"
	if _, err := Extract(changelog, "9.9.9"); err == nil {
		t.Fatal("expected an error for a version with no heading, got nil")
	}
}

func TestParseFragments_EndToEndAndDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	writeFragment(t, dir, "100.txt", "```\nrelease-note:fixed\nfix from PR 100\n```\n")
	writeFragment(t, dir, "7.txt", "```\nrelease-note:fixed\nfix from PR 7\n```\n")
	writeFragment(t, dir, "42.txt", "```\nrelease-note:fixed\nfix from PR 42\n```\n")

	entries, err := ParseFragments(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	got := []string{entries[0].Text, entries[1].Text, entries[2].Text}
	want := []string{"fix from PR 7", "fix from PR 42", "fix from PR 100"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q, want %q (numeric PR order)", i, got[i], want[i])
		}
	}
}

func TestParseFragments_EmptyDirIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	entries, err := ParseFragments(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries, want 0", len(entries))
	}
}

func writeFragment(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fragment %s: %v", name, err)
	}
}
