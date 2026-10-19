# s3lab — API admin (console)

Fonte de verdade para `internal/console` (servidor) e `web/` (UI). Servida na porta do console (padrão `9001`), separada da porta S3 (`9000`). Sem autenticação: pensada para ambiente local/CI.

Convenções:
- JSON UTF-8, `Content-Type: application/json`.
- Datas em RFC 3339 com milissegundos (`2026-10-19T12:00:00.000Z`).
- Erros: status HTTP adequado + corpo `{"error": {"code": "NoSuchBucket", "message": "..."}}`. Códigos S3 quando houver equivalente (`NoSuchBucket`, `NoSuchKey`, `BucketNotEmpty`, `InvalidBucketName`, `BucketAlreadyOwnedByYou`), senão `BadRequest`, `NotFound`, `InternalError`.
- Chaves de objeto vão sempre em query `key=` (URL-encoded), nunca no path.
- UI estática servida em `/` (qualquer rota fora de `/_s3lab/` cai no `index.html`).

## Saúde e info

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
  "startedAt": "2026-10-19T12:00:00.000Z"
}
```

## Buckets

`GET /_s3lab/buckets` → `200`
```json
{"buckets": [{"name": "photos", "createdAt": "2026-10-19T12:00:00.000Z"}]}
```

`POST /_s3lab/buckets` body `{"name": "photos"}` → `201` `{"name": "photos", "createdAt": "..."}`

`DELETE /_s3lab/buckets/{bucket}` → `204`. Com `?force=true` esvazia (objetos e uploads) antes de remover; sem `force` e bucket não vazio → `409 BucketNotEmpty`.

## Objetos

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
`delimiter` padrão `/` (passar `delimiter=` vazio para listagem plana). `maxKeys` padrão 200, máx 1000.

`GET /_s3lab/buckets/{bucket}/object?key=a/b.jpg` → `200`
```json
{
  "key": "a/b.jpg",
  "size": 1234,
  "etag": "9e107d9d372bb6826bd81d3542a419d6",
  "lastModified": "...",
  "partsCount": 0,
  "contentType": "image/jpeg",
  "headers": {"Content-Encoding": "", "Content-Disposition": "", "Content-Language": "", "Cache-Control": "", "Expires": ""},
  "metadata": {"owner": "leo"}
}
```
`headers` só inclui valores não vazios.

`GET /_s3lab/buckets/{bucket}/download?key=a/b.jpg` → `200` corpo bruto, `Content-Type` do objeto, `Content-Disposition: attachment; filename="b.jpg"`. Com `?inline=1` usa `inline` (preview na UI).

`PUT /_s3lab/buckets/{bucket}/object?key=a/b.jpg` corpo bruto; `Content-Type` da request vira o do objeto; headers `x-amz-meta-*` viram metadata → `201` mesmo JSON do GET de objeto.

`DELETE /_s3lab/buckets/{bucket}/object?key=a/b.jpg` → `204`.

`DELETE /_s3lab/buckets/{bucket}/objects?prefix=tmp/` → `200` `{"deleted": 12}` (apaga em lote por prefixo; prefixo vazio esvazia o bucket).

## Log de requests

Apenas requests da porta S3 são registradas.

`GET /_s3lab/requests?bucket=&op=&key=&status=&errors=true&since=<id>&limit=100` → `200`
```json
{
  "entries": [
    {
      "id": 42,
      "requestId": "8F3A1C2B4D5E6F70",
      "time": "...",
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
- Ordem: mais antiga → mais nova. `key` filtra por prefixo. `errors=true` → status ≥ 400. `since` → `id > since` (polling incremental). `limit` → as N mais recentes que casam.
- Campos com valor vazio podem ser omitidos (`query`, `bucket`, `key`, `errorCode`, `accessKey`, `auth`, `faultId`, `remoteAddr`, `userAgent`).

`DELETE /_s3lab/requests` → `204`.

`GET /_s3lab/requests/stream` → `text/event-stream` (SSE). Aceita os mesmos filtros de `GET /_s3lab/requests` (exceto `limit`/`since`).
```
event: request
id: 42
data: {...entry...}

: ping
```
Comentário `: ping` a cada 15s. Com header `Last-Event-ID`, reenvia primeiro as entradas com `id` maior ainda no buffer.

## Fault injection

Regra:
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
- `target`: `http` (padrão, nome de operação S3 em `op`, ex. `PutObject`) ou `storage` (nome do método do backend em `op`, ex. `PutObject`, `GetObject`, `ListObjects`).
- `op`, `bucket`, `key` (glob estilo `path.Match`) vazios casam qualquer valor.
- `status` 0 = só atraso. Se `status` > 0 e `code` vazio, o código é derivado do status (`500 InternalError`, `503 SlowDown`, `403 AccessDenied`, `404 NoSuchKey`, outros `InternalError`).
- `probability` em (0,1]; omitido = 1. `count` 0 = ilimitado; após `count` disparos a regra para de casar (continua listada).
- `hits` é somente leitura.
- Primeira regra que casa (ordem de criação) vence.

`GET /_s3lab/faults` → `200` `{"rules": [...]}`

`POST /_s3lab/faults` body = regra (sem `id` → gerado) → `201` regra salva; inválida → `400 {"error":{"code":"BadRequest",...}}`; `id` duplicado → `409`.

`DELETE /_s3lab/faults/{id}` → `204` ou `404`.

`DELETE /_s3lab/faults` → `204` (remove todas).

Também carregáveis no boot via env `S3LAB_FAULTS` (JSON array de regras).

## Reset

`POST /_s3lab/reset` → `204`
- Sem params: apaga todos os buckets/objetos/uploads e reaplica o seed (`S3LAB_BUCKETS`, `S3LAB_SEED_DIR`).
- `?bucket=photos`: esvazia só esse bucket e reaplica o seed dele.
- `?requests=true`: também limpa o log de requests.
- `?faults=true`: também remove as regras de falha.
