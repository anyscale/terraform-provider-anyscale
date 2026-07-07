#!/usr/bin/env bash
# Summarize acceptance-test SKIPs into the GitHub Actions step summary so a
# green run can't be silently mistaken for full coverage.
#
# internal/acctest/helpers.go's SkipIfNoRealInfra gates every acceptance test
# that creates real cloud infra (fake credentials can't pass real
# provisioning), and CI never sets ANYSCALE_TEST_REAL_INFRA=1 - by design, not
# a bug. Raw `go test -v` output already prints each individual `--- SKIP:`
# line, but that's easy to miss scrolling a long green log. This makes the
# count impossible to miss.
#
# Usage: summarize-acctest-skips.sh <go-test-log-file> <section-title>
set -euo pipefail

LOG_FILE="$1"
TITLE="$2"

TOTAL=$(grep -cE '^(--- PASS|--- FAIL|--- SKIP):' "$LOG_FILE" || true)
SKIPPED=$(grep -cE '^--- SKIP:' "$LOG_FILE" || true)
REALINFRA_SKIPPED=$(grep -c 'SKIP(no-real-infra)' "$LOG_FILE" || true)

{
  echo "### ${TITLE}"
  echo ""
  echo "${TOTAL} tests ran: $((TOTAL - SKIPPED)) executed, ${SKIPPED} skipped."
  if [ "${REALINFRA_SKIPPED}" -gt 0 ]; then
    echo ""
    echo "> ⚠️ **${REALINFRA_SKIPPED} of those skips are real-infra creation tests, gated behind \`ANYSCALE_TEST_REAL_INFRA=1\`, which this CI lane never sets.** They do NOT run here and a green check does NOT mean they passed - only that they were skipped by design (placeholder credentials can't pass real cloud provisioning). See \`internal/acctest/helpers.go\`'s \`SkipIfNoRealInfra\` doc comment. Treat real-infra creation coverage as unverified by CI until confirmed via a manual \`ANYSCALE_TEST_REAL_INFRA=1\` run or the \`make test-*\` example scenarios."
  fi
} >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
