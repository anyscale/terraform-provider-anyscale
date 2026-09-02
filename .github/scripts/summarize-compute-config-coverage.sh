#!/usr/bin/env bash
# Summarize which cloud providers Compute Config's acceptance tests actually
# exercised into the GitHub Actions step summary, so a green run can't be
# mistaken for full AWS/GCP/Kubernetes coverage.
#
# Unlike the real-infra skips summarize-acctest-skips.sh counts, this gap has
# no `--- SKIP:` line at all: TestAccComputeConfigResource_Basic and
# TestAccComputeConfigResource_K8S iterate whatever GetAllVMClouds/
# GetAllK8sClouds discovered in the org and just run fewer subtests than the
# full {AWS, GCP, K8S} taxonomy when a provider's cloud is missing - the test
# still reports PASS, just for a matrix with a silently missing dimension.
# helpers.go already logs one loud, greppable line per provider actually
# selected (or explicitly not found); this script turns that into a summary
# nobody has to scroll a full -v log to notice.
#
# Usage: summarize-compute-config-coverage.sh <go-test-log-file> <section-title>
set -euo pipefail

LOG_FILE="$1"
TITLE="$2"

# Only meaningful if this run actually invoked the compute-config provider
# matrix tests; otherwise there is nothing to report.
if ! grep -qE 'TestAccComputeConfigResource_(Basic|K8S)' "$LOG_FILE"; then
  exit 0
fi

has_aws=0
has_gcp=0
has_k8s=0
grep -q 'Selected AWS VM cloud for testing' "$LOG_FILE" && has_aws=1
grep -q 'Selected GCP VM cloud for testing' "$LOG_FILE" && has_gcp=1
grep -q 'Selected K8S cloud for testing' "$LOG_FILE" && has_k8s=1

missing=()
[ "$has_aws" -eq 1 ] || missing+=("AWS")
[ "$has_gcp" -eq 1 ] || missing+=("GCP")
[ "$has_k8s" -eq 1 ] || missing+=("Kubernetes")

{
  echo "### ${TITLE}: Compute Config provider coverage"
  echo ""
  echo "| Provider | Exercised this run |"
  echo "|---|---|"
  echo "| AWS | $([ "$has_aws" -eq 1 ] && echo '✅ yes' || echo '❌ no') |"
  echo "| GCP | $([ "$has_gcp" -eq 1 ] && echo '✅ yes' || echo '❌ no') |"
  echo "| Kubernetes | $([ "$has_k8s" -eq 1 ] && echo '✅ yes' || echo '❌ no') |"
  if [ "${#missing[@]}" -gt 0 ]; then
    missing_joined=$(IFS=,; echo "${missing[*]}" | sed 's/,/, /g')
    echo ""
    echo "> ⚠️ **This run did NOT exercise Compute Config on: ${missing_joined}.** The provider matrix tests (\`TestAccComputeConfigResource_Basic\`, \`TestAccComputeConfigResource_K8S\`) skip a provider cleanly when this test org has no cloud of that type - they still report PASS, so this callout exists specifically so that PASS is never read as \"all three providers verified.\" See \`internal/acctest/helpers.go\`'s \`GetAllVMClouds\`/\`GetAllK8sClouds\` and this repo's known CI-org fixture gap (one AWS cloud only, as of this writing)."
  fi
} >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
