# Fixtures

`run-<id>.txt` is the `go test -v` output of the failed "Acceptance Tests - Resources" step of
scheduled-acctest run `<id>` (2026-10-15 and 2026-10-22), each failing only on cloud hibernation.
Tests convert them to `go test -json` streams with `go tool test2json`.

Every Anyscale ID (`<prefix>_<26 chars>`) is replaced with `<prefix>_REDACTED`, so the test
cloud's ID is not committed. To add a run:

```bash
gh run view <id> --log-failed   # keep only the go test output of the failing step
perl -pe 's/\b([a-z]+)_[a-z0-9]{26}\b/$1_REDACTED/g'
```
