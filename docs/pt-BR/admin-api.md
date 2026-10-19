# Admin API

A admin API alimenta o console web e permite que os testes inspecionem e controlem o s3lab sem assinatura S3. Ela é servida na porta do console (padrão `9001`), separada da porta S3 (`9000`).

> **Exige login.** Todos os endpoints, exceto `/_s3lab/health`, `/_s3lab/login` e `/_s3lab/logout`, pedem as credenciais S3 (`S3LAB_ACCESS_KEY` / `S3LAB_SECRET_KEY`). Veja [autenticação](#autenticação).

## Autenticação

Duas formas de autenticar, ambas com a access key e a secret key do S3:

- **HTTP Basic auth**, para scripts e testes: `curl -u s3lab:s3labsecret localhost:9001/_s3lab/requests`. Go, `requests` do Python e Guzzle também aceitam as credenciais dentro da URL (`http://s3lab:s3labsecret@localhost:9001`).
- **Cookie de sessão**, usado pelo console web: `POST /_s3lab/login` com `{"accessKey": "...", "secretKey": "..."}` retorna `204` e grava um cookie `HttpOnly`, `SameSite=Strict`, válido por 12 horas. `POST /_s3lab/logout` remove o cookie. As sessões acabam quando o s3lab reinicia.

Credenciais ausentes ou erradas retornam `401 Unauthorized` (após 1 s de espera, para dificultar tentativas). Nenhum header `WWW-Authenticate` é enviado, então o navegador não abre o popup nativo de login.

Defina `S3LAB_CONSOLE_AUTH=false` para desligar o login (comportamento anterior à 0.2); nesse caso `/_s3lab/info` retorna `"auth": false`.

## Convenções

- JSON na entrada e na saída, UTF-8, `Content-Type: application/json`.
- Timestamps: RFC 3339 com milissegundos (`2026-10-19T12:00:00.000Z`).
- Erros: status HTTP correspondente mais `{"error": {"code": "NoSuchBucket", "message": "..."}}`. Códigos S3 são usados quando algum se encaixa (`NoSuchBucket`, `NoSuchKey`, `BucketNotEmpty`, `InvalidBucketName`, `BucketAlreadyOwnedByYou`); caso contrário, `BadRequest`, `NotFound`, `Conflict`, `InternalError`.
- Keys de objetos sempre vão no query parameter `key=` (URL-encoded), nunca no path.
- O console web é servido em `/`; qualquer `GET`/`HEAD` fora de `/_s3lab/` cai de volta em `index.html`. Outros métodos ali retornam `405 MethodNotAllowed`.
- `403 Forbidden` é retornado para um `Host` não permitido e para requests que não sejam GET cujo host do `Origin` difere do `Host` (veja [notas de segurança](configuration.md#notas-de-segurança)).
- `PUT` de objeto acima de `S3LAB_MAX_OBJECT_SIZE` retorna `413 EntityTooLarge`. `maxKeys` menor que 1 ou não numérico retorna `400`.

| Método | Path | Finalidade |
|---|---|---|
| GET | [`/_s3lab/health`](#health-e-info) | Liveness |
| GET | [`/_s3lab/info`](#health-e-info) | Versão, portas, storage, access key |
| POST | [`/_s3lab/login`](#autenticação) | Login do console (cookie de sessão) |
| POST | [`/_s3lab/logout`](#autenticação) | Encerra a sessão |
| GET, POST | [`/_s3lab/buckets`](#buckets) | Listar / criar buckets |
| DELETE | [`/_s3lab/buckets/{bucket}`](#buckets) | Apagar bucket (`?force=true`) |
| GET, DELETE | [`/_s3lab/buckets/{bucket}/objects`](#objetos) | Listar / apagar por prefixo |
| GET, PUT, DELETE | [`/_s3lab/buckets/{bucket}/object?key=`](#objetos) | Metadados / upload / exclusão do objeto |
| GET | [`/_s3lab/buckets/{bucket}/download?key=`](#objetos) | Body do objeto |
| GET, DELETE | [`/_s3lab/requests`](#log-de-requests) | Consultar / limpar o log de requests |
| GET | [`/_s3lab/requests/stream`](#log-de-requests) | Log de requests ao vivo (SSE) |
| GET, POST, DELETE | [`/_s3lab/faults`](#injeção-de-falhas) | Listar / adicionar / limpar regras de falha |
| DELETE | [`/_s3lab/faults/{id}`](#injeção-de-falhas) | Remover uma regra |
| POST | [`/_s3lab/reset`](#reset) | Resetar o estado |

## Health e info

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

A secret key nunca é retornada; o console mostra um placeholder no lugar dela.

## Buckets

`GET /_s3lab/buckets` → `200`

```json
{"buckets": [{"name": "photos", "createdAt": "2026-10-19T12:00:00.000Z"}]}
```

`POST /_s3lab/buckets` com body `{"name": "photos"}` → `201` `{"name": "photos", "createdAt": "..."}`

`DELETE /_s3lab/buckets/{bucket}` → `204`. Com `?force=true`, objetos e uploads pendentes são apagados antes; sem ele, um bucket não vazio retorna `409 BucketNotEmpty`.

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

`delimiter` tem `/` como padrão (envie `delimiter=` vazio para uma listagem plana). `maxKeys` tem padrão 200, máximo 1000. Passe `nextStartAfter` de volta como `startAfter` para obter a próxima página.

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

`headers` contém apenas os valores não vazios entre `Content-Encoding`, `Content-Disposition`, `Content-Language`, `Cache-Control`, `Expires`. `metadata` guarda os valores `x-amz-meta-*` sem o prefixo.

`GET /_s3lab/buckets/{bucket}/download?key=a/b.jpg` → `200` com o body bruto, o `Content-Type` do objeto e `Content-Disposition: attachment; filename="b.jpg"`. Adicione `?inline=1` para `inline` (usado nos previews).

`PUT /_s3lab/buckets/{bucket}/object?key=a/b.jpg` com o body bruto. O `Content-Type` da request vira o do objeto; headers `x-amz-meta-*` viram metadados → `201` com o mesmo JSON de `GET .../object`.

`DELETE /_s3lab/buckets/{bucket}/object?key=a/b.jpg` → `204`.

`DELETE /_s3lab/buckets/{bucket}/objects?prefix=tmp/` → `200` `{"deleted": 12}`. Um prefixo vazio esvazia o bucket.

## Log de requests

Apenas requests à porta S3 são registradas, exceto os probes `/minio/health/*`, que não entram no log.

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

| Parâmetro | Efeito |
|---|---|
| `bucket` | Bucket exato |
| `op` | Nome exato da operação (veja [compatibilidade S3](s3-compatibility.md)) |
| `key` | Prefixo da key |
| `status` | Status HTTP exato |
| `errors=true` | Apenas status ≥ 400 |
| `since` | Apenas entradas com `id > since` (polling incremental) |
| `limit` | Mantém as N correspondências mais recentes |

- As entradas vêm ordenadas da mais antiga para a mais nova. Os IDs continuam crescendo mesmo depois de um clear.
- `auth` é `header`, `presigned`, `failed`, ou vazio para anônimo.
- Campos vazios podem ser omitidos (`query`, `bucket`, `key`, `errorCode`, `accessKey`, `auth`, `faultId`, `remoteAddr`, `userAgent`).

`DELETE /_s3lab/requests` → `204`.

`GET /_s3lab/requests/stream` → `text/event-stream` (Server-Sent Events). Aceita os mesmos filtros de `GET /_s3lab/requests`, exceto `limit`. Com `since=<id>` (ou um header `Last-Event-ID`, que tem prioridade), as entradas correspondentes com `id` maior que ainda estão no buffer são reenviadas primeiro, e depois seguem as entradas ao vivo. Os clientes podem preencher uma tela com `GET /_s3lab/requests?limit=N` e abrir o stream com `since=<último id>` sem lacunas.

```
event: request
id: 42
data: {...entry...}

: ping
```

Um comentário `: ping` é enviado a cada 15 s.

```bash
curl -u s3lab:s3labsecret -N localhost:9001/_s3lab/requests/stream?errors=true
```

## Injeção de falhas

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

| Campo | Regras |
|---|---|
| `id` | Opcional na criação (gerado como `f-xxxxxxxx`). Deve ser único |
| `target` | `http` (padrão): casado antes da autenticação, `op` é um nome de operação S3. `storage`: casado dentro do backend, `op` é um método do backend (veja a lista abaixo) |
| `op`, `bucket` | Match exato; vazio casa com qualquer valor |
| `key` | Glob `path.Match` (`*`, `?`, `[a-z]`); `*` não atravessa `/` |
| `status` | `0` (apenas latência) ou `400`–`599` |
| `code` | Deduzido do status: `500 InternalError`, `503 SlowDown`, `403 AccessDenied`, `404 NoSuchKey`, `400 InvalidRequest`, demais `InternalError` |
| `delayMs` | `0`–`300000` |
| `probability` | Em `(0, 1]`; omitido = `1` |
| `count` | `0` = ilimitado; após `count` hits a regra para de casar, mas continua listada |
| `hits` | Somente leitura |

Regras de storage aceitam estes nomes de `op`: `ListBuckets`, `CreateBucket`, `DeleteBucket`, `HeadBucket`, `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `CopyObject`, `SetObjectMeta`, `ListObjects`, `CreateMultipartUpload`, `UploadPart`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `ListParts`, `ListMultipartUploads`. Não existe `ListObjectsV2` nem `DeleteObjects` para regras de storage (essas operações S3 chamam os métodos de backend acima).

Nas respostas, `target` está sempre presente (`http` quando omitido na criação), `count`, `delayMs` e `probability` são omitidos quando não definidos ou `0`, `hits` está sempre presente, e `message` tem como padrão `Injected fault.`.

Uma regra precisa de `status > 0` ou `delayMs > 0`. A primeira regra que casar (ordem de criação) vence.

`GET /_s3lab/faults` → `200` `{"rules": [...]}`

`POST /_s3lab/faults` com uma regra → `201` com a regra salva. Inválida → `400` código `BadRequest`; `id` duplicado → `409` código `Conflict`.

`DELETE /_s3lab/faults/{id}` → `204`, ou `404` código `NotFound`.

`DELETE /_s3lab/faults` → `204` (remove todas).

As regras também podem ser carregadas no boot com `S3LAB_FAULTS` (array JSON).

## Reset

`POST /_s3lab/reset` → `204`

| Parâmetro | Efeito |
|---|---|
| nenhum | Apaga todos os buckets, objetos e uploads, e reaplica o seed (`S3LAB_BUCKETS`, `S3LAB_SEED_DIR`) |
| `bucket=photos` | Esvazia apenas esse bucket e refaz o seed dele. Um bucket inexistente retorna `404 NoSuchBucket`; um bucket que não faz parte do seed é apenas esvaziado |
| `requests=true` | Também limpa o log de requests |
| `faults=true` | Também remove todas as regras de falha |
