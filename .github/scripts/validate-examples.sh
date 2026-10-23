#!/usr/bin/env bash
# terraform init + validate for every directory under examples/ that contains *.tf.
# Backs the init+validate stage of `make validate-examples`; also runs locally.
#
# Usage: validate-examples.sh <provider-binary-path>
#
# Each directory is validated in a throwaway mirror of examples/, never in place:
#   - ${path.module} files and relative module paths keep resolving, and an interrupted run
#     cannot leave .terraform/, lock files, or a shim .tf behind in the repo.
#   - A directory with no required_providers block (most per-resource doc snippets) would make
#     init resolve hashicorp/anyscale, which does not exist; the mirror copy gets a
#     zz_validate_shim.tf declaring anyscale/anyscale.
#   - The provider binary is pinned here with our own TF_CLI_CONFIG_FILE dev_overrides, so the
#     caller's TF_CLI_CONFIG_FILE / ~/.terraformrc can never substitute a different build.
#
# Environment (optional):
#   VALIDATE_EXAMPLES_SKIP  ';'-separated "<dir>|<reason>" entries exempt from validation.
#
# Requires Terraform >= 1.15 (init skips registry lookup for a dev-overridden provider).
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $0 <provider-binary-path>" >&2
  exit 2
fi

BINARY=$1
if [ ! -x "$BINARY" ]; then
  echo "validate-examples: provider binary not found or not executable: $BINARY" >&2
  exit 2
fi
BINARY_DIR=$(cd "$(dirname "$BINARY")" && pwd)

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cat >"$ROOT/terraformrc" <<EOF
provider_installation {
  dev_overrides {
    "registry.terraform.io/anyscale/anyscale" = "$BINARY_DIR"
  }
  direct {}
}
EOF
export TF_CLI_CONFIG_FILE="$ROOT/terraformrc"
export TF_PLUGIN_CACHE_DIR="$ROOT/plugin-cache"
export TF_IN_AUTOMATION=1
mkdir -p "$TF_PLUGIN_CACHE_DIR"

cp -R examples "$ROOT/examples"
find "$ROOT/examples" -name .terraform -type d -prune -exec rm -rf {} +
find "$ROOT/examples" -name .terraform.lock.hcl -type f -delete

# Same enumeration as the tflint loop in the Makefile.
dirs=$(find examples -name "*.tf" -not -path "*/.terraform/*" -exec dirname {} \; | sort -u)
total=$(printf '%s\n' "$dirs" | grep -c . || true)

skip_reason() {
  local entry dir reason
  local IFS=';'
  for entry in ${VALIDATE_EXAMPLES_SKIP:-}; do
    dir=$(echo "${entry%%|*}" | xargs)
    reason=$(echo "${entry#*|}" | xargs)
    if [ -n "$dir" ] && [ "$dir" = "$1" ]; then
      echo "$reason"
      return 0
    fi
  done
  return 1
}

# A stale exception (dir no longer exists) is an error, not a silent no-op.
IFS=';' read -r -a skip_entries <<<"${VALIDATE_EXAMPLES_SKIP:-}"
for entry in "${skip_entries[@]+"${skip_entries[@]}"}"; do
  dir=$(echo "${entry%%|*}" | xargs)
  [ -z "$dir" ] && continue
  if ! printf '%s\n' "$dirs" | grep -qxF "$dir"; then
    echo "validate-examples: VALIDATE_EXAMPLES_SKIP names $dir, which has no *.tf files" >&2
    exit 1
  fi
done

fail=0
validated=0
skipped=0
override_line=""
for d in $dirs; do
  if reason=$(skip_reason "$d"); then
    echo "SKIPPED ($reason): $d"
    skipped=$((skipped + 1))
    continue
  fi
  m="$ROOT/$d"
  if ! grep -q required_providers "$m"/*.tf; then
    cat >"$m/zz_validate_shim.tf" <<'EOF'
terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}
EOF
  fi
  rc=0
  out=$( (cd "$m" && terraform init -backend=false -input=false -no-color && terraform validate -no-color) 2>&1) || rc=$?
  if [ -z "$override_line" ]; then
    override_line=$(printf '%s\n' "$out" | grep -m1 'anyscale/anyscale in' || true)
  fi
  if [ $rc -ne 0 ]; then
    echo "FAILED: $d"
    printf '%s\n' "$out" | tail -20
    fail=1
  else
    echo "OK: $d"
  fi
  validated=$((validated + 1))
done

echo "provider override in effect:${override_line:- (none reported by terraform)}"
echo "validated $validated dirs ($skipped skipped, $total enumerated)"

if [ "$validated" -eq 0 ]; then
  echo "validate-examples: no directories validated" >&2
  exit 1
fi
if [ $((validated + skipped)) -ne "$total" ]; then
  echo "validate-examples: validated + skipped ($((validated + skipped))) != enumerated ($total)" >&2
  exit 1
fi
exit $fail
