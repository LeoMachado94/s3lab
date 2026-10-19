# Configuration

Every setting can be given as an environment variable or a command-line flag. Flags win over environment variables.

```bash
s3lab [serve] [flags]     # default command
s3lab healthcheck         # exits 0 if the console health endpoint answers (used by Docker HEALTHCHECK)
s3lab version
```

## Reference

| Environment variable | Flag | Default | Description |
|---|---|---|---|
| `S3LAB_ADDR` | `--addr` | `:9000` | S3 API listen address |
| `S3LAB_CONSOLE_ADDR` | `--console-addr` | `:9001` | Console UI + admin API listen address |
| `S3LAB_CONSOLE_ENABLED` | `--console` | `true` | Set `false` to disable the console and admin API entirely |
| `S3LAB_ACCESS_KEY` | `--access-key` | `s3lab` | Access key clients must sign with |
| `S3LAB_SECRET_KEY` | `--secret-key` | `s3labsecret` | Secret key |
| `S3LAB_REGION` | `--region` | `us-east-1` | Region returned by `GetBucketLocation`. Requests signed for **any** region are accepted |
| `S3LAB_ANONYMOUS` | `--anonymous` | `false` | Accept unsigned requests (anonymous access to everything) |
| `S3LAB_STORAGE` | `--storage` | `fs` | `fs` (persistent on disk) or `memory` (lost on exit) |
| `S3LAB_DATA_DIR` | `--data-dir` | `/data` (image), `./data` (binary) | Root directory for the `fs` backend |
| `S3LAB_BUCKETS` | `--buckets` | — | Comma-separated buckets created at boot and after reset, e.g. `uploads,avatars` |
| `S3LAB_SEED_DIR` | `--seed-dir` | — | Directory copied into buckets at boot and after reset (see below) |
| `S3LAB_FAULTS` | `--faults` | — | JSON array of fault rules loaded at boot (see [testing](testing.md#fault-injection)) |
| `S3LAB_DOMAIN` | `--domain` | — | Enables virtual-host style: `<bucket>.<domain>`. Path-style keeps working |
| `S3LAB_CORS_ORIGINS` | `--cors-origins` | `*` | Comma-separated allowed origins for browser requests. Empty string disables CORS |
| `S3LAB_MAX_OBJECT_SIZE` | `--max-object-size` | `5GiB` | Max size of a single PUT or part. Accepts `B`, `KiB`, `MiB`, `GiB` |
| `S3LAB_REQUEST_LOG_SIZE` | `--request-log-size` | `1000` | Number of requests kept in the request log (ring buffer) |
| `S3LAB_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error` (`debug` logs every request) |
| `S3LAB_LOG_FORMAT` | `--log-format` | `text` | `text` or `json` |

### MinIO compatibility aliases

Used only when the `S3LAB_*` equivalent is not set:

| MinIO variable | Maps to |
|---|---|
| `MINIO_ROOT_USER`, `MINIO_ACCESS_KEY` | `S3LAB_ACCESS_KEY` |
| `MINIO_ROOT_PASSWORD`, `MINIO_SECRET_KEY` | `S3LAB_SECRET_KEY` |
| `MINIO_REGION`, `MINIO_SITE_REGION` | `S3LAB_REGION` |
| `MINIO_DOMAIN` | `S3LAB_DOMAIN` |

## Storage backends

| | `fs` | `memory` |
|---|---|---|
| Survives restart | ✅ (mount `/data`) | ❌ |
| Speed | fast | fastest |
| Object size limit | disk space | RAM |
| Best for | local development | CI, unit/integration tests |

> The `fs` backend uses its own layout under `/data` (file names are hashed so any S3 key is safe). Don't edit files there by hand; use the S3 API, the console or `S3LAB_SEED_DIR`.

## Seeding

`S3LAB_BUCKETS` creates empty buckets. `S3LAB_SEED_DIR` uploads files: every **top-level directory** becomes a bucket and every file inside becomes an object whose key is its relative path.

```
seed/
├── avatars/
│   └── default.png        → s3://avatars/default.png
└── documents/
    └── terms/v1.pdf       → s3://documents/terms/v1.pdf
```

```yaml
services:
  s3:
    image: leomachado94/s3lab
    environment:
      S3LAB_SEED_DIR: /seed
    volumes:
      - ./tests/fixtures/s3:/seed:ro
```

Content-Type is guessed from the file extension. Seeding runs at startup and on every `POST /_s3lab/reset`. Existing objects with the same key are overwritten.

## Security notes

s3lab is a development tool:

- The console/admin API (port 9001) has **no authentication**. Anyone who reaches it can read and delete data. Don't expose it publicly; in shared environments set `S3LAB_CONSOLE_ENABLED=false` or bind it to localhost (`S3LAB_CONSOLE_ADDR=127.0.0.1:9001`).
- Change the default credentials if the S3 port is reachable by others.
- There is no TLS. Put a reverse proxy in front if you need HTTPS.
