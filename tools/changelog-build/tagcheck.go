package main

import (
	"fmt"
	"regexp"
	"strings"
)

// noTagOkMarker is the exact opt-out line CheckHeadingsTagged accepts
// directly beneath a version heading, in lieu of a matching git tag. It is an
// HTML comment so it stays invisible when GitHub (or any Markdown renderer)
// renders CHANGELOG.md - a machine-checkable twin of whatever human-facing
// prose explains the gap (see the "## [0.24.0]" note about "## [0.23.0]" for
// an example of that prose; the marker deliberately does not replace it).
const noTagOkMarker = "<!-- changelog-gate:no-tag-ok -->"

// versionHeadingRe matches a "## [<version>]" CHANGELOG.md heading line and
// captures the bracketed text (group 1) - either a version like "0.23.0", or
// the literal word "Unreleased".
var versionHeadingRe = regexp.MustCompile(`(?m)^## \[([^\]]+)\]`)

// CheckHeadingsTagged walks every "## [<version>]" heading in changelog, top
// to bottom (top = newest, matching this repo's Keep a Changelog
// convention), and returns one error per heading that is neither:
//
//   - the literal "## [Unreleased]" heading - always exempt, since it never
//     has a tag by definition; nor
//   - the single topmost version heading - always exempt unconditionally,
//     regardless of its own tag state. This repo's release process
//     (`make changelog-release` finalizes "## [Unreleased]" into a dated
//     heading on a PR; `make tag` only tags it after that PR merges, as a
//     separate later step) has a real, normal window where the newest
//     heading legitimately has no tag yet. Flagging it would make the
//     finalize PR permanently red. Every heading below the topmost one is
//     fair game: it is no longer "in flight" once something newer supersedes
//     it; nor
//   - backed by a real git tag (tagExists(version) == true); nor
//   - marked with an explicit noTagOkMarker line directly beneath the
//     heading (an opt-out for a version that was finalized in CHANGELOG.md
//     but deliberately never tagged or released).
//
// WHY THIS CHECK EXISTS: a finalized version heading with no matching git tag
// lets a reader believe a version shipped that never actually did, silently -
// unless the gap happens to be self-documented (as "## [0.23.0]" is, via
// prose under "## [0.24.0]" plus the marker this check looks for). Nothing
// previously caught a *future* recurrence of that gap for a heading that
// isn't self-documented this way.
//
// tagExists is injected rather than this function shelling out to git
// itself, so it stays unit-testable without touching a real repository;
// main.go wires up the concrete implementation
// (`git rev-parse -q --verify refs/tags/v<version>`).
func CheckHeadingsTagged(changelog string, tagExists func(version string) bool) []error {
	matches := versionHeadingRe.FindAllStringSubmatchIndex(changelog, -1)

	var errs []error
	skippedTopmost := false
	for _, m := range matches {
		// m[0]:m[1] is the whole "## [...]" match; m[2]:m[3] is the
		// captured bracketed text.
		version := changelog[m[2]:m[3]]
		if version == "Unreleased" {
			continue
		}
		if !skippedTopmost {
			// The single topmost version heading (i.e. the first one found
			// that isn't "## [Unreleased]") is always exempt - see the
			// doc comment above.
			skippedTopmost = true
			continue
		}
		if tagExists(version) {
			continue
		}
		if lineAfterHeadingIsMarker(changelog, m[1]) {
			continue
		}
		errs = append(errs, fmt.Errorf(
			"CHANGELOG.md heading %q has no matching git tag (refs/tags/v%s) and no %s "+
				"opt-out marker on the line directly beneath it — either create/push the "+
				"missing tag, or add that exact line directly under the heading and record "+
				"why nearby (e.g. as prose, the way the \"## [0.24.0]\" section explains "+
				"\"## [0.23.0]\"); a bare marker with no explanation just defers the question",
			"## ["+version+"]", version, noTagOkMarker,
		))
	}
	return errs
}

// lineAfterHeadingIsMarker reports whether the line immediately following a
// version heading - the very next line after the heading's own line
// (including any trailing " - <date>" suffix), before any blank-line
// separator - is exactly noTagOkMarker. headingEnd is the offset in
// changelog right after the heading's closing "]" (i.e. the end bound of the
// versionHeadingRe match).
func lineAfterHeadingIsMarker(changelog string, headingEnd int) bool {
	rest := changelog[headingEnd:]
	nl := strings.IndexByte(rest, '\n')
	if nl == -1 {
		// The heading is the last line in the file; there is no line after it.
		return false
	}
	rest = rest[nl+1:]
	if nextNl := strings.IndexByte(rest, '\n'); nextNl != -1 {
		rest = rest[:nextNl]
	}
	return strings.TrimSpace(rest) == noTagOkMarker
}

