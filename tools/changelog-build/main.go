// Command changelog-build folds .changelog/<PR#>.txt release-note fragments
// (see RELEASING.md and .changelog/README.md) into CHANGELOG.md's
// "## [Unreleased]" section, and, at release time, finalizes that section into
// a dated version heading while emitting a release-notes file for GoReleaser.
//
// Usage:
//
//	changelog-build                    # fold: regenerate Unreleased from current fragments (idempotent)
//	changelog-build -check             # validate fragments parse; write nothing
//	changelog-build -finalize X.Y.Z    # fold, then cut Unreleased into "## [X.Y.Z] - <date>",
//	                                   # delete consumed fragments, write a release-notes file
//	changelog-build -extract X.Y.Z     # read-only: re-emit an already-committed version
//	                                   # section's body (used by release.yml on a fresh checkout)
//	changelog-build -check-tags        # read-only: checks both directions between CHANGELOG.md
//	                                   # version headings and git tags - every non-topmost,
//	                                   # non-Unreleased heading needs a matching tag or opt-out
//	                                   # marker (CheckHeadingsTagged), and every tag needs a
//	                                   # matching heading or its own opt-out marker
//	                                   # (CheckTagsHeaded); see tagcheck.go
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "changelog-build: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("changelog-build", flag.ContinueOnError)
	changelogPath := fs.String("changelog", "CHANGELOG.md", "path to CHANGELOG.md")
	fragmentsDir := fs.String("fragments", ".changelog", "path to the release-note fragments directory")
	finalizeVersion := fs.String("finalize", "", "finalize Unreleased into this version (e.g. 0.2.0) instead of folding in place")
	extractVersion := fs.String("extract", "", "read-only: print/write an already-committed version section's body, then exit")
	notesOut := fs.String("notes-out", "dist/release-notes.md", "where to write the section body (with -finalize or -extract)")
	date := fs.String("date", time.Now().UTC().Format("2006-01-02"), "release date to stamp on the finalized version heading (only with -finalize)")
	check := fs.Bool("check", false, "validate that all fragments parse; write nothing")
	checkTags := fs.Bool("check-tags", false, "validate both directions between CHANGELOG.md version headings and git tags (every heading has a tag, every tag has a heading), each with its own explicit opt-out marker; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *extractVersion != "" {
		changelog, err := os.ReadFile(*changelogPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", *changelogPath, err)
		}
		notes, err := Extract(string(changelog), *extractVersion)
		if err != nil {
			return fmt.Errorf("extracting %s: %w", *extractVersion, err)
		}
		if err := os.MkdirAll(filepath.Dir(*notesOut), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(*notesOut), err)
		}
		if err := os.WriteFile(*notesOut, []byte(notes+"\n"), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", *notesOut, err)
		}
		fmt.Printf("changelog-build: extracted [%s] section to %s\n", *extractVersion, *notesOut)
		return nil
	}

	if *checkTags {
		changelog, err := os.ReadFile(*changelogPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", *changelogPath, err)
		}
		tags, err := gitListTags()
		if err != nil {
			return fmt.Errorf("listing git tags: %w", err)
		}
		var errs []error
		errs = append(errs, CheckHeadingsTagged(string(changelog), gitTagExists)...)
		errs = append(errs, CheckTagsHeaded(string(changelog), tags)...)
		if len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "changelog-build: %v\n", e)
			}
			return fmt.Errorf("%d changelog tag/heading check(s) failed", len(errs))
		}
		fmt.Println("changelog-build: every non-topmost, non-Unreleased CHANGELOG.md version heading has a matching git tag or opt-out marker, and every git tag has a matching heading or opt-out marker")
		return nil
	}

	entries, err := ParseFragments(*fragmentsDir)
	if err != nil {
		return fmt.Errorf("parsing fragments in %s: %w", *fragmentsDir, err)
	}

	if *check {
		fmt.Printf("changelog-build: %d fragment(s) in %s parse cleanly\n", countFragmentFiles(entries), *fragmentsDir)
		return nil
	}

	changelog, err := os.ReadFile(*changelogPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", *changelogPath, err)
	}

	body := RenderSection(entries)
	folded, err := Fold(string(changelog), body)
	if err != nil {
		return fmt.Errorf("folding fragments into %s: %w", *changelogPath, err)
	}

	if *finalizeVersion == "" {
		if err := os.WriteFile(*changelogPath, []byte(folded), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", *changelogPath, err)
		}
		fmt.Printf("changelog-build: folded %d fragment(s) into [Unreleased]\n", len(entries))
		return nil
	}

	finalChangelog, releaseNotes, err := Finalize(folded, *finalizeVersion, *date)
	if err != nil {
		return fmt.Errorf("finalizing %s: %w", *changelogPath, err)
	}
	if err := os.WriteFile(*changelogPath, []byte(finalChangelog), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", *changelogPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(*notesOut), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(*notesOut), err)
	}
	if err := os.WriteFile(*notesOut, []byte(releaseNotes+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", *notesOut, err)
	}
	for _, f := range fragmentFiles(entries, *fragmentsDir) {
		if err := os.Remove(f); err != nil {
			return fmt.Errorf("removing consumed fragment %s: %w", f, err)
		}
	}
	fmt.Printf("changelog-build: finalized [%s] - %s, wrote %s, removed %d fragment(s)\n", *finalizeVersion, *date, *notesOut, countFragmentFiles(entries))
	return nil
}

func countFragmentFiles(entries []Entry) int {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Source] = true
	}
	return len(seen)
}

func fragmentFiles(entries []Entry, dir string) []string {
	seen := map[string]bool{}
	var files []string
	for _, e := range entries {
		if !seen[e.Source] {
			seen[e.Source] = true
			files = append(files, filepath.Join(dir, e.Source))
		}
	}
	return files
}

// gitTagExists is the real CheckHeadingsTagged tagExists implementation used
// outside of tests. It shells out to git rather than any network call, so it
// depends entirely on the tag already being present in the local checkout -
// in CI that requires more than actions/checkout's default shallow clone;
// see the fetch-depth: 0 plus explicit `git fetch --tags` steps this check is
// wired up behind in changelog-gate.yml.
func gitTagExists(version string) bool {
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/tags/v"+version)
	return cmd.Run() == nil
}

// gitListTags is the real CheckTagsHeaded tags implementation used outside
// of tests. A failure here (e.g. not run inside a git checkout at all) is
// reported as an error rather than swallowed into an empty list - treating
// "git failed" as "there are zero tags" would silently disarm the whole
// check instead of failing loudly, which is exactly the class of bug
// CheckTagsHeaded exists to catch elsewhere. Same fetch-depth/fetch-tags
// dependency as gitTagExists.
func gitListTags() ([]string, error) {
	out, err := exec.Command("git", "tag", "-l").Output()
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}
