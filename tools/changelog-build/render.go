package main

import (
	"fmt"
	"regexp"
	"strings"
)

const unreleasedMarker = "## [Unreleased]"

// footerUnreleasedLineRe matches the whole Keep a Changelog reference-link line
// for the Unreleased section, whatever its target, e.g.
//
//	[Unreleased]: https://github.com/owner/repo/compare/v0.1.1...HEAD
//
// It only identifies the line to anchor on; the base URL is parsed separately so
// a bare or unusually-shaped [Unreleased]: target can still fall back to a tag
// URL for the base.
var footerUnreleasedLineRe = regexp.MustCompile(`(?m)^\[Unreleased\]:.*$`)

// footerCompareBaseRe extracts the repo base URL (group 1) from a
// `.../compare/vX...HEAD` reference target — everything before "/compare/".
var footerCompareBaseRe = regexp.MustCompile(`(\S+?)/compare/v\S+?\.\.\.HEAD`)

// footerTagBaseRe extracts the repo base URL (group 1) from any released-version
// reference-link definition, e.g.
//
//	[0.1.1]: https://github.com/owner/repo/releases/tag/v0.1.1
//
// Used as a fallback source for the base URL when the [Unreleased]: line is
// present but its target is not a parseable compare URL.
var footerTagBaseRe = regexp.MustCompile(`(?m)^\[[^\]]+\]:\s*(\S+?)/releases/tag/v\S+\s*$`)

// RenderSection groups entries by type (the order in .changelog/README.md) and renders them
// as Keep a Changelog-style Markdown subsections. Deterministic: the same
// entries in the same order always render identically, which is what makes
// the fold step idempotent.
func RenderSection(entries []Entry) string {
	byType := make(map[EntryType][]Entry, len(typeOrder))
	for _, e := range entries {
		byType[e.Type] = append(byType[e.Type], e)
	}

	var b strings.Builder
	wroteAny := false
	for _, t := range typeOrder {
		es := byType[t]
		if len(es) == 0 {
			continue
		}
		if wroteAny {
			b.WriteString("\n")
		}
		wroteAny = true
		fmt.Fprintf(&b, "### %s\n\n", sectionHeading[t])
		for _, e := range es {
			b.WriteString("- " + e.Text + "\n")
		}
	}
	return b.String()
}

// Fold replaces the ## [Unreleased] section body in changelog with body
// (the output of RenderSection). It is a pure text transform: same inputs,
// same output, every time.
//
// Finalize no longer leaves a standing empty ## [Unreleased] heading behind
// (see below), so between releases the changelog may have no such heading at
// all. If body is non-empty, Fold creates one fresh, immediately above the
// newest version heading (or at EOF if there is no version heading yet). If
// body is empty and no heading exists, there is nothing to fold and nothing
// to clean up, so the changelog is returned unchanged rather than manufacturing
// an empty heading purely to have one.
func Fold(changelog, body string) (string, error) {
	idx := strings.Index(changelog, unreleasedMarker)
	if idx == -1 {
		if body == "" {
			return changelog, nil
		}
		insertAt := len(changelog)
		if nvh := nextVersionHeading(changelog); nvh != -1 {
			insertAt = nvh
		}
		section := unreleasedMarker + "\n\n" + strings.TrimRight(body, "\n") + "\n\n"
		return changelog[:insertAt] + section + changelog[insertAt:], nil
	}
	afterHeading := idx + len(unreleasedMarker)
	rest := changelog[afterHeading:]

	tail := ""
	if nextIdx := nextVersionHeading(rest); nextIdx != -1 {
		tail = rest[nextIdx:]
	}

	newBody := "\n\n"
	if body != "" {
		newBody = "\n\n" + strings.TrimRight(body, "\n") + "\n\n"
	}

	return changelog[:afterHeading] + newBody + tail, nil
}

// Finalize renames the current ## [Unreleased] section to a dated version
// heading, in place - it does NOT leave a fresh empty ## [Unreleased] behind,
// so the changelog leads with the newest real version instead of an empty
// placeholder heading. Fold (above) is what reintroduces ## [Unreleased] the
// next time there is an actual fragment to fold in, so the accumulation
// workflow is unaffected; only the standing empty header between releases is
// gone. Finalize also maintains the Keep a Changelog reference-link footer for
// the new version, and returns the updated changelog plus the finalized
// section's own body (for feeding GoReleaser --release-notes so the GitHub
// Release matches CHANGELOG.md byte-for-byte; see RELEASING.md).
//
// releaseNotes is the finalized section body ONLY — footer link definitions are
// not part of the release notes, so they never appear in the returned notes.
func Finalize(changelog, version, date string) (newChangelog, releaseNotes string, err error) {
	idx := strings.Index(changelog, unreleasedMarker)
	if idx == -1 {
		return "", "", fmt.Errorf("no %q heading found in changelog", unreleasedMarker)
	}
	afterHeading := idx + len(unreleasedMarker)
	rest := changelog[afterHeading:]

	tail := ""
	body := rest
	if nextIdx := nextVersionHeading(rest); nextIdx != -1 {
		body = rest[:nextIdx]
		tail = rest[nextIdx:]
	}

	releaseNotes = strings.TrimSpace(body)
	versionHeading := fmt.Sprintf("## [%s] - %s", version, date)
	newSection := versionHeading + body

	newChangelog = changelog[:idx] + newSection + tail
	// Footer maintenance operates on the whole document (the link section lives
	// at the very bottom, after `tail`) and is independent of the release notes
	// computed above, so releaseNotes stays exactly the section body.
	newChangelog = updateFooterLinks(newChangelog, version)

	return newChangelog, releaseNotes, nil
}

