package main

import (
	"strings"
	"testing"
)

func TestCheckHeadingsTagged(t *testing.T) {
	tests := []struct {
		name        string
		changelog   string
		tagged      map[string]bool
		wantNumErrs int
		wantErrText []string // each must appear as a substring of some returned error
	}{
		{
			name: "non-topmost heading with a real tag passes",
			changelog: "# Changelog\n\n" +
				"## [0.3.0] - 2026-08-01\n\nnewest\n\n" +
				"## [0.2.0] - 2026-07-01\n\nolder, has a real tag\n",
			tagged:      map[string]bool{"0.2.0": true},
			wantNumErrs: 0,
		},
		{
			name: "non-topmost heading with the marker passes",
			changelog: "# Changelog\n\n" +
				"## [0.3.0] - 2026-08-01\n\nnewest\n\n" +
				"## [0.2.0] - 2026-07-01\n" + noTagOkMarker + "\n\nolder, marked instead of tagged\n",
			tagged:      map[string]bool{},
			wantNumErrs: 0,
		},
		{
			name: "non-topmost heading with neither fails, naming the version",
			changelog: "# Changelog\n\n" +
				"## [0.3.0] - 2026-08-01\n\nnewest\n\n" +
				"## [0.2.0] - 2026-07-01\n\nolder, untagged and unmarked\n",
			tagged:      map[string]bool{},
			wantNumErrs: 1,
			wantErrText: []string{"0.2.0"},
		},
		{
			name: "topmost heading with neither passes - skipped unconditionally",
			changelog: "# Changelog\n\n" +
				"## [0.3.0] - 2026-08-01\n\nnewest, no tag, no marker, still exempt\n",
			tagged:      map[string]bool{},
			wantNumErrs: 0,
		},
		{
			name: "an Unreleased heading passes (exempt) and is not itself the topmost version",
			changelog: "# Changelog\n\n" +
				"## [Unreleased]\n\nwork in progress\n\n" +
				"## [0.3.0] - 2026-08-01\n\nthe real topmost version heading, skipped\n\n" +
				"## [0.2.0] - 2026-07-01\n\nmust be tagged or marked, and is neither\n",
			tagged:      map[string]bool{},
			wantNumErrs: 1,
			wantErrText: []string{"0.2.0"},
		},
		{
			name: "a marker separated from the heading by a blank line does not satisfy the check",
			changelog: "# Changelog\n\n" +
				"## [0.3.0] - 2026-08-01\n\nnewest\n\n" +
				"## [0.2.0] - 2026-07-01\n\n" + noTagOkMarker + "\n\nmarker is one line too late\n",
			tagged:      map[string]bool{},
			wantNumErrs: 1,
			wantErrText: []string{"0.2.0"},
		},
		{
			name: "multiple non-topmost headings are each evaluated independently",
			changelog: "# Changelog\n\n" +
				"## [0.4.0] - 2026-08-01\n\ntopmost, always skipped regardless of tag state\n\n" +
				"## [0.3.0] - 2026-07-15\n\nmiddle, has a real tag\n\n" +
				"## [0.2.0] - 2026-07-01\n" + noTagOkMarker + "\n\nmiddle, has the marker instead\n\n" +
				"## [0.1.0] - 2026-06-01\n\noldest, has neither - the only one that should fail\n",
			tagged:      map[string]bool{"0.3.0": true},
			wantNumErrs: 1,
			wantErrText: []string{"0.1.0"},
		},
		{
			name:        "no version headings at all is a no-op",
			changelog:   "# Changelog\n\nintro only, no releases cut yet\n",
			tagged:      map[string]bool{},
			wantNumErrs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tagExists := func(version string) bool { return tt.tagged[version] }
			errs := CheckHeadingsTagged(tt.changelog, tagExists)
			if len(errs) != tt.wantNumErrs {
				t.Fatalf("got %d error(s), want %d; errs: %v", len(errs), tt.wantNumErrs, errs)
			}
			for _, want := range tt.wantErrText {
				found := false
				for _, e := range errs {
					if strings.Contains(e.Error(), want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no returned error mentions %q; errs: %v", want, errs)
				}
			}
		})
	}
}

// TestCheckHeadingsTagged_RealChangelogShapeAroundV023AndV024 mirrors the
// actual CHANGELOG.md structure this check was built for: [0.25.0] is
// topmost (skipped unconditionally), [0.24.0] carries both a real tag and a
// human-facing prose note (passes on the tag alone, no marker needed), and
// [0.23.0] has no tag and relies solely on the marker directly beneath its
// heading.
func TestCheckHeadingsTagged_RealChangelogShapeAroundV023AndV024(t *testing.T) {
	changelog := "# Changelog\n\n" +
		"## [0.25.0] - 2026-08-03\n\n### Breaking Changes\n\n- topmost, always skipped\n\n" +
		"## [0.24.0] - 2026-07-28\n\n" +
		"**Note:** this release also carries the breaking changes listed under `[0.23.0]` below.\n\n" +
		"### Breaking Changes\n\n- some change\n\n" +
		"## [0.23.0] - 2026-07-27\n" + noTagOkMarker + "\n\n### Breaking Changes\n\n- cloud_name removed\n"

	// 0.25.0 deliberately omitted: it's topmost, so its tag state must not
	// matter. 0.24.0 has a real tag. 0.23.0 has none - it relies on the marker.
	tagged := map[string]bool{"0.24.0": true}
	tagExists := func(version string) bool { return tagged[version] }

	errs := CheckHeadingsTagged(changelog, tagExists)
	if len(errs) != 0 {
		t.Fatalf("got %d unexpected error(s): %v", len(errs), errs)
	}
}

