# Testing guide

s3lab is designed to be the S3 your test suite talks to. The recommended setup:

1. **One s3lab per test run**, with `S3LAB_STORAGE=memory` (fast, nothing to clean up).
2. **Reset between tests** with `POST /_s3lab/reset`, so every test starts from the same seeded state.
3. **Assert on the request log** (`GET /_s3lab/requests`) when *how* your code talks to S3 matters.
4. **Inject faults** to prove retries, timeouts and error handling work.

All `/_s3lab/*` calls go to the console port (`9001`) and need no signing. Full reference: [admin API](admin-api.md).

---

## Running in CI

### GitHub Actions

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      s3:
        image: leomachado94/s3lab
        ports: ["9000:9000", "9001:9001"]
        env:
          S3LAB_STORAGE: memory
          S3LAB_BUCKETS: uploads,avatars
        options: >-
          --health-cmd "/s3lab healthcheck"
          --health-interval 2s
          --health-retries 15
    env:
      AWS_ENDPOINT_URL: http://localhost:9000
      AWS_ACCESS_KEY_ID: s3lab
      AWS_SECRET_ACCESS_KEY: s3labsecret
      AWS_REGION: us-east-1
      S3LAB_ADMIN_URL: http://localhost:9001
    steps:
      - uses: actions/checkout@v4
      - run: make test
```

> If your job itself runs in a `container:`, use `http://s3:9000` (the service name) instead of `localhost`.

### GitLab CI

```yaml
test:
  image: python:3.13
  services:
    - name: leomachado94/s3lab
      alias: s3
  variables:
    S3LAB_STORAGE: memory
    S3LAB_BUCKETS: uploads
    AWS_ENDPOINT_URL: http://s3:9000
    AWS_ACCESS_KEY_ID: s3lab
    AWS_SECRET_ACCESS_KEY: s3labsecret
    AWS_REGION: us-east-1
    S3LAB_ADMIN_URL: http://s3:9001
  script:
    - pytest
```

### Docker Compose (local test stack)

```yaml
services:
  s3:
    image: leomachado94/s3lab
    environment:
      S3LAB_STORAGE: memory
      S3LAB_SEED_DIR: /seed
    volumes:
      - ./tests/fixtures/s3:/seed:ro
    healthcheck:
      test: ["CMD", "/s3lab", "healthcheck"]
      interval: 2s
      retries: 15
```

### Testcontainers

Go:

```go
req := testcontainers.ContainerRequest{
	Image:        "leomachado94/s3lab",
	ExposedPorts: []string{"9000/tcp", "9001/tcp"},
	Env:          map[string]string{"S3LAB_STORAGE": "memory", "S3LAB_BUCKETS": "test"},
	WaitingFor:   wait.ForHTTP("/_s3lab/health").WithPort("9001/tcp"),
}
c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
endpoint, _ := c.PortEndpoint(ctx, "9000/tcp", "http")
admin, _ := c.PortEndpoint(ctx, "9001/tcp", "http")
```

Python:

```python
from testcontainers.core.container import DockerContainer
from testcontainers.core.waiting_utils import wait_for_logs

s3lab = (DockerContainer("leomachado94/s3lab")
         .with_env("S3LAB_STORAGE", "memory")
         .with_env("S3LAB_BUCKETS", "test")
         .with_exposed_ports(9000, 9001))
s3lab.start()
wait_for_logs(s3lab, "listening")
endpoint = f"http://{s3lab.get_container_host_ip()}:{s3lab.get_exposed_port(9000)}"
```

---

## Reset between tests

```bash
curl -X POST "http://localhost:9001/_s3lab/reset?requests=true&faults=true"
```

| Call | Effect |
|---|---|
| `POST /_s3lab/reset` | Delete all buckets/objects/uploads, then re-apply `S3LAB_BUCKETS` and `S3LAB_SEED_DIR` |
| `POST /_s3lab/reset?bucket=uploads` | Empty only `uploads` and re-seed it |
| `&requests=true` | Also clear the request log |
| `&faults=true` | Also remove all fault rules |

Run it in your framework's "before each" hook.

### Go

```go
func resetS3(t *testing.T) {
	t.Helper()
	resp, err := http.Post(os.Getenv("S3LAB_ADMIN_URL")+"/_s3lab/reset?requests=true&faults=true", "", nil)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset s3lab: %v %v", err, resp)
	}
	resp.Body.Close()
}
```

### Python (pytest)

```python
import os, pytest, requests

ADMIN = os.environ.get("S3LAB_ADMIN_URL", "http://localhost:9001")

@pytest.fixture(autouse=True)
def clean_s3():
    requests.post(f"{ADMIN}/_s3lab/reset", params={"requests": "true", "faults": "true"}).raise_for_status()
    yield
```

### JavaScript (Jest / Vitest)

```js
const ADMIN = process.env.S3LAB_ADMIN_URL ?? "http://localhost:9001";

beforeEach(async () => {
  const res = await fetch(`${ADMIN}/_s3lab/reset?requests=true&faults=true`, { method: "POST" });
  if (res.status !== 204) throw new Error(`s3lab reset failed: ${res.status}`);
});
```

### PHP / Laravel

