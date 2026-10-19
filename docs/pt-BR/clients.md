# Configuração de clientes

Todo cliente S3 precisa das mesmas quatro coisas:

1. **Endpoint**: `http://localhost:9000` (ou `http://<service>:9000` dentro do Docker Compose)
2. **Credenciais**: `s3lab` / `s3labsecret` por padrão
3. **Região**: qualquer valor; `us-east-1` é o mais seguro
4. **Endereçamento path-style**: ligado (`http://host:9000/bucket/key`). O path-style sempre funciona; [`S3LAB_DOMAIN`](configuration.md) *também* habilita o estilo virtual-host (`<bucket>.<domain>`)

Os exemplos abaixo usam os valores padrão. Troque pelos seus.

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

Como profile nomeado (`~/.aws/config`):

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

URL pré-assinada:

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

Upload direto do navegador com PUT pré-assinado funciona de cara, porque o CORS é aberto por padrão (`S3LAB_CORS_ORIGINS=*`).

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

Instale o driver: `composer require league/flysystem-aws-s3-v3 "^3.0"`.

`.env` (app rodando **dentro** do mesmo Docker Compose do s3lab):

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

O disk `s3` padrão de `config/filesystems.php` já lê essas variáveis.

```php
Storage::disk('s3')->put('avatars/1.png', $contents);
Storage::disk('s3')->url('avatars/1.png');                       // http://localhost:9000/uploads/avatars/1.png
Storage::disk('s3')->temporaryUrl('avatars/1.png', now()->addMinutes(5));
```

> `temporaryUrl()` assina com o **host do endpoint** (`s3:9000`), que o navegador não consegue resolver. Veja [solução de problemas → URLs pré-assinadas geradas dentro do Docker](troubleshooting.md#urls-pré-assinadas-geradas-dentro-do-docker-não-abrem-no-navegador).

> `Storage::url()` retorna uma URL simples, sem assinatura. O navegador só consegue abri-la com `S3LAB_ANONYMOUS=true`, que permite leituras **e escritas/remoções** anônimas para qualquer pessoa que alcance a porta; use apenas em uma rede de desenvolvimento confiável. Caso contrário, use `temporaryUrl()`.

PHPUnit: crie o bucket em `S3LAB_BUCKETS` e faça o reset do estado entre os testes; veja o [guia de testes](testing.md#php--laravel).

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
