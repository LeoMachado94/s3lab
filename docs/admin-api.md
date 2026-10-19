# Admin API

The admin API powers the web console and lets tests inspect and control s3lab without S3 signing. It is served on the console port (default `9001`), separate from the S3 port (`9000`).

> **Login required.** Every endpoint except `/_s3lab/health`, `/_s3lab/login` and `/_s3lab/logout` needs the S3 credentials (`S3LAB_ACCESS_KEY` / `S3LAB_SECRET_KEY`). See [authentication](#authentication).

## Authentication

Two ways to authenticate, both with the S3 access key and secret key:

- **HTTP Basic auth**, for scripts and tests: `curl -u s3lab:s3labsecret localhost:9001/_s3lab/requests`. Go, Python `requests` and Guzzle also accept the credentials inside the URL (`http://s3lab:s3labsecret@localhost:9001`).
- **Session cookie**, used by the web console: `POST /_s3lab/login` with `{"accessKey": "...", "secretKey": "..."}` returns `204` and sets an `HttpOnly`, `SameSite=Strict` cookie valid for 12 hours. `POST /_s3lab/logout` clears it. Sessions end when s3lab restarts.

Missing or wrong credentials return `401 Unauthorized` (after a 1 s delay, to slow down guessing). No `WWW-Authenticate` header is sent, so browsers don't show a native login popup.

Set `S3LAB_CONSOLE_AUTH=false` to turn login off (the pre-0.2 behaviour); `/_s3lab/info` reports `"auth": false` then.

## Conventions

- JSON in and out, UTF-8, `Content-Type: application/json`.
- Timestamps: RFC 3339 with milliseconds (`2026-10-19T12:00:00.000Z`).
- Errors: matching HTTP status plus `{"error": {"code": "NoSuchBucket", "message": "..."}}`. S3 codes are used when one fits (`NoSuchBucket`, `NoSuchKey`, `BucketNotEmpty`, `InvalidBucketName`, `BucketAlreadyOwnedByYou`), otherwise `BadRequest`, `NotFound`, `Conflict`, `InternalError`.
- Object keys always go in the `key=` query parameter (URL-encoded), never in the path.
- The web console is served at `/`; any `GET`/`HEAD` outside `/_s3lab/` falls back to `index.html`. Other methods there return `405 MethodNotAllowed`.
- `403 Forbidden` is returned for a `Host` that is not allowed and for non-GET requests whose `Origin` host differs from `Host` (see [security notes](configuration.md#security-notes)).
- `PUT` object above `S3LAB_MAX_OBJECT_SIZE` returns `413 EntityTooLarge`. `maxKeys` below 1 or non-numeric returns `400`.

| Method | Path | Purpose |
|---|---|---|
| GET | [`/_s3lab/health`](#health-and-info) | Liveness |
| GET | [`/_s3lab/info`](#health-and-info) | Version, ports, storage, access key |
| POST | [`/_s3lab/login`](#authentication) | Console login (session cookie) |
| POST | [`/_s3lab/logout`](#authentication) | End the session |
| GET, POST | [`/_s3lab/buckets`](#buckets) | List / create buckets |
| DELETE | [`/_s3lab/buckets/{bucket}`](#buckets) | Delete bucket (`?force=true`) |
| GET, DELETE | [`/_s3lab/buckets/{bucket}/objects`](#objects) | List / delete by prefix |
| GET, PUT, DELETE | [`/_s3lab/buckets/{bucket}/object?key=`](#objects) | Object metadata / upload / delete |
| GET | [`/_s3lab/buckets/{bucket}/download?key=`](#objects) | Object body |
| GET, DELETE | [`/_s3lab/requests`](#request-log) | Query / clear request log |
| GET | [`/_s3lab/requests/stream`](#request-log) | Live request log (SSE) |
| GET, POST, DELETE | [`/_s3lab/faults`](#fault-injection) | List / add / clear fault rules |
| DELETE | [`/_s3lab/faults/{id}`](#fault-injection) | Remove one rule |
| POST | [`/_s3lab/reset`](#reset) | Reset state |

## Health and info

`GET /_s3lab/health` → `200`

```json
{"status": "ok", "version": "0.1.0"}
```

`GET /_s3lab/info` → `200`

```json
{
  "version": "0.1.0",
  "region": "us-east-1",
  "storage": "fs",
  "s3Port": 9000,
  "consolePort": 9001,
  "accessKey": "s3lab",
  "virtualHostDomain": "",
  "requestLogCapacity": 1000,
  "auth": true,
  "startedAt": "2026-10-19T12:00:00.000Z"
}
```

The secret key is never returned; the console shows a placeholder for it.

## Buckets

`GET /_s3lab/buckets` → `200`

```json
{"buckets": [{"name": "photos", "createdAt": "2026-10-19T12:00:00.000Z"}]}
```

`POST /_s3lab/buckets` with body `{"name": "photos"}` → `201` `{"name": "photos", "createdAt": "..."}`

`DELETE /_s3lab/buckets/{bucket}` → `204`. With `?force=true` objects and pending uploads are deleted first; without it, a non-empty bucket returns `409 BucketNotEmpty`.

## Objects

`GET /_s3lab/buckets/{bucket}/objects?prefix=&delimiter=/&startAfter=&maxKeys=200` → `200`

```json
{
  "objects": [
    {"key": "a/b.jpg", "size": 1234, "etag": "9e107d9d372bb6826bd81d3542a419d6", "lastModified": "...", "contentType": "image/jpeg"}
  ],
  "prefixes": ["a/sub/"],
  "isTruncated": false,
  "nextStartAfter": ""
}
```

`delimiter` defaults to `/` (send `delimiter=` empty for a flat listing). `maxKeys` defaults to 200, max 1000; below 1 or non-numeric is `400`. Pass `nextStartAfter` back as `startAfter` for the next page.

`GET /_s3lab/buckets/{bucket}/object?key=a/b.jpg` → `200`

```json
{
  "key": "a/b.jpg",
  "size": 1234,
  "etag": "9e107d9d372bb6826bd81d3542a419d6",
  "lastModified": "...",
  "partsCount": 0,
  "contentType": "image/jpeg",
  "headers": {"Cache-Control": "max-age=60"},
  "metadata": {"owner": "leo"}
}
```

`headers` contains only non-empty values among `Content-Encoding`, `Content-Disposition`, `Content-Language`, `Cache-Control`, `Expires`. `metadata` holds `x-amz-meta-*` values without the prefix.

`GET /_s3lab/buckets/{bucket}/download?key=a/b.jpg` → `200` raw body with the object's `Content-Type` and `Content-Disposition: attachment; filename="b.jpg"`. Add `?inline=1` for `inline` (used for previews).

`PUT /_s3lab/buckets/{bucket}/object?key=a/b.jpg` with the raw body. The request `Content-Type` becomes the object's; `x-amz-meta-*` headers become metadata → `201` with the same JSON as `GET .../object`.

`DELETE /_s3lab/buckets/{bucket}/object?key=a/b.jpg` → `204`.

`DELETE /_s3lab/buckets/{bucket}/objects?prefix=tmp/` → `200` `{"deleted": 12}`. An empty prefix empties the bucket.

## Request log

Only requests to the S3 port are recorded, except `/minio/health/*` probes, which are not logged.

`GET /_s3lab/requests?bucket=&op=&key=&status=&errors=true&since=<id>&limit=100` → `200`

```json
{
  "entries": [
    {
      "id": 42,
      "requestId": "8F3A1C2B4D5E6F70",
      "time": "2026-10-19T12:00:00.000Z",
      "method": "PUT",
      "path": "/photos/a.jpg",
      "query": "",
      "op": "PutObject",
      "bucket": "photos",
      "key": "a.jpg",
      "status": 200,
      "errorCode": "",
      "durationMs": 3.21,
      "bytesIn": 1234,
      "bytesOut": 0,
      "accessKey": "s3lab",
      "auth": "header",
      "faultId": "",
      "remoteAddr": "172.18.0.1:53422",
      "userAgent": "aws-sdk-go-v2/1.30.0"
    }
  ]
}
```

| Parameter | Effect |
|---|---|
| `bucket` | Exact bucket |
| `op` | Exact operation name (see [S3 compatibility](s3-compatibility.md)) |
| `key` | Key prefix |
| `status` | Exact HTTP status |
| `errors=true` | Only status ≥ 400 |
| `since` | Only entries with `id > since` (incremental polling) |
| `limit` | Keep the newest N matches |

- Entries are ordered oldest → newest. IDs keep increasing even after a clear.
- `auth` is `header`, `presigned`, `failed`, or empty for anonymous.
- Empty fields may be omitted (`query`, `bucket`, `key`, `errorCode`, `accessKey`, `auth`, `faultId`, `remoteAddr`, `userAgent`).

`DELETE /_s3lab/requests` → `204`.

`GET /_s3lab/requests/stream` → `text/event-stream` (Server-Sent Events). Accepts the same filters as `GET /_s3lab/requests` except `limit`. With `since=<id>` (or a `Last-Event-ID` header, which wins), matching entries with a greater `id` still in the buffer are replayed first, then live entries follow. Clients can fill a view with `GET /_s3lab/requests?limit=N` and open the stream with `since=<last id>` without gaps.

```
event: request
id: 42
data: {...entry...}

: ping
```

A `: ping` comment is sent every 15 s.

```bash
curl -u s3lab:s3labsecret -N localhost:9001/_s3lab/requests/stream?errors=true
```

## Fault injection

Rule:

```json
{
  "id": "slow-puts",
  "target": "http",
  "op": "PutObject",
  "bucket": "photos",
  "key": "uploads/*",
  "status": 503,
  "code": "SlowDown",
  "message": "Please reduce your request rate.",
  "delayMs": 500,
  "probability": 0.3,
  "count": 0,
  "hits": 0
}
```

| Field | Rules |
|---|---|
| `id` | Optional on create (generated as `f-xxxxxxxx`). Must be unique |
| `target` | `http` (default): matched before auth, `op` is an S3 operation name. `storage`: matched inside the backend, `op` is a backend method (see the list below) |
| `op`, `bucket` | Exact match; empty matches anything |
| `key` | `path.Match` glob (`*`, `?`, `[a-z]`); `*` does not cross `/` |
| `status` | `0` (latency only) or `400`–`599` |
| `code` | Defaults from status: `500 InternalError`, `503 SlowDown`, `403 AccessDenied`, `404 NoSuchKey`, `400 InvalidRequest`, others `InternalError` |
| `delayMs` | `0`–`300000` |
| `probability` | In `(0, 1]`; omitted = `1` |
| `count` | `0` = unlimited; after `count` hits the rule stops matching but stays listed |
| `hits` | Read-only |

Storage rules accept these `op` names: `ListBuckets`, `CreateBucket`, `DeleteBucket`, `HeadBucket`, `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `CopyObject`, `SetObjectMeta`, `ListObjects`, `CreateMultipartUpload`, `UploadPart`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `ListParts`, `ListMultipartUploads`. There is no `ListObjectsV2` or `DeleteObjects` for storage rules (those S3 operations call the backend methods above).

In responses, `target` is always present (`http` when omitted on create), `count`, `delayMs` and `probability` are omitted when unset or `0`, `hits` is always present, and `message` defaults to `Injected fault.`.

A rule needs `status > 0` or `delayMs > 0`. The first matching rule (creation order) wins.

`GET /_s3lab/faults` → `200` `{"rules": [...]}`

`POST /_s3lab/faults` with a rule → `201` saved rule. Invalid → `400` code `BadRequest`; duplicate `id` → `409` code `Conflict`.

`DELETE /_s3lab/faults/{id}` → `204`, or `404` code `NotFound`.

`DELETE /_s3lab/faults` → `204` (removes all).

Rules can also be loaded at boot with `S3LAB_FAULTS` (JSON array).

## Reset

`POST /_s3lab/reset` → `204`

| Parameter | Effect |
|---|---|
| none | Delete all buckets, objects and uploads, then re-apply the seed (`S3LAB_BUCKETS`, `S3LAB_SEED_DIR`) |
| `bucket=photos` | Empty only that bucket and re-seed it. A missing bucket returns `404 NoSuchBucket`; a bucket that is not part of the seed is only emptied |
| `requests=true` | Also clear the request log |
| `faults=true` | Also remove all fault rules |
