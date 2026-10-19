# Migrating from MinIO

In most projects it's a one-line change: swap the image. MinIO's credential variables keep working.

## Docker Compose

Before:

```yaml
services:
  minio:
    image: minio/minio
    command: server /data --console-address ":9001"
    ports: ["9000:9000", "9001:9001"]
    environment:
      MINIO_ROOT_USER: admin
      MINIO_ROOT_PASSWORD: password
    volumes:
      - minio-data:/data

  createbuckets:
    image: minio/mc
    depends_on: [minio]
    entrypoint: >
      /bin/sh -c "
      mc alias set local http://minio:9000 admin password &&
      mc mb -p local/uploads &&
      mc anonymous set download local/uploads"
```

After:

```yaml
services:
  minio:                       # keep the service name so app config doesn't change
    image: leomachado/s3lab
    ports: ["9000:9000", "9001:9001"]
    environment:
      MINIO_ROOT_USER: admin
      MINIO_ROOT_PASSWORD: password
      S3LAB_BUCKETS: uploads   # replaces the mc init container
    volumes:
      - minio-data:/data
```

- No `command:` needed. If you keep `command: server /data ...`, s3lab ignores the MinIO arguments and logs a warning.
- Delete the `mc` init container: use `S3LAB_BUCKETS` / `S3LAB_SEED_DIR`.
- Ports are the same: `9000` API, `9001` console.

## Environment mapping

| MinIO | s3lab |
|---|---|
| `MINIO_ROOT_USER` / `MINIO_ACCESS_KEY` | `S3LAB_ACCESS_KEY` (alias honoured) |
| `MINIO_ROOT_PASSWORD` / `MINIO_SECRET_KEY` | `S3LAB_SECRET_KEY` (alias honoured) |
| `MINIO_REGION` / `MINIO_SITE_REGION` | `S3LAB_REGION` (alias honoured) |
| `MINIO_DOMAIN` | `S3LAB_DOMAIN` (alias honoured) |
| `--console-address :9001` | `S3LAB_CONSOLE_ADDR=:9001` (default) |
| `mc mb local/bucket` | `S3LAB_BUCKETS=bucket` |
| `mc cp -r ./fixtures local/bucket` | `S3LAB_SEED_DIR` |
| `mc anonymous set download` | `S3LAB_ANONYMOUS=true` (global, not per bucket; **also allows anonymous writes and deletes**) |
| `/minio/health/live` | `GET :9001/_s3lab/health` or `s3lab healthcheck` |

## Healthchecks

MinIO setups often use `curl -f http://localhost:9000/minio/health/live`. The s3lab image has no shell or curl (distroless); use the built-in command:

```yaml
healthcheck:
  test: ["CMD", "/s3lab", "healthcheck"]
  interval: 2s
  retries: 15
```

s3lab also answers `GET /minio/health/live` and `/minio/health/ready` on the S3 port with `200`, so external checks keep working. Only these two are implemented (not `/minio/health/cluster`).

## Data

The on-disk formats are different: s3lab cannot read a MinIO `/data` directory. Start with a fresh volume, or copy data over while both run:

```bash
# with both servers up (MinIO on 9000, s3lab on 9100)
aws --endpoint-url http://localhost:9000 s3 sync s3://uploads ./export
aws --endpoint-url http://localhost:9100 s3 sync ./export s3://uploads
```

## What you lose

- MinIO-specific APIs (`mc admin`, bucket notifications, ILM, policies, versioning). See [compatibility](s3-compatibility.md).
- Per-bucket anonymous policies: s3lab's `S3LAB_ANONYMOUS` is all-or-nothing, and unlike `mc anonymous set download` (read-only) it also allows anonymous writes and deletes.

## What you gain

- [Request log](testing.md#asserting-s3-requests) and [fault injection](testing.md#fault-injection) for tests.
- `S3LAB_STORAGE=memory` and one-call [reset](testing.md#reset-between-tests).
- ~17 MB image, near-instant startup, MIT license.
