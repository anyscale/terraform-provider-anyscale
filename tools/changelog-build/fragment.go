package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// EntryType is a release-note fragment type from the ratified changelog contract
// (.crystl/quest/changelog-release-contract.md, sec3).
type EntryType string

const (
	TypeBreakingChange EntryType = "breaking-change"
	TypeNewResource    EntryType = "new-resource"
	TypeNewDataSource  EntryType = "new-data-source"
	TypeAdded          EntryType = "added"
	TypeChanged        EntryType = "changed"
	TypeDeprecated     EntryType = "deprecated"
	TypeRemoved        EntryType = "removed"
	TypeFixed          EntryType = "fixed"
	TypeSecurity       EntryType = "security"
)

// typeOrder is the section render order: highest-signal entries first.
var typeOrder = []EntryType{
	TypeBreakingChange,
	TypeNewResource,
	TypeNewDataSource,
	TypeAdded,
	TypeChanged,
	TypeDeprecated,
	TypeRemoved,
	TypeFixed,
	TypeSecurity,
}

var sectionHeading = map[EntryType]string{
	TypeBreakingChange: "Breaking Changes",
	TypeNewResource:    "New Resources",
	TypeNewDataSource:  "New Data Sources",
	TypeAdded:          "Added",
	TypeChanged:        "Changed",
	TypeDeprecated:     "Deprecated",
	TypeRemoved:        "Removed",
	TypeFixed:          "Fixed",
	TypeSecurity:       "Security",
}

var validTypes = func() map[string]EntryType {
	m := make(map[string]EntryType, len(typeOrder))
	for _, t := range typeOrder {
		m[string(t)] = t
	}
	return m
}()

// Entry is one release-note fragment entry, parsed from a .changelog/<PR#>.txt file.
type Entry struct {
	Type   EntryType
	Text   string
	Source string // fragment filename, for error messages
}

// ParseFragments reads and parses every *.txt fragment in dir, in a stable
// (numeric-by-filename, falling back to lexical) order.
func ParseFragments(dir string) ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		return nil, err
	}
	sortFragmentFiles(files)

	var entries []Entry
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		es, err := parseFragmentContent(filepath.Base(f), string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		if len(es) == 0 {
			return nil, fmt.Errorf("%s: no release-note fragments found", filepath.Base(f))
		}
		entries = append(entries, es...)
	}
	return entries, nil
}

func sortFragmentFiles(files []string) {
	sort.Slice(files, func(i, j int) bool {
		ni, oki := fragmentNumber(files[i])
		nj, okj := fragmentNumber(files[j])
		if oki && okj && ni != nj {
			return ni < nj
		}
		return files[i] < files[j]
	})
}

func fragmentNumber(path string) (int, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".txt")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseFragmentContent extracts one or more release-note entries from a single
// fragment file's content. Exact format (contract sec2):
//
//	```
//	release-note:<type>
//	<one user-facing sentence>
//	```
//
// The type declaration may also appear as the fence's info string
// ("```release-note:<type>") for compatibility with the HashiCorp convention;
// both forms are accepted.
func parseFragmentContent(source, content string) ([]Entry, error) {
	lines := strings.Split(content, "\n")
	var entries []Entry
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "```") {
			i++
			continue
		}
		fenceInfo := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		i++

		var rawType string
		if fenceInfo != "" {
			rawType = fenceInfo
		} else {
			for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
				i++
			}
			if i >= len(lines) {
				return nil, fmt.Errorf("unterminated fence: expected a release-note:<type> declaration")
			}
			rawType = strings.TrimSpace(lines[i])
			i++
		}

		rawType = strings.TrimSpace(strings.TrimPrefix(rawType, "release-note:"))
		entryType, ok := validTypes[strings.ToLower(rawType)]
		if !ok {
			return nil, fmt.Errorf("unrecognized release-note type %q (want one of: breaking-change, new-resource, new-data-source, added, changed, deprecated, removed, fixed, security)", rawType)
		}

		var bodyLines []string
		closed := false
		for i < len(lines) {
			if strings.TrimSpace(lines[i]) == "```" {
				closed = true
				i++
				break
			}
			bodyLines = append(bodyLines, strings.TrimSpace(lines[i]))
			i++
		}
		if !closed {
			return nil, fmt.Errorf("unterminated fence for release-note:%s", rawType)
		}

		text := strings.Join(strings.Fields(strings.Join(bodyLines, " ")), " ")
		if text == "" {
			return nil, fmt.Errorf("empty release-note:%s body", rawType)
		}
		entries = append(entries, Entry{Type: entryType, Text: text, Source: source})
	}
	return entries, nil
}
