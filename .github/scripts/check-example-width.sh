#!/usr/bin/env bash
# Fail when a Registry-rendered example line is wider than the Registry's code
# box shows. See "Registry example width" in CLAUDE.md for the reasoning.
#
# Scope: *.tf and *.sh under the example directories the Registry renders.
# README.md is not rendered by the Registry, so it is not checked.
#
# A line over the limit still passes if it is one of:
#   - a line containing `terraform import` (import commands and the comments
#     that show them);
#   - a line containing a URL (scheme://);
#   - an attribute assignment whose value is one string literal, which cannot
#     be split without changing the value.
#
# Usage: check-example-width.sh [limit]   (default: EXAMPLE_WIDTH or 72)
set -euo pipefail

limit="${1:-${EXAMPLE_WIDTH:-72}}"
dirs=(examples/resources examples/data-sources examples/provider examples/ephemeral-resources)

existing=()
for d in "${dirs[@]}"; do
  [[ -d "$d" ]] && existing+=("$d")
done

find "${existing[@]}" -type f \( -name '*.tf' -o -name '*.sh' \) -print0 \
  | sort -z \
  | xargs -0 awk -v limit="$limit" '
      length($0) > limit {
        if ($0 ~ /terraform import/) { exempt["import"]++; next }
        if ($0 ~ /[A-Za-z][A-Za-z0-9+.-]*:\/\//) { exempt["url"]++; next }
        if ($0 ~ /^[ \t]*[A-Za-z0-9_]+[ \t]*=[ \t]*"[^"]*"[ \t]*(#.*)?$/) { exempt["literal"]++; next }
        printf "%s:%d: %d cols (limit %d)\n", FILENAME, FNR, length($0), limit
        bad++
      }
      END {
        printf "example width: %d over %d cols; exempt: %d import, %d url, %d literal\n",
          bad + 0, limit, exempt["import"] + 0, exempt["url"] + 0, exempt["literal"] + 0 > "/dev/stderr"
        exit bad > 0
      }'
