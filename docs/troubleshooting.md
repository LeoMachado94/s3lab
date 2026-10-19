# Troubleshooting

First stop for any problem: open <http://localhost:9001/#/requests> (or `curl -u s3lab:s3labsecret localhost:9001/_s3lab/requests?errors=true`) and look at the failing request's `op`, `status` and `errorCode`. If the request isn't there at all, your client is not reaching s3lab.

## `SignatureDoesNotMatch`

The signature covers the method, path, query, host and signed headers. Usual causes:

| Cause | Fix |
|---|---|
| Wrong secret key | Check `S3LAB_SECRET_KEY` (or `MINIO_ROOT_PASSWORD`) matches the client |
| A proxy rewrites the `Host` header or path | Make the proxy forward `Host` unchanged and not re-encode paths |
| URL host changed after signing | Don't rewrite the host of presigned URLs; see below |
| Client signs a body that is altered (gzip middleware, etc.) | Sign the final body, or use `UNSIGNED-PAYLOAD` |

## `InvalidAccessKeyId`

The access key isn't the one configured. Defaults are `s3lab` / `s3labsecret`. Watch for SDKs picking credentials from `~/.aws/credentials` or `AWS_PROFILE` instead of your test config.

## `RequestTimeTooSkewed`

Client and server clocks differ by more than 15 minutes. Common with Docker Desktop after the laptop sleeps: restart Docker Desktop or sync the VM clock.

## Virtual-host style without `S3LAB_DOMAIN`: wrong results, `NoSuchBucket`, `InvalidBucketName`

The SDK is using virtual-host style and sends `GET /key` to `my-bucket.localhost`. Without `S3LAB_DOMAIN`, s3lab ignores the `Host` header and reads the path as `bucket/key`, so `key` is taken as the bucket name. The symptom is **not** `SignatureDoesNotMatch`: you see empty or wrong results, `NoSuchBucket`, `InvalidBucketName`, or an unexpected bucket being created.

Enable path-style (`UsePathStyle`, `forcePathStyle`, `use_path_style_endpoint`, `addressing_style: path`; see the [client setup](clients.md)), or configure `S3LAB_DOMAIN` and make `*.your-domain` resolve to s3lab.

## `401 Unauthorized` (console / admin API)

The console and admin API need the S3 credentials. In the browser, log in with `S3LAB_ACCESS_KEY` / `S3LAB_SECRET_KEY`. In scripts, send them as HTTP Basic auth: `curl -u <access key>:<secret key> ...`. To turn login off for a loopback-only setup, set `S3LAB_CONSOLE_AUTH=false`. See [admin API authentication](admin-api.md#authentication).

## `403 Forbidden` host not allowed (console)

The console/admin API rejects `Host` values it doesn't recognise (DNS-rebinding protection), e.g. a custom domain behind a reverse proxy. Add the host to `S3LAB_CONSOLE_ALLOWED_HOSTS` (comma-separated). See [security notes](configuration.md#security-notes).

## Presigned URLs generated inside Docker don't open in the browser

Your app signs URLs with the endpoint it uses, e.g. `http://s3:9000/...`. The browser can't resolve `s3`, and changing the host afterwards breaks the signature.

Options:

1. **Use one hostname that works from both sides.** For example, sign with `http://localhost:9000` and run the app with `network_mode: host` (Linux), or add an alias:
   ```yaml
   services:
     app:
       extra_hosts:
         - "s3.localhost:host-gateway"
       environment:
         AWS_ENDPOINT_URL: http://s3.localhost:9000   # Laravel: AWS_ENDPOINT
   ```
   and open `http://s3.localhost:9000/...` in the browser (`*.localhost` resolves to 127.0.0.1 in modern browsers).
2. **Use a second client just for presigning**, configured with the public endpoint (`http://localhost:9000`). Signing is offline, so it doesn't need to reach the server.

## Browser upload blocked by CORS

CORS is open by default. If you set `S3LAB_CORS_ORIGINS`, include the exact origin (scheme + host + port, e.g. `http://localhost:5173`). The preflight shows up in the request log as `CORSPreflight`.

## `EntityTooSmall` on `CompleteMultipartUpload`

Every part except the last must be at least 5 MiB, as on AWS. Increase your part size.

## `EntityTooLarge`

The single PUT/part is bigger than `S3LAB_MAX_OBJECT_SIZE` (default 5 GiB). Use multipart or raise the limit.

## `XAmzContentSHA256Mismatch` / `IncompleteBody`

The body received differs from what the client declared: often a proxy buffering/truncating uploads, or a client bug sending the wrong `Content-Length`.

## `NotImplemented`

The client called an operation s3lab doesn't support (versioning, tagging, policies…). The request log shows `op: Unsupported` with the method and query. See [compatibility](s3-compatibility.md); open an issue if your framework needs it.

## Recent AWS SDKs and checksums

Since 2025 AWS SDKs add CRC checksums and send `STREAMING-UNSIGNED-PAYLOAD-TRAILER` bodies by default. s3lab supports this. If an old proxy in between chokes on chunked bodies, set on the client:

```bash
AWS_REQUEST_CHECKSUM_CALCULATION=when_required
AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
```

## Data disappears on restart

You're on `S3LAB_STORAGE=memory`, or `/data` isn't mounted as a volume. Use `-v s3lab-data:/data` with the default `fs` storage.

## Permission denied on `/data`

The image runs as a non-root user (UID `65532`). When bind-mounting a host directory, make it writable for that UID:

```bash
mkdir -p ./s3data && sudo chown 65532:65532 ./s3data
```

Named volumes (`-v s3lab-data:/data`) work without this.

## Getting more detail

- `S3LAB_LOG_LEVEL=debug` logs every request with its outcome and `requestId` to stderr (`docker logs s3lab`).
- Every response has an `x-amz-request-id` header; search for it in the request log or the logs.
