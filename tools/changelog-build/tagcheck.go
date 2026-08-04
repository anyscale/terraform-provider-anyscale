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
