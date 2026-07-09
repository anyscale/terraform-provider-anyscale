package main

import (
	"fmt"
	"strings"
)

const unreleasedMarker = "## [Unreleased]"

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
func Fold(changelog, body string) (string, error) {
	idx := strings.Index(changelog, unreleasedMarker)
	if idx == -1 {
		return "", fmt.Errorf("no %q heading found in changelog", unreleasedMarker)
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
// heading, inserts a fresh empty ## [Unreleased] above it, and returns the
// updated changelog plus the finalized section's own body (for feeding
// GoReleaser --release-notes so the GitHub Release matches CHANGELOG.md
// byte-for-byte; see RELEASING.md).
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
	newSection := unreleasedMarker + "\n\n" + versionHeading + body

	return changelog[:idx] + newSection + tail, releaseNotes, nil
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

// nextVersionHeading returns the index within s of the next "## [" heading
// line, or -1 if there isn't one (i.e. this is the last section in the file).
func nextVersionHeading(s string) int {
	idx := strings.Index(s, "\n## [")
	if idx == -1 {
		return -1
	}
	return idx + 1 // skip the leading \n, keep "## [..."
}