// updateFooterLinks maintains the Keep a Changelog reference-link footer for a
// newly finalized version. It:
//
//  1. rewrites the `[Unreleased]: <base>/compare/vOLD...HEAD` line to compare
//     from the new version (`<base>/compare/vX.Y.Z...HEAD`), and
//  2. inserts `[X.Y.Z]: <base>/releases/tag/vX.Y.Z` immediately below the
//     `[Unreleased]:` line, preserving newest-first ordering and every existing
//     version link.
//
// The repo base URL is parsed from the existing footer (the `[Unreleased]:`
// compare URL, or, as a fallback, any `[x.y.z]:` tag URL) rather than hardcoded,
// so the tool stays repo-agnostic.
//
// The repo base URL is parsed from the existing footer: first from the
// `[Unreleased]:` line's own `.../compare/vX...HEAD` target, and if that target
// is not a parseable compare URL, from any `[x.y.z]:` tag URL. Either way it is
// derived, never hardcoded, so the tool stays repo-agnostic.
//
// If there is no `[Unreleased]:` footer line to anchor on (e.g. a changelog with
// no reference-link section at all), OR there is one but no base URL can be
// derived from anywhere in the footer, the changelog body is returned unchanged:
// we deliberately do NOT fabricate a footer, since a malformed footer would be
// worse than none. This keeps the transform pure, deterministic, and
// non-corrupting on inputs that never had a (usable) footer.
func updateFooterLinks(changelog, version string) string {
	loc := footerUnreleasedLineRe.FindStringIndex(changelog)
	if loc == nil {
		// No "[Unreleased]:" line to anchor on: no footer (or an unrecognizable
		// one). Leave the document untouched rather than invent a footer.
		return changelog
	}
	unreleasedLine := changelog[loc[0]:loc[1]]

	// Derive the base URL: prefer the [Unreleased] line's own compare target,
	// then fall back to any release-tag line elsewhere in the footer.
	base := ""
	if cm := footerCompareBaseRe.FindStringSubmatch(unreleasedLine); cm != nil {
		base = cm[1]
	} else if tm := footerTagBaseRe.FindStringSubmatch(changelog); tm != nil {
		base = tm[1]
	}
	if base == "" {
		// A footer line exists but we can't derive a repo URL from it or any tag
		// line. Editing would risk emitting a malformed URL, so leave it alone.
		return changelog
	}

	// Replace the whole matched [Unreleased]: line (loc[0]:loc[1]) with the
	// rewritten compare line plus, unless it already exists, the new version's
	// tag definition on the next line. Preserving the match bounds keeps
	// everything before and after (trailing older version links, final newline)
	// intact.
	newUnreleasedLine := fmt.Sprintf("[Unreleased]: %s/compare/v%s...HEAD", base, version)
	versionLine := fmt.Sprintf("[%s]: %s/releases/tag/v%s", version, base, version)

	// Idempotency: if a `[X.Y.Z]:` definition for this exact version already
	// exists in the footer, don't insert a duplicate — just rewrite the
	// Unreleased compare line. This makes re-finalizing the same version (or
	// re-parsing an already-finalized footer) stable.
	replacement := newUnreleasedLine
	if !hasVersionLinkDefinition(changelog, version) {
		replacement += "\n" + versionLine
	}

	return changelog[:loc[0]] + replacement + changelog[loc[1]:]
}

// Extract returns the body of an already-committed "## [<version>] - ..."
// section, without modifying changelog. This is what release.yml uses on a
// fresh checkout at the release tag to hand GoReleaser the exact same text
// `make tag` already committed to CHANGELOG.md, so the GitHub Release body
// byte-matches the CHANGELOG.md section for that version (see RELEASING.md).
func Extract(changelog, version string) (string, error) {
	marker := fmt.Sprintf("## [%s]", version)
	idx := strings.Index(changelog, marker)
	if idx == -1 {
		return "", fmt.Errorf("no %q heading found in changelog", marker)
	}

	lineEnd := strings.IndexByte(changelog[idx:], '\n')
	if lineEnd == -1 {
		return "", nil
	}
	rest := changelog[idx+lineEnd:]

	body := rest
	if nextIdx := nextVersionHeading(rest); nextIdx != -1 {
		body = rest[:nextIdx]
	}
	return strings.TrimSpace(body), nil
}

// hasVersionLinkDefinition reports whether the footer already contains a
// reference-link definition for the exact version, i.e. a line beginning with
// `[X.Y.Z]:`. Used to keep footer maintenance idempotent.
func hasVersionLinkDefinition(changelog, version string) bool {
	needle := "[" + version + "]:"
	for _, line := range strings.Split(changelog, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), needle) {
			return true
		}
	}
	return false
}

// nextVersionHeading returns the index within s of the next "## [" heading
// line, or -1 if there isn't one (i.e. this is the last section in the file).
func nextVersionHeading(s string) int {
	idx := strings.Index(s, "\n## [")
	if idx == -1 {
		return -1
	}
	return idx + 1 // skip the leading \n, keep "## [..."
}
