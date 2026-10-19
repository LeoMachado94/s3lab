# Compatibilidade S3

O s3lab implementa a parte da API S3 que aplicações e SDKs usam no dia a dia. Qualquer outra coisa responde `501 NotImplemented` com um erro XML de S3 no formato correto, para você perceber em vez de ter um comportamento errado silencioso.

## Operações suportadas

| Área | Operações |
|---|---|
| Service | `ListBuckets` |
| Buckets | `CreateBucket`, `DeleteBucket`, `HeadBucket`, `GetBucketLocation` |
| Listagem | `ListObjectsV2` (prefix, delimiter, `max-keys`, `start-after`, continuation token, `encoding-type=url`, `fetch-owner`), `ListObjects` (v1, marker) |
| Objetos | `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `DeleteObjects` (batch, modo quiet), `CopyObject` (diretiva de metadados `COPY`/`REPLACE`, condições de cópia) |
| Multipart | `CreateMultipartUpload`, `UploadPart`, `UploadPartCopy` (com `x-amz-copy-source-range`), `CompleteMultipartUpload`, `AbortMultipartUpload`, `ListParts`, `ListMultipartUploads` |
| ACLs de objeto | ACLs canned (`x-amz-acl` em `PutObject`, `CopyObject`, `CreateMultipartUpload`, `PutObjectAcl`; ou corpo `AccessControlPolicy`) são armazenadas e retornadas por `GetObjectAcl`, então checagens de visibilidade (Flysystem/Laravel `visibility()`) funcionam. **Não são aplicadas**: o acesso é decidido pelas credenciais / `S3LAB_ANONYMOUS` |
| CORS | `OPTIONS` de preflight e headers de resposta, configurados globalmente via `S3LAB_CORS_ORIGINS` |

### Recursos de objetos

- `Range: bytes=a-b`, `bytes=a-`, `bytes=-n` (range único) → `206` com `Content-Range`; range não satisfazível → `416 InvalidRange`; headers malformados ou com múltiplos ranges são ignorados (objeto inteiro, `200`), como na AWS.
- Requests condicionais: `If-Match`, `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since` → `304` / `412`.
- Armazenados e retornados: `Content-Type`, `Content-Encoding`, `Content-Disposition`, `Content-Language`, `Cache-Control`, `Expires`, `x-amz-meta-*`.
- `response-content-type`, `response-content-disposition` e outros overrides `response-*` no `GetObject` (úteis com URLs pré-assinadas).
- O `Content-MD5` é verificado (`400 BadDigest` se não bater).
- ETag: MD5 do conteúdo; objetos multipart usam o formato do S3 `<md5-of-md5s>-<parts>`. Tamanho mínimo de part de 5 MiB (exceto a última), máximo de 10 000 parts.
- Keys de até 1024 bytes, qualquer UTF-8 (inclusive `../`, `//`, `/` no final).

### Autenticação

- AWS Signature V4 no header `Authorization`.
- URLs pré-assinadas (SigV4 na query string, até 7 dias).
- Modos de payload: SHA-256 assinado, `UNSIGNED-PAYLOAD`, `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` (aws-chunked) e as variantes com trailer usadas pelos SDKs recentes para checksums (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`).
- Requests assinadas para qualquer região são aceitas.
- Tolerância a diferença de relógio: 15 minutos (acima disso, `RequestTimeTooSkewed`).
- Signature V2 **não** é suportada (`InvalidRequest`).

### Endereçamento

- Path-style (`http://host:9000/bucket/key`), sempre.
- Estilo virtual-host (`http://bucket.s3.local:9000/key`) quando `S3LAB_DOMAIN=s3.local` está definido e o nome resolve para o s3lab.

## Não suportado (ainda)

| Recurso | Comportamento |
|---|---|
| Versioning, object lock, retention, legal hold | `501 NotImplemented` |
| Tagging de objeto/bucket | `501` (planejado) |
| Bucket policies, ACLs por bucket, IAM, STS | `501`; uma única access key tem acesso total |
| CORS por bucket, lifecycle, replication, website, notifications | `501` (webhooks de eventos planejados) |
| Criptografia server-side (SSE-S3/KMS/C) | headers ignorados, dados armazenados como estão |
| Checksums (`x-amz-checksum-*`) | aceitos, não validados nem retornados |
| Uploads via POST no navegador (formulário HTML + policy) | `501` |
| `SelectObjectContent`, restore, torrent | `501` |
| Storage classes | aceitas, sempre reportadas como `STANDARD` |

## Diferenças conhecidas em relação à AWS

- `CreateBucket` em um bucket existente retorna `409 BucketAlreadyOwnedByYou` (só existe um owner).
- `GetBucketLocation` retorna `S3LAB_REGION`, qualquer que seja a região usada na assinatura do cliente.
- O ID e o nome de exibição do owner são sempre `s3lab`.
- A listagem é consistente imediatamente após as escritas (a AWS também é, desde 2020).
