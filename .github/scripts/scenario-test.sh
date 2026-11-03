#!/usr/bin/env bash
# End-to-end test of one examples/ scenario against real infrastructure. Backs every
# `make test-<scenario>` target.
#
# Usage: scenario-test.sh <provider-binary-path> <example-dir> <state-file>
#
# The run fails, with the failing step named, unless all of these hold:
#   1. apply succeeds;
#   2. a plan right after apply is empty (no perpetual diff);
#   3. after every anyscale_cloud and anyscale_cloud_resource is removed from a copy of the state
#      and imported back, the plan is still empty (import round-trip, including backend-derived
#      fields). The real state is never edited, so destroy and cleanup always see every cloud;
#   4. destroy succeeds;
#   5. every cloud the scenario created answers GET /api/v2/clouds/<id> with 404 within
#      SCENARIO_GONE_TIMEOUT seconds (default 120), since cloud delete does not wait.
#
# If any step fails, an EXIT trap still destroys whatever the state holds. A failed cleanup
# destroy is reported loudly; run `make sweep-dry-run` to find leaks.
#
# A scenario that must apply twice (for example, an EKS cluster whose Anyscale Operator can only
# be installed once the first apply has created its CRDs) sets SCENARIO_SECOND_APPLY_VARS to
# space-separated <variable>=<value> pairs. After the first apply they are exported as TF_VAR_*
# and applied again; every later step runs with them set.
#
# A scenario that creates no cloud (it targets an existing one) sets SCENARIO_NO_CLOUD=1: steps
# 3 and 5 are then skipped, and the run fails if the state does hold a cloud.
#
# Inputs come from the environment only: TF_VAR_* for the example's variables (no tfvars
# file), ANYSCALE_CLI_TOKEN or ~/.anyscale/credentials.json for the Anyscale API, and the
# platform's own credentials (AWS_*, GOOGLE_*) for its infrastructure. The API base URL is
# resolved like the provider resolves it: ANYSCALE_API_URL, ANYSCALE_API_HOST, ANYSCALE_HOST,
# then https://console.anyscale.com.
#
# The provider binary is pinned with a private TF_CLI_CONFIG_FILE dev_overrides, so the
# caller's ~/.terraformrc can never substitute a different build.
#
# Requires Terraform >= 1.15 (init skips registry lookup for a dev-overridden provider), jq
# and curl.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <provider-binary-path> <example-dir> <state-file>" >&2
  exit 2
fi

provider_bin=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
example_dir=$2
state=$3

if [ ! -x "$provider_bin" ]; then
  echo "provider binary not found or not executable: $provider_bin" >&2
  exit 2
fi
mkdir -p "$(dirname "$state")"
state=$(cd "$(dirname "$state")" && pwd)/$(basename "$state")

step() { printf '\n==> [%s] %s\n' "$(basename "$example_dir")" "$*"; }
fail() { printf '\nFAIL [%s] %s\n' "$(basename "$example_dir")" "$*" >&2; exit 1; }

tf_config=$(mktemp)
cat >"$tf_config" <<EOF
provider_installation {
  dev_overrides {
    "anyscale/anyscale" = "$(dirname "$provider_bin")"
  }
  direct {}
}
EOF
export TF_CLI_CONFIG_FILE=$tf_config
export TF_IN_AUTOMATION=1

destroyed=0
cleanup() {
  local status=$?
  if [ "$destroyed" -eq 0 ] && [ -s "$state" ]; then
    step "cleanup: destroying after a failed step"
    if ! terraform destroy -auto-approve -input=false -state="$state"; then
      printf '\nCLEANUP DESTROY FAILED for %s (state %s). Resources may have leaked; run make sweep-dry-run.\n' \
        "$example_dir" "$state" >&2
      status=1
    fi
  fi
  rm -f "$tf_config" "$state.import" "$state.import".*backup
  exit "$status"
}
trap cleanup EXIT

# expect_empty_plan <label> <state-file> fails unless `terraform plan` reports no changes.
# -detailed-exitcode returns 0 for an empty plan, 2 for a non-empty one, 1 for an error.
expect_empty_plan() {
  local rc=0
  terraform plan -detailed-exitcode -input=false -lock=false -state="$2" || rc=$?
  case "$rc" in
    0) ;;
    2) fail "$1: plan is not empty (see the plan above)" ;;
    *) fail "$1: plan errored (exit $rc)" ;;
  esac
}

api_base_url() {
  local v
  for v in ANYSCALE_API_URL ANYSCALE_API_HOST ANYSCALE_HOST; do
    if [ -n "${!v:-}" ]; then
      printf '%s' "${!v%/}"
      return
    fi
  done
  printf 'https://console.anyscale.com'
}

