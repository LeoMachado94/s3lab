# s3lab

S3-compatible server for dev/testing, written in Go. Plan: `~/.claude/plans/minio-n-o-est-mais-snuggly-newell.md`.

## Rules
- Never run Go on the host. Use `make test`, `make test-race`, `make lint`, `make fmt`, `make tidy`
  (they run in Docker). Scope with `PKGS="./internal/foo/..."`.
- Layering: `storage` (port, no net/http) ← `storage/{fs,memory}` (adapters) ← `s3api` (HTTP).
  Domain errors are sentinels in `internal/storage/errors.go`; map to S3 codes only in `internal/s3api/s3err`.
- Per-request state lives in `s3op.Info` (context). Middlewares read/write it; never re-resolve the op.
- Admin API contract: `docs/admin-api.md`. Change it only together with console + UI.
- Every backend must pass `storagetest.Run`.
- Comments: only when the why is not obvious, one short line.
- Commits: Conventional Commits, no AI attribution / Co-Authored-By lines.
