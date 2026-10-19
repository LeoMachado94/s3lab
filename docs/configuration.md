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
| `S3LAB_CONSOLE_ENABLED` | `--console` (`--console=false`) | `true` | Set `false` to disable the console and admin API entirely |
| `S3LAB_CONSOLE_AUTH` | `--console-auth` | `true` | Require login (S3 access key + secret key) for the console and admin API. `false` leaves them open. See [security notes](#security-notes) |
| `S3LAB_CONSOLE_ALLOWED_HOSTS` | `--console-allowed-hosts` | — | Comma-separated extra `Host` values the console/admin API accepts (see [security notes](#security-notes)) |
| `S3LAB_ACCESS_KEY` | `--access-key` | `s3lab` | Access key clients must sign with |
| `S3LAB_SECRET_KEY` | `--secret-key` | `s3labsecret` | Secret key |
| `S3LAB_REGION` | `--region` | `us-east-1` | Region returned by `GetBucketLocation`. Requests signed for **any** region are accepted |
| `S3LAB_ANONYMOUS` | `--anonymous` | `false` | Accept unsigned requests (anonymous access to everything, **including writes and deletes**) |
| `S3LAB_STORAGE` | `--storage` | `fs` | `fs` (persistent on disk) or `memory` (lost on exit) |
| `S3LAB_DATA_DIR` | `--data-dir` | `/data` (image), `./data` (binary) | Root directory for the `fs` backend |
| `S3LAB_BUCKETS` | `--buckets` | — | Comma-separated buckets created at boot and after reset, e.g. `uploads,avatars` |
| `S3LAB_SEED_DIR` | `--seed-dir` | — | Directory copied into buckets at boot and after reset (see below) |
| `S3LAB_FAULTS` | `--faults` | — | JSON array of fault rules loaded at boot (see [testing](testing.md#fault-injection)) |
| `S3LAB_DOMAIN` | `--domain` | — | Enables virtual-host style: `<bucket>.<domain>`. Path-style keeps working |
| `S3LAB_CORS_ORIGINS` | `--cors-origins` | `*` | Comma-separated allowed origins for browser requests. Empty string disables CORS |
| `S3LAB_MAX_OBJECT_SIZE` | `--max-object-size` | `5GiB` | Max size of a single PUT or part; also caps uploads through the console/admin API (`413 EntityTooLarge`). See [sizes](#validation-and-flags) |
| `S3LAB_REQUEST_LOG_SIZE` | `--request-log-size` | `1000` | Number of requests kept in the request log (ring buffer, max `100000`) |
| `S3LAB_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error` (`debug` logs every request). Logs go to stderr |
| `S3LAB_LOG_FORMAT` | `--log-format` | `text` | `text` or `json` |

### Validation and flags

- Startup fails if the access key or secret key is empty, unless `S3LAB_ANONYMOUS=true`. The region must be non-empty. `S3LAB_REQUEST_LOG_SIZE` accepts at most `100000`.
- Boolean flags accept `--console=false` and `--console false`.
- Sizes (`S3LAB_MAX_OBJECT_SIZE`): `B`, `KiB`, `MiB`, `GiB`, `TiB` (1024-based), `KB`, `MB`, `GB`, `TB` (1000-based) or plain bytes; case-insensitive.
- `s3lab healthcheck` probes the console health endpoint (`127.0.0.1`, then `[::1]`, on the port from `S3LAB_CONSOLE_ADDR`). With the console disabled it probes `GET /minio/health/live` on the S3 port. It reads **environment variables only**, not CLI flags, so in Docker set ports via env (`S3LAB_CONSOLE_ADDR`), not `--console-addr`.
- Logs are written to stderr.

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
    image: leomachado/s3lab
    environment:
      S3LAB_SEED_DIR: /seed
    volumes:
      - ./tests/fixtures/s3:/seed:ro
```

Content-Type is guessed from the file extension. Seeding runs at startup and on every `POST /_s3lab/reset`. Existing objects with the same key are overwritten.

Seeding rules:

- Files directly under the seed dir (not inside a bucket directory) are ignored.
- `.DS_Store` and `Thumbs.db` are skipped.
- Symlinks that escape their bucket directory are skipped.
- A top-level directory whose name is not a valid bucket name aborts startup.

## Security notes

s3lab is a development tool:

- The console/admin API (port 9001) **requires login** with the S3 access key and secret key (HTTP Basic auth or the console's session cookie). Change the defaults (`s3lab` / `s3labsecret`) whenever the port is reachable by others. It is still a dev tool: plain HTTP, no rate limiting beyond a 1 s delay per failed attempt. Don't expose it publicly; in shared environments also bind it to localhost or set `S3LAB_CONSOLE_ENABLED=false`.
- `S3LAB_CONSOLE_AUTH=false` removes the login. Only do that when the port is published on loopback only.
- Binding to localhost (`S3LAB_CONSOLE_ADDR=127.0.0.1:9001`) is for the bare binary. In Docker, bind inside the container as usual and publish the port on loopback only: `-p 127.0.0.1:9001:9001`.
- **Host allowlist (DNS-rebinding protection).** The console/admin API only answers `Host` values that are IP literals, `localhost` / `*.localhost`, single-label names (Docker service names such as `s3`), `*.local`, `*.test`, `*.internal`, or listed in `S3LAB_CONSOLE_ALLOWED_HOSTS`. Anything else gets `403 Forbidden`.
- **Cross-origin rule.** Non-GET requests carrying an `Origin` header whose host differs from `Host` get `403 Forbidden`.
- Change the default credentials if the S3 port is reachable by others.
- There is no TLS. Put a reverse proxy in front if you need HTTPS.