// tagVersionRe extracts the version from a "v<version>" git tag name (e.g.
// "v0.25.1" -> "0.25.1"). A tag that does not start with a literal "v"
// followed by a digit is outside this project's release-tag convention and
// is ignored by CheckTagsHeaded rather than treated as an error - the check
// only knows how to relate release tags to CHANGELOG.md version headings.
var tagVersionRe = regexp.MustCompile(`^v(\d\S*)$`)

// tagNoHeadingMarkerFmt is the opt-out marker template CheckTagsHeaded
// accepts anywhere in the file to exempt a specific git tag from its
// requirement that every tag have a matching CHANGELOG.md heading, e.g.
// "<!-- changelog-gate:tag-no-heading-ok: 0.1.0 -->". Unlike noTagOkMarker
// (which must sit on the line directly beneath the heading it exempts),
// this marker exempts a *tag* that has no heading to sit beneath, so its
// position in the file does not matter to the check - but by the same
// convention noTagOkMarker's doc comment describes, a bare marker with no
// nearby human-facing prose explaining why just defers the question, and
// should not be added without it (see the "## [0.1.1]" section's own note
// about v0.1.0 for the pattern).
const tagNoHeadingMarkerFmt = "<!-- changelog-gate:tag-no-heading-ok: %s -->"

// tagNoHeadingMarkerRe parses tagNoHeadingMarkerFmt markers out of a
// changelog, capturing the exempted version in group 1.
var tagNoHeadingMarkerRe = regexp.MustCompile(`<!--\s*changelog-gate:tag-no-heading-ok:\s*(\S+)\s*-->`)

// CheckTagsHeaded is the converse of CheckHeadingsTagged: it walks every git
// tag and returns one error per tag that has neither a matching
// "## [<version>]" CHANGELOG.md heading nor a tagNoHeadingMarkerFmt opt-out
// marker anywhere in the file.
//
// WHY THIS CHECK EXISTS: CheckHeadingsTagged alone only catches a heading
// whose tag is missing - it has nothing to say about a heading that is
// missing entirely. Finalizing a release from a checkout whose CHANGELOG.md
// predates the previous release produces a file with the new version's
// heading and no previous one: the previous version is still really tagged
// and shipped, so this is a silent erasure of a real release from the
// record, and CheckHeadingsTagged reports success throughout, because it
// only ever looks at headings that are actually present.
//
// There is no exemption here symmetric to CheckHeadingsTagged's topmost-
// heading one. A tag is only ever created after its heading-bearing commit
// merges (`make changelog-release` finalizes a heading on a PR; `make tag`
// tags it only after that PR merges, as a separate later step), so a tag
// legitimately preceding its own heading cannot happen the way a heading
// legitimately preceding its own tag can - every tag this check sees should
// already have a heading-bearing commit behind it.
//
// tags is injected (see gitListTags for the real implementation) so this
// stays unit-testable without touching a real repository.
func CheckTagsHeaded(changelog string, tags []string) []error {
	headings := map[string]bool{}
	for _, m := range versionHeadingRe.FindAllStringSubmatch(changelog, -1) {
		headings[m[1]] = true
	}

	exempt := map[string]bool{}
	for _, m := range tagNoHeadingMarkerRe.FindAllStringSubmatch(changelog, -1) {
		exempt[m[1]] = true
	}

	var errs []error
	for _, tag := range tags {
		m := tagVersionRe.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		version := m[1]
		if headings[version] || exempt[version] {
			continue
		}
		errs = append(errs, fmt.Errorf(
			"git tag %q has no matching CHANGELOG.md heading (%q) and no matching opt-out "+
				"marker anywhere in the file — either add the missing heading documenting "+
				"what that release actually shipped, or add %q and record nearby why the "+
				"release has no section (e.g. as prose, the way the \"## [0.1.1]\" section "+
				"explains v0.1.0)",
			tag, "## ["+version+"]", fmt.Sprintf(tagNoHeadingMarkerFmt, version),
		))
	}
	return errs
}
