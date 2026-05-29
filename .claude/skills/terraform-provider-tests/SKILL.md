---
name: terraform-provider-tests
description: Coverage gap analysis and parallel acceptance test execution tooling for Terraform providers. Use when (1) running coverage gap analysis to find missing drift/import/idempotency tests, (2) running large acceptance test suites in parallel (4x-8x speedup over sequential `go test`), (3) doing fast compile-only verification after test edits, or (4) bootstrapping an example-validation script. For test patterns (statecheck, plancheck, scenario templates), use the official `provider-test-patterns` skill. For running a single TestAcc, use `run-acceptance-tests`.
---

# Terraform Provider Tests — Coverage & Parallel Execution Tooling

This skill provides automation tooling that complements the official HashiCorp pattern-reference skills. `provider-test-patterns` describes *what* tests should look like. This skill describes *how* to find which tests are missing and *how* to run them efficiently at scale.

## When invoked

Run coverage gap analysis and summarize:

```bash
python3 .claude/skills/terraform-provider-tests/scripts/analyze_gap.py ./internal/provider/ \
    --output ./ai_reports/tf_provider_tests_gap_$(date +%Y%m%d_%H%M%S).md
```

Then provide a **succinct** summary with:
- Overall grade (A/B/C)
- Top 3 findings
- Single recommended next action

Priority levels for findings:
- **P1 (Critical)**: Missing drift/import tests, heavy legacy usage (>20 calls per file)
- **P2 (Important)**: Missing idempotency, moderate legacy (5-20 calls per file)
- **P3 (Cleanup)**: Light legacy (<5 calls per file)

## Tools

### Coverage gap analyzer

`scripts/analyze_gap.py` — scans Go test files and produces a markdown report.

```bash
python3 .claude/skills/terraform-provider-tests/scripts/analyze_gap.py \
    <test_directory> [--output report.md] [--config .gap-analyzer.yaml]
```

Detects:
- Legacy `Check: resource.TestCheckResourceAttr()` patterns (with severity classification)
- Missing drift detection / import / idempotency tests
- Optional vs required field coverage
- ID consistency tracking issues (missing `CompareValue`)
- Modern pattern adoption percentage

Output: markdown report with executive summary (overall A/B/C grade), file-by-file analysis, and prioritized recommendations.

The `.gap-analyzer.yaml` at the skill root configures domain allowlists and severity rules. Edit it to tune false positives without changing the script.

**Recommended report path**: `./ai_reports/tf_provider_tests_gap_$(date +%Y%m%d_%H%M%S).md`. For one-time analysis use `./ai_reports/tf_provider_tests_gap.md`. For post-fix verification use `tf_provider_tests_final_<timestamp>.md`.

### Parallel test runner

`scripts/run_tests_parallel.sh` — runs acceptance tests concurrently per-file (4x-8x speedup vs sequential `go test`).

```bash
SCRIPT=.claude/skills/terraform-provider-tests/scripts/run_tests_parallel.sh

$SCRIPT                       # all TestAcc, 4 concurrent files
$SCRIPT -c 8                  # higher concurrency
$SCRIPT --resources-only      # resources only
$SCRIPT --data-sources-only   # data sources only
$SCRIPT -p "TestAccPattern"   # filter by name pattern
$SCRIPT -f resource_x_test.go # single file
$SCRIPT --verbose             # detailed logs per test
```

Run `$SCRIPT -h` for all options (`-d` dir, `-t` timeout, `-S` stagger, `-C` cleanup, `--no-color`).

For an auto-logging wrapper that runs in the background and tails the log: `scripts/run_tests_with_log.sh` (accepts the same flags).

**Requirements**: `TF_ACC=1`, valid provider credentials (`ANYSCALE_CLI_TOKEN` for this repo), GNU `parallel` recommended (falls back to `xargs`).

### Fast compile verification

`scripts/verify_compilation.sh` — runs `go test -c` without executing tests. Use after test edits to catch syntax/import errors in ~2-5 seconds.

```bash
.claude/skills/terraform-provider-tests/scripts/verify_compilation.sh ./internal/provider/
```

## Workflow

1. **Analyze** — run `analyze_gap.py`, read the report.
2. **Fix** — apply patterns from the `provider-test-patterns` skill (statecheck, plancheck, CompareValue, drift detection scenarios).
3. **Verify** — run `verify_compilation.sh` to catch syntax/import errors fast.
4. **Test** — run the full suite with `run_tests_parallel.sh`, or this repo's `make testacc` for sequential. For a single TestAcc, use the `run-acceptance-tests` skill.
5. **Re-analyze** — re-run `analyze_gap.py` with a `_final_` suffix to measure progress.

## Completion criteria

A modernized test file has:
- Zero legacy `Check` blocks (all assertions via `statecheck.ExpectKnownValue()`)
- Idempotency checks after Create and Update (`plancheck.ExpectEmptyPlan()`)
- Import test with `ImportStateVerify`
- Drift detection test (resources only)
- ID consistency tracking with `CompareValue` across all steps
- All tests compile and pass

## Examples-validation template

`templates/test-examples-template.sh` is a generic harness for running `terraform init/plan/apply/destroy` against each `examples/*/` directory. To use it: copy to `scripts/test-examples.sh` in the project, then customize `PROVIDER_NAME`, required env vars, and `cleanup_resources()`. Independent of the analyzer and parallel runner.

## Common pitfalls

- **`undefined: statecheck` / `undefined: plancheck`** — missing imports; see `provider-test-patterns` for the standard import block.
- **Type mismatch in `knownvalue` matcher** — String→`StringExact`, Bool→`Bool`, Int64→`Int64Exact`, computed UUID→`NotNull()`.
- **Duplicate validation** — same assertion in both `Check` and `ConfigStateChecks`. Remove `Check`, keep `ConfigStateChecks`.
- **`TF_ACC is not set`** — export `TF_ACC=1` before invoking the parallel runner.
- **`parallel: command not found`** — install via `brew install parallel`; the script falls back to `xargs` automatically.

## Cross-references

- **Test patterns** (TestCase/TestStep, statecheck, plancheck, scenarios, ephemeral resources): use `provider-test-patterns`
- **Single-test execution and diagnosis flags**: use `run-acceptance-tests`
- **Drift detection patterns, advanced state/plan checks, TDD workflow**: use `terraform-provider-design`
- **Terraform's native `.tftest.hcl` (module tests, not Go acctests)**: use `terraform-test`
