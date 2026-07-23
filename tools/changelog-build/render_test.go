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

func TestParseFragmentContent_NewEphemeralResourceAndNewActionTypes(t *testing.T) {
	content := "```\nrelease-note:new-ephemeral-resource\nephemeral-resource/anyscale_service_credentials: Fetch a running Service's live auth token without ever writing it to state.\n```\n\n```\nrelease-note:new-action\naction/anyscale_system_cluster_terminate: Terminate a System Cluster's underlying compute imperatively.\n```\n"
	entries, err := parseFragmentContent("210.txt", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Type != TypeNewEphemeralResource || entries[1].Type != TypeNewAction {
		t.Fatalf("got types %q, %q, want %q, %q", entries[0].Type, entries[1].Type, TypeNewEphemeralResource, TypeNewAction)
	}
}

func TestParseFragmentContent_UnknownTypeErrors(t *testing.T) {
	content := "```\nrelease-note:enhancement\nsomething\n```\n"
	if _, err := parseFragmentContent("1.txt", content); err == nil {
		t.Fatal("expected an error for an unrecognized type, got nil")
	}
}

func TestParseFragmentContent_UnknownTypeErrorListsNewTypes(t *testing.T) {
	content := "```\nrelease-note:enhancement\nsomething\n```\n"
	_, err := parseFragmentContent("1.txt", content)
	if err == nil {
		t.Fatal("expected an error for an unrecognized type, got nil")
	}
	for _, want := range []string{"new-ephemeral-resource", "new-action"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention valid type %q — the error message must stay in sync with typeOrder", err.Error(), want)
		}
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

func TestRenderSection_NewEphemeralResourceAndNewActionHeadingsAndOrder(t *testing.T) {
	entries := []Entry{
		{Type: TypeAdded, Text: "add one"},
		{Type: TypeNewAction, Text: "action one"},
		{Type: TypeNewResource, Text: "resource one"},
		{Type: TypeNewEphemeralResource, Text: "ephemeral one"},
	}
	got := RenderSection(entries)
	want := "### New Resources\n\n- resource one\n\n### New Ephemeral Resources\n\n- ephemeral one\n\n### New Actions\n\n- action one\n\n### Added\n\n- add one\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
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

func TestFold_MissingMarkerInsertsFreshOneBeforeNewestVersion(t *testing.T) {
	// Finalize no longer leaves a standing empty ## [Unreleased] behind, so this
	// is the normal state of CHANGELOG.md between releases. A real fragment to
	// fold must still produce a correct, freshly-created Unreleased section,
	// placed immediately above the newest version heading.
	changelog := "# Changelog\n\nintro\n\n## [0.2.0] - 2026-08-01\n\nstuff\n"
	got, err := Fold(changelog, "### Added\n\n- new thing\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "# Changelog\n\nintro\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [0.2.0] - 2026-08-01\n\nstuff\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFold_MissingMarkerAndEmptyBodyIsANoOp(t *testing.T) {
	// No Unreleased heading, and nothing to fold: don't manufacture an empty
	// heading just to have one - that's the exact cosmetic problem this design
	// avoids. The changelog is returned byte-for-byte unchanged.
	changelog := "# Changelog\n\n## [0.2.0] - 2026-08-01\n\nstuff\n"
	got, err := Fold(changelog, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != changelog {
		t.Errorf("expected no-op, got:\n%q\nwant:\n%q", got, changelog)
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

func TestFinalize_RenamesUnreleasedInPlaceWithNoFreshOne(t *testing.T) {
	// The changelog must lead with the newest real version - no empty
	// ## [Unreleased] placeholder left standing above it. Fold is what brings
	// Unreleased back, the next time there is a real fragment to fold in.
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	newChangelog, notes, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantChangelog := "# Changelog\n\n## [0.2.0] - 2026-08-01\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	if newChangelog != wantChangelog {
		t.Errorf("changelog:\ngot:\n%q\nwant:\n%q", newChangelog, wantChangelog)
	}
	if strings.Contains(newChangelog, unreleasedMarker) {
		t.Errorf("no ## [Unreleased] heading should remain after finalize:\n%s", newChangelog)
	}
	wantNotes := "### Added\n\n- new thing"
	if notes != wantNotes {
		t.Errorf("notes:\ngot:\n%q\nwant:\n%q", notes, wantNotes)
	}
}

func TestFinalize_UpdatesFooterLinks(t *testing.T) {
	// Real anyscale footer, drifted exactly as CHANGELOG.md is today: [Unreleased]
	// still compares from v0.1.1 and there's no [0.2.0] tag link yet.
	changelog := "# Changelog\n\n" +
		"## [Unreleased]\n\n### Added\n\n- new thing\n\n" +
		"## [0.1.1] - 2026-07-06\n\nold content\n\n" +
		"[Unreleased]: https://github.com/anyscale/terraform-provider-anyscale/compare/v0.1.1...HEAD\n" +
		"[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1\n" +
		"[0.0.1-dev]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.0.1-dev\n"

	newChangelog, _, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// [Unreleased] must now compare FROM the new version, and the old compare
	// base (v0.1.1) must be gone from the Unreleased line.
	wantUnreleased := "[Unreleased]: https://github.com/anyscale/terraform-provider-anyscale/compare/v0.2.0...HEAD"
	if !strings.Contains(newChangelog, wantUnreleased+"\n") {
		t.Errorf("expected rewritten Unreleased compare line %q, got changelog:\n%s", wantUnreleased, newChangelog)
	}
	if strings.Contains(newChangelog, "compare/v0.1.1...HEAD") {
		t.Errorf("stale compare base v0.1.1 should no longer appear on the Unreleased line:\n%s", newChangelog)
	}

	// A new [0.2.0] tag link must be inserted immediately below [Unreleased]
	// (newest-first), and the previous version links must be preserved.
	wantTag020 := "[0.2.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.2.0"
	if !strings.Contains(newChangelog, wantTag020+"\n") {
		t.Errorf("expected new tag link %q in footer, got:\n%s", wantTag020, newChangelog)
	}
	if !strings.Contains(newChangelog, "[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1\n") {
		t.Errorf("existing [0.1.1] link must be preserved:\n%s", newChangelog)
	}
	if !strings.Contains(newChangelog, "[0.0.1-dev]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.0.1-dev\n") {
		t.Errorf("existing [0.0.1-dev] link must be preserved:\n%s", newChangelog)
	}

	// Ordering: the whole footer block must be newest-first, exactly.
	wantFooter := wantUnreleased + "\n" +
		wantTag020 + "\n" +
		"[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1\n" +
		"[0.0.1-dev]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.0.1-dev\n"
	if !strings.HasSuffix(newChangelog, wantFooter) {
		t.Errorf("footer ordering/content wrong.\ngot changelog:\n%s\nwant footer suffix:\n%s", newChangelog, wantFooter)
	}
}

func TestFinalize_FooterBaseURLIsParsedNotHardcoded(t *testing.T) {
	// A completely different repo host/owner. If the base URL were hardcoded to
	// the anyscale one, these assertions would fail.
	changelog := "# Changelog\n\n" +
		"## [Unreleased]\n\n### Fixed\n\n- a bug\n\n" +
		"## [1.0.0] - 2026-01-01\n\nstuff\n\n" +
		"[Unreleased]: https://gitlab.example.com/team/widget/compare/v1.0.0...HEAD\n" +
		"[1.0.0]: https://gitlab.example.com/team/widget/releases/tag/v1.0.0\n"

	newChangelog, _, err := Finalize(changelog, "1.1.0", "2026-02-02")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantUnreleased := "[Unreleased]: https://gitlab.example.com/team/widget/compare/v1.1.0...HEAD"
	wantTag := "[1.1.0]: https://gitlab.example.com/team/widget/releases/tag/v1.1.0"
	if !strings.Contains(newChangelog, wantUnreleased+"\n") {
		t.Errorf("base URL not derived from footer; expected %q in:\n%s", wantUnreleased, newChangelog)
	}
	if !strings.Contains(newChangelog, wantTag+"\n") {
		t.Errorf("base URL not derived from footer; expected %q in:\n%s", wantTag, newChangelog)
	}
	if strings.Contains(newChangelog, "anyscale") {
		t.Errorf("footer must not contain a hardcoded anyscale URL:\n%s", newChangelog)
	}
}

func TestFinalize_NoFooterSectionLeavesBodyIntact(t *testing.T) {
	// No reference-link footer at all. Documented behavior: leave the changelog
	// body correct and do NOT fabricate a footer (we can't derive the repo URL).
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"

	newChangelog, notes, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The body transform (rename in place, no fresh Unreleased) must still be correct...
	wantChangelog := "# Changelog\n\n## [0.2.0] - 2026-08-01\n\n### Added\n\n- new thing\n\n## [0.1.1] - 2026-07-06\n\nold content\n"
	if newChangelog != wantChangelog {
		t.Errorf("body should be finalized unchanged when there is no footer.\ngot:\n%q\nwant:\n%q", newChangelog, wantChangelog)
	}
	// ...and no footer link definitions should have been fabricated.
	if strings.Contains(newChangelog, "compare/") || strings.Contains(newChangelog, "releases/tag/") {
		t.Errorf("no footer should be fabricated when none exists:\n%s", newChangelog)
	}
	if notes != "### Added\n\n- new thing" {
		t.Errorf("release notes wrong: %q", notes)
	}
}

func TestFinalize_FooterBaseFallsBackToTagURL(t *testing.T) {
	// The [Unreleased] line is present but its target is NOT a parseable
	// compare URL (here it just points at a bare tree URL). The base URL must
	// then be derived from the [1.0.0] tag line instead, proving the fallback
	// path is real and not dead code.
	changelog := "# Changelog\n\n" +
		"## [Unreleased]\n\n### Fixed\n\n- a bug\n\n" +
		"## [1.0.0] - 2026-01-01\n\nstuff\n\n" +
		"[Unreleased]: https://example.org/acme/thing/tree/main\n" +
		"[1.0.0]: https://example.org/acme/thing/releases/tag/v1.0.0\n"

	newChangelog, _, err := Finalize(changelog, "1.1.0", "2026-02-02")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantUnreleased := "[Unreleased]: https://example.org/acme/thing/compare/v1.1.0...HEAD"
	wantTag := "[1.1.0]: https://example.org/acme/thing/releases/tag/v1.1.0"
	if !strings.Contains(newChangelog, wantUnreleased+"\n") {
		t.Errorf("expected base derived from tag URL in Unreleased line %q, got:\n%s", wantUnreleased, newChangelog)
	}
	if !strings.Contains(newChangelog, wantTag+"\n") {
		t.Errorf("expected new tag link %q, got:\n%s", wantTag, newChangelog)
	}
}

func TestFinalize_FooterWithNoDerivableBaseLeftUnchanged(t *testing.T) {
	// An [Unreleased] line exists but there is NO compare URL and NO tag line to
	// derive a base URL from. We can't safely edit it, so the footer (and body)
	// must be left exactly as-is rather than emitting a malformed URL.
	changelog := "# Changelog\n\n" +
		"## [Unreleased]\n\n### Fixed\n\n- a bug\n\n" +
		"## [1.0.0] - 2026-01-01\n\nstuff\n\n" +
		"[Unreleased]: see the git log\n"

	newChangelog, _, err := Finalize(changelog, "1.1.0", "2026-02-02")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(newChangelog, "[Unreleased]: see the git log\n") {
		t.Errorf("footer with no derivable base URL must be left unchanged, got:\n%s", newChangelog)
	}
	if strings.Contains(newChangelog, "compare/") || strings.Contains(newChangelog, "releases/tag/") {
		t.Errorf("must not fabricate URLs when no base is derivable:\n%s", newChangelog)
	}
}

func TestFinalize_FooterMaintenanceIsIdempotentInSpirit(t *testing.T) {
	// Finalizing X.Y.Z, then re-finalizing the SAME X.Y.Z against the produced
	// footer, must leave a consistent footer: [Unreleased] still compares from
	// X.Y.Z and the [X.Y.Z] tag link is present exactly once (no duplicate, no
	// drift). This proves the footer transform re-parses stably.
	changelog := "# Changelog\n\n" +
		"## [Unreleased]\n\n### Added\n\n- new thing\n\n" +
		"## [0.1.1] - 2026-07-06\n\nold\n\n" +
		"[Unreleased]: https://github.com/anyscale/terraform-provider-anyscale/compare/v0.1.1...HEAD\n" +
		"[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1\n"

	once, _, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("first Finalize: %v", err)
	}
	// Re-run updateFooterLinks directly for the same version against the already
	// finalized footer: this is the "re-parsing is stable" property.
	twiceFooter := updateFooterLinks(once, "0.2.0")

	wantUnreleasedCount := strings.Count(twiceFooter, "compare/v0.2.0...HEAD")
	if wantUnreleasedCount != 1 {
		t.Errorf("expected exactly one Unreleased compare-from-0.2.0 line, got %d:\n%s", wantUnreleasedCount, twiceFooter)
	}
	tagCount := strings.Count(twiceFooter, "[0.2.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.2.0")
	if tagCount != 1 {
		t.Errorf("expected exactly one [0.2.0] tag link after re-parse, got %d:\n%s", tagCount, twiceFooter)
	}
	if strings.Contains(twiceFooter, "compare/v0.1.1...HEAD") {
		t.Errorf("re-parse must not resurrect the stale v0.1.1 compare base:\n%s", twiceFooter)
	}
}

func TestFinalize_ReleaseNotesExcludeFooterLinks(t *testing.T) {
	// The releaseNotes value must be the section body only — never any footer
	// link definitions, even when a footer is present and gets maintained.
	changelog := "# Changelog\n\n" +
		"## [Unreleased]\n\n### Added\n\n- new thing\n\n" +
		"## [0.1.1] - 2026-07-06\n\nold\n\n" +
		"[Unreleased]: https://github.com/anyscale/terraform-provider-anyscale/compare/v0.1.1...HEAD\n" +
		"[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1\n"

	_, notes, err := Finalize(changelog, "0.2.0", "2026-08-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notes != "### Added\n\n- new thing" {
		t.Errorf("release notes must be the section body only, got:\n%q", notes)
	}
	if strings.Contains(notes, "compare/") || strings.Contains(notes, "releases/tag/") || strings.Contains(notes, "[Unreleased]:") {
		t.Errorf("release notes must not contain footer link definitions, got:\n%q", notes)
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