```php
// tests/Concerns/ResetsS3lab.php
trait ResetsS3lab
{
    protected function setUpResetsS3lab(): void
    {
        Http::post(env('S3LAB_ADMIN_URL', 'http://s3:9001').'/_s3lab/reset?requests=true&faults=true')->throw();
    }
}
```

Call `$this->setUpResetsS3lab()` in `setUp()`. Unlike `Storage::fake()`, this exercises the real S3 driver: signatures, multipart, visibility headers and presigned URLs.

---

## Asserting S3 requests

The request log records every call to the S3 port:

```bash
curl "http://localhost:9001/_s3lab/requests?bucket=uploads&op=PutObject"
```

```json
{"entries": [
  {"id": 7, "op": "PutObject", "bucket": "uploads", "key": "avatars/42.png",
   "status": 200, "durationMs": 1.8, "bytesIn": 20480, "auth": "header", "...": "..."}
]}
```

Filters: `bucket`, `op`, `key` (prefix), `status`, `errors=true` (status ≥ 400), `since=<id>`, `limit`. See [admin API](admin-api.md#request-log).

Things worth asserting:

- **The upload actually happened** with the right key and size.
- **No wasted calls**: e.g. your code doesn't `HeadObject` before every `GetObject`.
- **Multipart is used** for big files (`op=CreateMultipartUpload`).
- **Retries happened** after an injected fault (two `PutObject` entries, the first with `503`).
- **Nothing failed silently**: `errors=true` returns an empty list.

Python example:

```python
def s3_requests(**filters):
    r = requests.get(f"{ADMIN}/_s3lab/requests", params=filters)
    r.raise_for_status()
    return r.json()["entries"]

def test_avatar_upload_writes_once(client):
    client.post("/avatar", files={"file": b"..."})
    puts = s3_requests(bucket="avatars", op="PutObject")
    assert len(puts) == 1
    assert puts[0]["key"] == "users/1.png"
    assert s3_requests(errors="true") == []
```

Use `since` to only look at what happened after a point in the test:

```python
last = (s3_requests(limit=1) or [{"id": 0}])[-1]["id"]
do_something()
new_calls = s3_requests(since=last)
```

---

## Fault injection

A fault rule matches requests by operation, bucket and key glob, then returns an S3 error and/or adds latency.

```bash
curl -X POST localhost:9001/_s3lab/faults -H 'Content-Type: application/json' -d '{
  "op": "PutObject",
  "bucket": "uploads",
  "status": 503,
  "code": "SlowDown",
  "count": 2
}'
```

| Field | Meaning |
|---|---|
| `op` | S3 operation (`PutObject`, `GetObject`, `ListObjectsV2`, …). Empty = any |
| `bucket` | Exact bucket. Empty = any |
| `key` | Glob, e.g. `uploads/*.png`. Empty = any |
| `status` / `code` / `message` | Error to return. `status: 0` = latency only |
| `delayMs` | Wait before responding (or before failing) |
| `probability` | `0.3` = 30% of matching requests. Default `1` |
| `count` | Stop after firing N times. Default `0` = forever |
| `target` | `http` (default) or `storage` (fails inside the backend, after auth and parsing) |

The response includes an `id`; remove it with `DELETE /_s3lab/faults/{id}` or remove all with `DELETE /_s3lab/faults`. Every request hit by a rule shows `faultId` in the request log.

### Recipes

| You want to test | Rule |
|---|---|
| Retry on throttling | `{"op":"PutObject","status":503,"code":"SlowDown","count":2}` |
| Flaky reads | `{"op":"GetObject","status":500,"probability":0.5}` |
| Client timeout | `{"op":"GetObject","delayMs":30000}` |
| Slow network everywhere | `{"delayMs":800}` |
| Missing file handling | `{"op":"GetObject","key":"reports/*","status":404,"code":"NoSuchKey"}` |
| Permission errors | `{"bucket":"private","status":403,"code":"AccessDenied"}` |
| Multipart completion failure | `{"op":"CompleteMultipartUpload","status":500,"count":1}` |
| Fail the first requests after boot | `S3LAB_FAULTS='[{"status":503,"code":"SlowDown","count":3}]'` |

Go test with a fault:

```go
func TestUploadRetriesOnSlowDown(t *testing.T) {
	resetS3(t)
	addFault(t, `{"op":"PutObject","status":503,"code":"SlowDown","count":1}`)

	if err := uploader.Upload(ctx, "uploads", "a.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("upload should succeed after retry: %v", err)
	}
	puts := listRequests(t, "op=PutObject")
	if len(puts) != 2 || puts[0].Status != 503 || puts[1].Status != 200 {
		t.Fatalf("expected one failure then success, got %+v", puts)
	}
}
```

---

## Tips

- **Isolate parallel tests** by giving each test its own bucket (e.g. `test-<random>`) instead of resetting globally, or reset with `?bucket=`.
- **Big files**: `memory` keeps everything in RAM; for multi-GB tests use `fs` on a `tmpfs` mount.
- **Debug a failing test** by opening <http://localhost:9001/#/requests> while it runs.
- **Logs**: `S3LAB_LOG_LEVEL=debug` prints every request to stdout (`docker logs`).