# api_token prints the Anyscale token without echoing it anywhere else.
api_token() {
  if [ -n "${ANYSCALE_CLI_TOKEN:-}" ]; then
    printf '%s' "$ANYSCALE_CLI_TOKEN"
    return
  fi
  local creds="$HOME/.anyscale/credentials.json"
  [ -r "$creds" ] || fail "no ANYSCALE_CLI_TOKEN and no $creds"
  jq -er '.cli_token // .token' "$creds" || fail "no token in $creds"
}

# cloud_http_status prints the HTTP status of GET /api/v2/clouds/<id>. The token goes to
# curl on stdin as a config line, never on the command line.
cloud_http_status() {
  printf 'header = "Authorization: Bearer %s"\n' "$token" |
    curl -sS -o /dev/null -w '%{http_code}' -K - "$(api_base_url)/api/v2/clouds/$1"
}

# Resolved up front so a missing token fails before any infrastructure exists.
token=$(api_token) || exit 1
[ -n "$token" ] || fail "empty Anyscale token"

cd "$example_dir"

step "init"
terraform init -input=false -upgrade=false >/dev/null || fail "init failed"

step "apply"
terraform apply -auto-approve -input=false -state="$state" || fail "apply failed"

if [ -n "${SCENARIO_SECOND_APPLY_VARS:-}" ]; then
  for pair in $SCENARIO_SECOND_APPLY_VARS; do
    case "$pair" in
      *=*) export "TF_VAR_${pair%%=*}=${pair#*=}" ;;
      *) fail "SCENARIO_SECOND_APPLY_VARS entry is not <variable>=<value>: $pair" ;;
    esac
  done
  step "second apply ($SCENARIO_SECOND_APPLY_VARS)"
  terraform apply -auto-approve -input=false -state="$state" || fail "second apply failed"
fi

step "plan after apply must be empty"
expect_empty_plan "plan after apply" "$state"

# Every managed anyscale_cloud and anyscale_cloud_resource, at any module depth, as
# "<type>\t<address>\t<id>", clouds first so each cloud resource imports after its cloud.
anyscale_resources=$(terraform show -json "$state" | jq -r '
  [.values.root_module | .. | objects | select(has("resources")) | .resources[]
   | select(.mode == "managed" and (.type == "anyscale_cloud" or .type == "anyscale_cloud_resource"))]
  | sort_by(.type)
  | .[] | "\(.type)\t\(.address)\t\(.values.id)"')
if [ "${SCENARIO_NO_CLOUD:-0}" = "1" ]; then
  [ -z "$anyscale_resources" ] || fail "SCENARIO_NO_CLOUD=1, but the state holds a cloud: $anyscale_resources"
  step "destroy (no cloud in this scenario: import round-trip and cloud check skipped)"
  terraform destroy -auto-approve -input=false -state="$state" || fail "destroy failed"
  destroyed=1
  step "PASS"
  exit 0
fi
[ -n "$anyscale_resources" ] || fail "state holds no anyscale_cloud or anyscale_cloud_resource"

cloud_ids=$(printf '%s\n' "$anyscale_resources" | awk -F'\t' '$1 == "anyscale_cloud" { print $3 }')

# The round-trip edits a copy, so a failed import cannot drop a cloud from the state that
# destroy and cleanup use.
import_state="$state.import" # removed again by the EXIT trap
cp "$state" "$import_state"

step "import round-trip: remove from a copy of the state"
while IFS=$'\t' read -r _ address _; do
  terraform state rm -state="$import_state" "$address" </dev/null || fail "state rm $address failed"
done <<<"$anyscale_resources"

step "import round-trip: import"
while IFS=$'\t' read -r _ address id; do
  terraform import -input=false -state="$import_state" "$address" "$id" </dev/null || fail "import $address $id failed"
done <<<"$anyscale_resources"

step "plan after import must be empty"
expect_empty_plan "plan after import" "$import_state"

step "destroy"
terraform destroy -auto-approve -input=false -state="$state" || fail "destroy failed"
destroyed=1

step "clouds must be gone"
gone_timeout=${SCENARIO_GONE_TIMEOUT:-120}
for id in $cloud_ids; do
  waited=0
  while code=$(cloud_http_status "$id") && [ "$code" != "404" ]; do
    [ "$waited" -lt "$gone_timeout" ] ||
      fail "cloud $id still answers GET with HTTP $code ${gone_timeout}s after destroy, want 404"
    sleep 10
    waited=$((waited + 10))
  done
  [ "$code" = "404" ] || fail "cloud $id: GET failed"
  echo "cloud $id: 404 after ${waited}s"
done

step "PASS"
