# Getting started

## 1. Run it

### Docker

```bash
docker run --rm --name s3lab \
  -p 9000:9000 -p 9001:9001 \
  -e S3LAB_BUCKETS=dev \
  leomachado/s3lab
```

Data is stored in `/data` inside the container. Keep it across restarts with a volume:

```bash
docker run -d --name s3lab -p 9000:9000 -p 9001:9001 -v s3lab-data:/data leomachado/s3lab
```

Or skip disk entirely (fastest, everything is lost on stop):

```bash
docker run --rm -p 9000:9000 -e S3LAB_STORAGE=memory leomachado/s3lab
```

### Docker Compose

```yaml
services:
  s3:
    image: leomachado/s3lab
    ports:
      - "9000:9000"   # S3 API
      - "9001:9001"   # console + admin API
    environment:
      S3LAB_ACCESS_KEY: local
      S3LAB_SECRET_KEY: local-secret
      S3LAB_BUCKETS: uploads,avatars
    volumes:
      - s3data:/data
    healthcheck:
      test: ["CMD", "/s3lab", "healthcheck"]
      interval: 2s
      timeout: 2s
      retries: 15

  app:
    build: .
    depends_on:
      s3:
        condition: service_healthy
    environment:
      AWS_ENDPOINT_URL: http://s3:9000
      AWS_ACCESS_KEY_ID: local
      AWS_SECRET_ACCESS_KEY: local-secret
      AWS_REGION: us-east-1

volumes:
  s3data:
```

> Inside the compose network the endpoint is `http://s3:9000` (service name), from your machine it is `http://localhost:9000`.

### Binary

Download from the [releases page](https://github.com/LeoMachado94/s3lab/releases) or build it:

```bash
go install github.com/LeoMachado94/s3lab/cmd/s3lab@latest
s3lab --data-dir ./data --buckets dev
```

## 2. Default credentials

| Setting | Default | Change with |
|---|---|---|
| Endpoint | `http://localhost:9000` | the published port (`-p`), or the listen address `S3LAB_ADDR` |
| Access key | `s3lab` | `S3LAB_ACCESS_KEY` |
| Secret key | `s3labsecret` | `S3LAB_SECRET_KEY` |
| Region | `us-east-1` | `S3LAB_REGION` |
| Addressing | path-style (`http://host:9000/bucket/key`) | `S3LAB_DOMAIN` *also* enables virtual-host style (`<bucket>.<domain>`); path-style always keeps working |

Most SDKs need **path-style** enabled when talking to a custom endpoint. See [client setup](clients.md).

## 3. First bucket and object

With the AWS CLI:

```bash
export AWS_ACCESS_KEY_ID=s3lab AWS_SECRET_ACCESS_KEY=s3labsecret AWS_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:9000   # AWS CLI >= 2.13

aws s3 mb s3://photos
aws s3 cp ./cat.jpg s3://photos/2026/cat.jpg
aws s3 ls s3://photos --recursive
aws s3 presign s3://photos/2026/cat.jpg --expires-in 300
```

With plain `curl` (no signing needed: use the admin API):

```bash
curl -u s3lab:s3labsecret -X POST localhost:9001/_s3lab/buckets -H 'Content-Type: application/json' -d '{"name":"photos"}'
curl -u s3lab:s3labsecret -X PUT "localhost:9001/_s3lab/buckets/photos/object?key=hello.txt" \
  -H 'Content-Type: text/plain' --data-binary 'hello'
curl -u s3lab:s3labsecret "localhost:9001/_s3lab/buckets/photos/objects"
```

## 4. Open the console

Go to <http://localhost:9001> and log in with the access key and secret key (`s3lab` / `s3labsecret` by default): browse buckets, upload files, and open the **Requests** tab to watch every S3 call your app makes, live. See [console](console.md).

## 5. Next steps

- Wire up your language: [client setup](clients.md)
- Use it in CI and tests: [testing guide](testing.md)
- Tune it: [configuration](configuration.md)