func TestCheckTagsHeaded(t *testing.T) {
	tests := []struct {
		name        string
		changelog   string
		tags        []string
		wantNumErrs int
		wantErrText []string // each must appear as a substring of some returned error
	}{
		{
			name: "every tag has a matching heading",
			changelog: "# Changelog\n\n" +
				"## [0.2.0] - 2026-08-01\n\nnewest\n\n" +
				"## [0.1.0] - 2026-07-01\n\noldest\n",
			tags:        []string{"v0.2.0", "v0.1.0"},
			wantNumErrs: 0,
		},
		{
			name: "THE ERASURE SCENARIO: a stale finalize wipes an old heading while a new one lands - the missing version is still tagged and must be flagged",
			changelog: "# Changelog\n\n" +
				"## [0.2.0] - 2026-08-01\n\nfinalized from a checkout that predates 0.1.0's own heading\n",
			tags:        []string{"v0.2.0", "v0.1.0"},
			wantNumErrs: 1,
			wantErrText: []string{"v0.1.0"},
		},
		{
			name: "a tag with the marker anywhere in the file passes without a heading",
			changelog: "# Changelog\n\n" +
				"## [0.1.1] - 2026-07-06\n" +
				"<!-- changelog-gate:tag-no-heading-ok: 0.1.0 -->\n\n" +
				"documents what v0.1.0 and v0.1.1 both shipped\n",
			tags:        []string{"v0.1.1", "v0.1.0"},
			wantNumErrs: 0,
		},
		{
			name: "multiple missing tags are each reported independently",
			changelog: "# Changelog\n\n" +
				"## [0.3.0] - 2026-08-01\n\nonly this heading exists\n",
			tags:        []string{"v0.3.0", "v0.2.0", "v0.1.0"},
			wantNumErrs: 2,
			wantErrText: []string{"v0.2.0", "v0.1.0"},
		},
		{
			name:        "no tags at all is a no-op",
			changelog:   "# Changelog\n\nintro only\n",
			tags:        nil,
			wantNumErrs: 0,
		},
		{
			name: "a tag not shaped like this project's v<version> convention is ignored, not errored",
			changelog: "# Changelog\n\n" +
				"## [0.1.0] - 2026-07-01\n\nonly release\n",
			tags:        []string{"v0.1.0", "some-other-tool-tag", "nightly-build"},
			wantNumErrs: 0,
		},
		{
			name: "a marker for the WRONG version does not exempt the missing one",
			changelog: "# Changelog\n\n" +
				"## [0.2.0] - 2026-08-01\n" +
				"<!-- changelog-gate:tag-no-heading-ok: 0.0.9 -->\n\nnewest\n",
			tags:        []string{"v0.2.0", "v0.1.0"},
			wantNumErrs: 1,
			wantErrText: []string{"v0.1.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := CheckTagsHeaded(tt.changelog, tt.tags)
			if len(errs) != tt.wantNumErrs {
				t.Fatalf("got %d error(s), want %d; errs: %v", len(errs), tt.wantNumErrs, errs)
			}
			for _, want := range tt.wantErrText {
				found := false
				for _, e := range errs {
					if strings.Contains(e.Error(), want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no returned error mentions %q; errs: %v", want, errs)
				}
			}
		})
	}
}

// TestCheckTagsHeaded_RealChangelogShapeAroundV010AndV011 mirrors the actual
// CHANGELOG.md/tag history this check was built for: v0.1.0 and v0.1.1 are
// two separate tags on the exact same commit (verified via
// `git rev-list -n1 v0.1.0` / `v0.1.1` at 0e862fd), because v0.1.0 was this
// provider's first tagged release, created before CHANGELOG.md existed. It
// relies solely on the marker beneath "## [0.1.1]", the same way the real
// file's "## [0.23.0]" relies on noTagOkMarker for the converse check.
func TestCheckTagsHeaded_RealChangelogShapeAroundV010AndV011(t *testing.T) {
	changelog := "# Changelog\n\n" +
		"## [0.25.1] - 2026-08-04\n\n### Fixed\n\n- topmost\n\n" +
		"## [0.1.1] - 2026-07-06\n" +
		"<!-- changelog-gate:tag-no-heading-ok: 0.1.0 -->\n\n" +
		"**Note:** v0.1.0 and v0.1.1 are separate tags on the exact same commit - v0.1.0 was this " +
		"provider's first tagged release, created before CHANGELOG.md existed.\n\n" +
		"### Added\n\n- initial framework migration\n"

	// v0.1.0 deliberately has no heading of its own - it relies on the marker.
	// v0.1.1 and v0.25.1 both have real headings.
	tags := []string{"v0.25.1", "v0.1.1", "v0.1.0"}

	errs := CheckTagsHeaded(changelog, tags)
	if len(errs) != 0 {
		t.Fatalf("got %d unexpected error(s): %v", len(errs), errs)
	}
}
