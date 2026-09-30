package main

import (
	"fmt"
	"strings"
)

func countSeverity(changes []Change, sev Severity) int {
	n := 0
	for _, c := range changes {
		if c.Severity == sev {
			n++
		}
	}
	return n
}

// Render formats changes as a GitHub job-summary markdown report. Changes
// keep Diff's deterministic order within each severity section.
func Render(provider string, changes []Change) string {
	var sb strings.Builder
	sb.WriteString("## Schema diff\n\n")

	nb, nr, nn := countSeverity(changes, Breaking), countSeverity(changes, NeedsReview), countSeverity(changes, NonBreaking)
	if len(changes) == 0 {
		fmt.Fprintf(&sb, "`%s`: no schema changes.\n\n", provider)
	} else {
		fmt.Fprintf(&sb, "`%s`: **%d breaking**, %d needs review, %d non-breaking.\n\n", provider, nb, nr, nn)
	}

	section := func(sev Severity, heading string, collapse bool) {
		if countSeverity(changes, sev) == 0 {
			return
		}
		if collapse {
			fmt.Fprintf(&sb, "<details><summary>%s</summary>\n\n", heading)
		} else {
			fmt.Fprintf(&sb, "### %s\n\n", heading)
		}
		for _, c := range changes {
			if c.Severity == sev {
				fmt.Fprintf(&sb, "- `%s`: %s (`%s`)\n", c.Subject, c.Message, c.Rule)
			}
		}
		if collapse {
			sb.WriteString("\n</details>\n")
		}
		sb.WriteString("\n")
	}
	section(Breaking, "Breaking", false)
	section(NeedsReview, "Needs review", false)
	section(NonBreaking, fmt.Sprintf("Non-breaking (%d)", nn), true)

	sb.WriteString("> Schema JSON does not show plan modifiers: a newly added `RequiresReplace` is invisible to this check and remains a review item.\n")
	return sb.String()
}
