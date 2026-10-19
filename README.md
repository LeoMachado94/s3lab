# s3lab

**A tiny S3-compatible server built for development and integration tests.**
Drop-in replacement for MinIO in your `docker compose` and CI, plus the tools you actually need when testing S3 code: a live request log, fault injection, one-call reset, and seeding.

[Português (pt-BR)](docs/pt-BR/README.md)

```bash
docker run --rm -p 9000:9000 -p 9001:9001 -e S3LAB_BUCKETS=dev leomachado94/s3lab
```

- S3 API: `http://localhost:9000` — access key `s3lab`, secret `s3labsecret`, region `us-east-1`
- Console: `http://localhost:9001`

```bash
aws --endpoint-url http://localhost:9000 s3 cp ./photo.jpg s3://dev/photo.jpg
```

## Why s3lab

| | s3lab | MinIO |
|---|---|---|
| Image size | ~15 MB, single static binary | ~100+ MB |
| Startup | < 100 ms | seconds |
| In-memory mode | ✅ `S3LAB_STORAGE=memory` | ❌ |
| Request log with JSON API (assert in tests) | ✅ | ❌ |
| Fault injection (errors, latency, throttling) | ✅ | ❌ |
| Reset state between tests in one HTTP call | ✅ | ❌ |
| Seed buckets/files at boot | ✅ | partial |
| License | MIT | AGPLv3 |

s3lab is **not** production storage: no replication, no erasure coding, no IAM policies. Use it where you'd use a mock, and get real S3 behaviour instead.

## Features

- **S3 API** used by real apps: buckets, objects, ranges, conditional requests, copy, multipart upload, batch delete, presigned URLs, CORS, SigV4 (header, presigned and streaming/chunked payloads). See [compatibility](docs/s3-compatibility.md).
- **Two backends**: `fs` (persistent, default) and `memory` (fast, ephemeral; perfect for CI).
- **Web console**: browse buckets, upload/download, inspect metadata, watch requests live, manage fault rules.
- **Request log**: every S3 call recorded with operation, status, error code, latency and bytes, queryable via `GET /_s3lab/requests`.
- **Fault injection**: make `PutObject` return `503 SlowDown` 30% of the time, add 2 s latency to `GetObject`, fail the first 3 requests… to test retries and timeouts.
- **Seed & reset**: create buckets and upload files at boot; restore that state with `POST /_s3lab/reset`.
- **MinIO env aliases**: `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` work, so migrating is a one-line image change.

## Documentation

| Guide | What's inside |
|---|---|
| [Getting started](docs/getting-started.md) | Run with Docker, Compose, binary; first bucket and object |
| [Configuration](docs/configuration.md) | Every environment variable and flag |
| [Client setup](docs/clients.md) | AWS CLI, Go, Python, Node.js, PHP/Laravel, Java, .NET, Ruby |
| [Testing guide](docs/testing.md) | CI (GitHub Actions, GitLab), reset between tests, asserting requests, fault injection recipes |
| [Web console](docs/console.md) | Tour of the UI |
| [Admin API](docs/admin-api.md) | `/_s3lab/*` endpoints reference |
| [S3 compatibility](docs/s3-compatibility.md) | Supported operations and known differences |
| [Migrating from MinIO](docs/migrating-from-minio.md) | Side-by-side compose, env mapping |
| [Troubleshooting](docs/troubleshooting.md) | `SignatureDoesNotMatch`, DNS-style buckets, CORS and more |

## Quick compose

```yaml
services:
  s3:
    image: leomachado94/s3lab
    ports: ["9000:9000", "9001:9001"]
    environment:
      S3LAB_BUCKETS: uploads,avatars
    volumes:
      - s3data:/data
volumes:
  s3data:
```

## License

MIT — see [LICENSE](LICENSE).
