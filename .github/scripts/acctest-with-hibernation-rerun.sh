#!/usr/bin/env bash
# Run one acceptance-test suite for scheduled-acctest.yml, re-running once
# the tests that failed only because the test cloud was hibernated.
#
# The static test cloud hibernates once it has had no clusters or builds for
# longer than a backend threshold (7 days in prod), counted from its last
# completed wake, so it can hibernate mid-run, after the wake gate saw it
# awake (internal/acctest/cloud_wake.go). A test whose first create lands
# then fails with HTTP 599 "hibernated due to inactivity", and that 599
# itself starts the wake. So:
#
#   1. Run the suite with `go test -json`. The JSON goes to <log>.jsonl; the
#      human-readable log is rendered from it to stdout and <log>.
#   2. On failure, classify from the JSON (tools/acctest-rerun). A rerun
#      happens only if every failed top-level test failed on hibernation.
#   3. Wait for the cloud to wake (TestAccWakeGate, bounded by the gate).
#   4. Re-run exactly those tests, once. The step passes only if they pass.
#
# Any other failure fails as before. The whole suite is never re-run.
#
# Writes recovered=true to $GITHUB_OUTPUT when the rerun passed, and a
# section to $GITHUB_STEP_SUMMARY for every outcome.
#
# Usage: acctest-with-hibernation-rerun.sh <title> <go-test-timeout> <run-pattern> <log>
set -uo pipefail

TITLE="$1"
TIMEOUT="$2"
RUN_PATTERN="$3"
LOG="$4"
EVENTS="${LOG}.jsonl"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
OUTPUT="${GITHUB_OUTPUT:-/dev/null}"

TOOL="$(mktemp -d)/acctest-rerun"
go build -o "$TOOL" ./tools/acctest-rerun || exit 1

# run_suite <events> <log> <go test args...>: the exit status is go test's.
run_suite() {
  local events="$1" log="$2"
  shift 2
  TF_ACC=1 go test -json ./internal/acctest/ "$@" 2>&1 | tee "$events" | "$TOOL" render | tee "$log"
  return "${PIPESTATUS[0]}"
}

run_suite "$EVENTS" "$LOG" -timeout "$TIMEOUT" -parallel 4 -run "$RUN_PATTERN"
rc=$?
if [ "$rc" -eq 0 ]; then
  exit 0
fi

reason_file="$(mktemp)"
pattern="$("$TOOL" classify "$EVENTS" 2>"$reason_file")"
classify_rc=$?
if [ "$classify_rc" -ne 0 ]; then
  {
    echo "### ${TITLE}: failed"
    echo ""
    echo "No hibernation rerun: $(sed "s/^no rerun: //" "$reason_file")"
  } >>"$SUMMARY"
  cat "$reason_file"
  exit "$rc"
fi

echo "==> Every failed test failed on cloud hibernation; waiting for the cloud, then re-running: ${pattern}"
{
  echo "### ${TITLE}: hibernation rerun"
  echo ""
  echo "First attempt failed only on cloud hibernation. Re-running once: \`${pattern}\`"
} >>"$SUMMARY"

if ! ANYSCALE_TEST_WAKE_GATE=1 run_suite "${LOG}.gate.jsonl" "${LOG}.gate" -timeout 15m -run '^TestAccWakeGate$'; then
  echo "" >>"$SUMMARY"
  echo "**Failed:** the wake gate failed (see TestAccWakeGate above), so the tests were not re-run." >>"$SUMMARY"
  exit "$rc"
fi
# go test also exits 0 when the gate skipped (no credentials) or the pattern
# matched nothing; either way the cloud was never checked.
if ! "$TOOL" passed "${LOG}.gate.jsonl" TestAccWakeGate; then
  echo "" >>"$SUMMARY"
  echo "**Failed:** the wake gate did not run (skipped or not found), so the tests were not re-run." >>"$SUMMARY"
  exit "$rc"
fi

ANYSCALE_TEST_RERUN=1 run_suite "${LOG}.rerun.jsonl" "${LOG}.rerun" -timeout "$TIMEOUT" -parallel 4 -run "$pattern"
rerun_rc=$?
echo "" >>"$SUMMARY"
if [ "$rerun_rc" -ne 0 ]; then
  echo "**Failed:** the rerun failed too." >>"$SUMMARY"
  exit "$rerun_rc"
fi
echo "**Recovered after hibernation rerun:** the re-run tests passed." >>"$SUMMARY"
echo "recovered=true" >>"$OUTPUT"
