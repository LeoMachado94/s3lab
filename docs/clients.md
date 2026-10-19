# Client setup

Every S3 client needs the same four things:

1. **Endpoint**: `http://localhost:9000` (or `http://<service>:9000` inside Docker Compose)
2. **Credentials**: `s3lab` / `s3labsecret` by default
3. **Region**: any value; `us-east-1` is the safest
4. **Path-style addressing**: on (`http://host:9000/bucket/key`). Path-style always works; [`S3LAB_DOMAIN`](configuration.md) *also* enables virtual-host style (`<bucket>.<domain>`)

Examples below use the defaults. Replace them with your own values.

- [AWS CLI](#aws-cli) · [Go](#go-aws-sdk-go-v2) · [Python](#python-boto3) · [Node.js](#nodejs-aws-sdk-v3) · [PHP](#php-aws-sdk) · [Laravel](#laravel) · [Java](#java-aws-sdk-v2) · [.NET](#net) · [Ruby / Rails](#ruby--rails) · [Django](#django-django-storages) · [rclone](#rclone)

---

## AWS CLI

```bash
export AWS_ACCESS_KEY_ID=s3lab
export AWS_SECRET_ACCESS_KEY=s3labsecret
export AWS_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:9000   # CLI v2.13+; older: pass --endpoint-url on every command

aws s3 mb s3://dev
aws s3 sync ./public s3://dev/public
aws s3api head-object --bucket dev --key public/index.html
```

As a named profile (`~/.aws/config`):

```ini
[profile s3lab]
region = us-east-1
endpoint_url = http://localhost:9000
s3 =
  addressing_style = path
```

```ini
# ~/.aws/credentials
[s3lab]
aws_access_key_id = s3lab
aws_secret_access_key = s3labsecret
```

```bash
aws --profile s3lab s3 ls
```

## Go (aws-sdk-go-v2)

```go
cfg, err := config.LoadDefaultConfig(ctx,
	config.WithRegion("us-east-1"),
	config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("s3lab", "s3labsecret", "")),
)
if err != nil {
	return err
}
client := s3.NewFromConfig(cfg, func(o *s3.Options) {
	o.BaseEndpoint = aws.String("http://localhost:9000")
	o.UsePathStyle = true
})
```

Presigned URL:

```go
presigner := s3.NewPresignClient(client)
req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
	Bucket: aws.String("dev"),
	Key:    aws.String("report.pdf"),
}, s3.WithPresignExpires(15*time.Minute))
// req.URL
```

## Python (boto3)

```python
import boto3
from botocore.config import Config

s3 = boto3.client(
    "s3",
    endpoint_url="http://localhost:9000",
    aws_access_key_id="s3lab",
    aws_secret_access_key="s3labsecret",
    region_name="us-east-1",
    config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
)

s3.create_bucket(Bucket="dev")
s3.upload_file("data.csv", "dev", "imports/data.csv")
url = s3.generate_presigned_url("get_object", Params={"Bucket": "dev", "Key": "imports/data.csv"}, ExpiresIn=300)
```

## Node.js (AWS SDK v3)

```js
import { S3Client, PutObjectCommand, GetObjectCommand } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

const s3 = new S3Client({
  endpoint: "http://localhost:9000",
  region: "us-east-1",
  forcePathStyle: true,
  credentials: { accessKeyId: "s3lab", secretAccessKey: "s3labsecret" },
});

await s3.send(new PutObjectCommand({ Bucket: "dev", Key: "hello.txt", Body: "hi", ContentType: "text/plain" }));
const url = await getSignedUrl(s3, new GetObjectCommand({ Bucket: "dev", Key: "hello.txt" }), { expiresIn: 300 });
```

Browser direct upload with a presigned PUT works out of the box because CORS is open by default (`S3LAB_CORS_ORIGINS=*`).

## PHP (AWS SDK)

```php
use Aws\S3\S3Client;

$s3 = new S3Client([
    'version' => 'latest',
    'region' => 'us-east-1',
    'endpoint' => 'http://localhost:9000',
    'use_path_style_endpoint' => true,
    'credentials' => ['key' => 's3lab', 'secret' => 's3labsecret'],
]);

$s3->putObject(['Bucket' => 'dev', 'Key' => 'a.txt', 'Body' => 'hello']);
```

## Laravel

Install the driver: `composer require league/flysystem-aws-s3-v3 "^3.0"`.

`.env` (app running **inside** the same Docker Compose as s3lab):

```dotenv
FILESYSTEM_DISK=s3
AWS_ACCESS_KEY_ID=s3lab
AWS_SECRET_ACCESS_KEY=s3labsecret
AWS_DEFAULT_REGION=us-east-1
AWS_BUCKET=uploads
AWS_ENDPOINT=http://s3:9000
AWS_USE_PATH_STYLE_ENDPOINT=true
# Public URL used by Storage::url(); must be reachable by the browser
AWS_URL=http://localhost:9000/uploads
```

The default `config/filesystems.php` `s3` disk already reads these variables.

```php
Storage::disk('s3')->put('avatars/1.png', $contents);
Storage::disk('s3')->url('avatars/1.png');                       // http://localhost:9000/uploads/avatars/1.png
Storage::disk('s3')->temporaryUrl('avatars/1.png', now()->addMinutes(5));
```

> `temporaryUrl()` signs the **endpoint host** (`s3:9000`), which the browser can't resolve. See [troubleshooting → presigned URLs from Docker](troubleshooting.md#presigned-urls-generated-inside-docker-dont-open-in-the-browser).

> `Storage::url()` returns a plain, unsigned URL. A browser can open it only with `S3LAB_ANONYMOUS=true`, which allows anonymous reads **and writes/deletes** for anyone who reaches the port, so use it only on a trusted dev network. Otherwise use `temporaryUrl()`.

PHPUnit: create the bucket in your `S3LAB_BUCKETS` and reset state between tests, see [testing guide](testing.md#php--laravel).

## Java (AWS SDK v2)

```java
S3Client s3 = S3Client.builder()
    .endpointOverride(URI.create("http://localhost:9000"))
    .region(Region.US_EAST_1)
    .forcePathStyle(true)
    .credentialsProvider(StaticCredentialsProvider.create(
        AwsBasicCredentials.create("s3lab", "s3labsecret")))
    .build();
```

Spring Cloud AWS (`application.yml`):

```yaml
spring:
  cloud:
    aws:
      region.static: us-east-1
      credentials:
        access-key: s3lab
        secret-key: s3labsecret
      s3:
        endpoint: http://localhost:9000
        path-style-access-enabled: true
```

## .NET

```csharp
var s3 = new AmazonS3Client(
    new BasicAWSCredentials("s3lab", "s3labsecret"),
    new AmazonS3Config
    {
        ServiceURL = "http://localhost:9000",
        ForcePathStyle = true,
        AuthenticationRegion = "us-east-1",
    });
```

## Ruby / Rails

```ruby
s3 = Aws::S3::Client.new(
  endpoint: "http://localhost:9000",
  region: "us-east-1",
  force_path_style: true,
  access_key_id: "s3lab",
  secret_access_key: "s3labsecret",
)
```

Active Storage (`config/storage.yml`):

```yaml
s3lab:
  service: S3
  endpoint: http://localhost:9000
  force_path_style: true
  region: us-east-1
  bucket: uploads
  access_key_id: s3lab
  secret_access_key: s3labsecret
```

## Django (django-storages)

```python
STORAGES = {
    "default": {"BACKEND": "storages.backends.s3.S3Storage"},
}
AWS_S3_ENDPOINT_URL = "http://localhost:9000"
AWS_S3_ADDRESSING_STYLE = "path"
AWS_S3_REGION_NAME = "us-east-1"
AWS_ACCESS_KEY_ID = "s3lab"
AWS_SECRET_ACCESS_KEY = "s3labsecret"
AWS_STORAGE_BUCKET_NAME = "uploads"
AWS_S3_SIGNATURE_VERSION = "s3v4"
```

## rclone

```ini
# ~/.config/rclone/rclone.conf
[s3lab]
type = s3
provider = Other
endpoint = http://localhost:9000
access_key_id = s3lab
secret_access_key = s3labsecret
region = us-east-1
force_path_style = true
```

```bash
rclone copy ./backup s3lab:dev/backup
```
