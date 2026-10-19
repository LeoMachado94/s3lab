# S3 compatibility

s3lab implements the part of the S3 API that applications and SDKs use day to day. Anything else answers `501 NotImplemented` with a proper S3 XML error, so you notice instead of getting silent wrong behaviour.

## Supported operations

| Area | Operations |
|---|---|
| Service | `ListBuckets` |
| Buckets | `CreateBucket`, `DeleteBucket`, `HeadBucket`, `GetBucketLocation` |
| Listing | `ListObjectsV2` (prefix, delimiter, `max-keys`, `start-after`, continuation token, `encoding-type=url`, `fetch-owner`), `ListObjects` (v1, marker) |
| Objects | `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `DeleteObjects` (batch, quiet mode), `CopyObject` (`COPY`/`REPLACE` metadata directive, copy conditions) |
| Multipart | `CreateMultipartUpload`, `UploadPart`, `UploadPartCopy` (with `x-amz-copy-source-range`), `CompleteMultipartUpload`, `AbortMultipartUpload`, `ListParts`, `ListMultipartUploads` |
| Object ACLs | Canned ACLs (`x-amz-acl` on `PutObject`, `CopyObject`, `CreateMultipartUpload`, `PutObjectAcl`; or an `AccessControlPolicy` body) are stored and returned by `GetObjectAcl`, so visibility checks (Flysystem/Laravel `visibility()`) work. They are **not enforced**: access is decided by credentials / `S3LAB_ANONYMOUS` |
| CORS | Preflight `OPTIONS` and response headers, configured globally via `S3LAB_CORS_ORIGINS` |

### Object features

- `Range: bytes=a-b`, `bytes=a-`, `bytes=-n` (single range) → `206` with `Content-Range`; unsatisfiable → `416 InvalidRange`; malformed or multi-range headers are ignored (full object, `200`), as on AWS.
- ACLs: an unknown `x-amz-acl` value returns `400 InvalidArgument`; `PutObjectAcl` with neither header nor body returns `400 MissingSecurityHeader`; `PutObjectAcl` changes metadata only (ETag and LastModified stay the same).
- Conditional requests: `If-Match`, `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since` → `304` / `412`.
- Stored and returned: `Content-Type`, `Content-Encoding`, `Content-Disposition`, `Content-Language`, `Cache-Control`, `Expires`, `x-amz-meta-*`.
- `response-content-type`, `response-content-disposition` and other `response-*` overrides on `GetObject` (handy with presigned URLs).
- `Content-MD5` is verified (`400 BadDigest` on mismatch).
- ETag: MD5 of the content; multipart objects use the S3 format `<md5-of-md5s>-<parts>`. Minimum part size 5 MiB (except the last part), max 10 000 parts.
- Keys up to 1024 bytes, any UTF-8 (including `../`, `//`, trailing `/`).

### Authentication

- AWS Signature V4 in the `Authorization` header.
- Presigned URLs (query-string SigV4, up to 7 days).
- Payload modes: signed SHA-256, `UNSIGNED-PAYLOAD`, `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` (aws-chunked), and the trailer variants used by recent SDKs for checksums (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`).
- Requests signed for any region are accepted.
- Clock skew tolerance: 15 minutes (`RequestTimeTooSkewed` beyond).
- Signature V2 is **not** supported: in the `Authorization` header it returns `400 InvalidRequest`, in the query string `403 AccessDenied`.

### Addressing

- Path-style (`http://host:9000/bucket/key`), always.
- Virtual-host style (`http://bucket.s3.local:9000/key`) when `S3LAB_DOMAIN=s3.local` is set and the name resolves to s3lab.

## Not supported (yet)

| Feature | Behaviour |
|---|---|
| Versioning, object lock, retention, legal hold | `501 NotImplemented` |
| Object/bucket tagging | `?tagging` subresource: `501` (planned). The `x-amz-tagging` header on `PutObject` is accepted and ignored |
| Bucket policies, per-bucket ACLs, IAM, STS | `501`; a single access key has full access |
| Per-bucket CORS, lifecycle, replication, website, notifications | `501` (event webhooks planned) |
| Server-side encryption (SSE-S3/KMS/C) | headers ignored, data stored as is |
| Checksums (`x-amz-checksum-*`) | accepted, not validated or returned |
| Browser POST uploads (HTML form + policy) | `403 AccessDenied` when unsigned (auth runs first), `501` otherwise |
| `SelectObjectContent`, restore, torrent | `501` |
| Storage classes | accepted, always reported as `STANDARD` |

## Known differences from AWS

- `CreateBucket` on an existing bucket returns `409 BucketAlreadyOwnedByYou` (there is only one owner).
- `GetBucketLocation` returns `S3LAB_REGION`, whatever region the client signed with; for `us-east-1` the `LocationConstraint` is empty, like AWS.
- Owner ID and display name are always `s3lab`.
- Listing is consistent immediately after writes (AWS is too, since 2020).
