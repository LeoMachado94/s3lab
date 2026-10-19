# Solução de problemas

Primeira parada para qualquer problema: abra <http://localhost:9001/#/requests> (ou `curl localhost:9001/_s3lab/requests?errors=true`) e olhe o `op`, o `status` e o `errorCode` da request que falhou. Se a request nem aparece, seu cliente não está chegando ao s3lab.

## `SignatureDoesNotMatch`

A assinatura cobre método, path, query, host e headers assinados. Causas comuns:

| Causa | Correção |
|---|---|
| Secret key errada | Confira se `S3LAB_SECRET_KEY` (ou `MINIO_ROOT_PASSWORD`) bate com a do cliente |
| Um proxy reescreve o header `Host` ou o path | Faça o proxy repassar o `Host` sem alterações e não recodificar paths |
| O host da URL mudou depois da assinatura | Não reescreva o host de URLs pré-assinadas; veja abaixo |
| O cliente assina um body que depois é alterado (middleware de gzip etc.) | Assine o body final, ou use `UNSIGNED-PAYLOAD` |

## `InvalidAccessKeyId`

A access key não é a configurada. Os padrões são `s3lab` / `s3labsecret`. Cuidado com SDKs que pegam credenciais de `~/.aws/credentials` ou de `AWS_PROFILE` em vez da configuração dos seus testes.

## `RequestTimeTooSkewed`

Os relógios do cliente e do servidor diferem em mais de 15 minutos. Comum no Docker Desktop depois que o notebook entra em repouso: reinicie o Docker Desktop ou sincronize o relógio da VM.

## Estilo virtual-host sem `S3LAB_DOMAIN`: resultados errados, `NoSuchBucket`, `InvalidBucketName`

O SDK está usando o estilo virtual-host e envia `GET /key` para `my-bucket.localhost`. Sem `S3LAB_DOMAIN`, o s3lab ignora o header `Host` e lê o path como `bucket/key`, então `key` é tratado como o nome do bucket. O sintoma **não** é `SignatureDoesNotMatch`: você vê resultados vazios ou errados, `NoSuchBucket`, `InvalidBucketName` ou um bucket inesperado sendo criado.

Habilite o path-style (`UsePathStyle`, `forcePathStyle`, `use_path_style_endpoint`, `addressing_style: path`; veja a [configuração de clientes](clients.md)) ou configure `S3LAB_DOMAIN` e faça `*.your-domain` resolver para o s3lab.

## `403 Forbidden` host not allowed (console)

O console/admin API rejeita valores de `Host` que não reconhece (proteção contra DNS rebinding), por exemplo um domínio próprio atrás de um reverse proxy. Adicione o host em `S3LAB_CONSOLE_ALLOWED_HOSTS` (separado por vírgula). Veja as [notas de segurança](configuration.md#notas-de-segurança).

## URLs pré-assinadas geradas dentro do Docker não abrem no navegador

Seu app assina as URLs com o endpoint que ele usa, por exemplo `http://s3:9000/...`. O navegador não resolve `s3`, e trocar o host depois quebra a assinatura.

Opções:

1. **Use um único hostname que funcione dos dois lados.** Por exemplo, assine com `http://localhost:9000` e rode o app com `network_mode: host` (Linux), ou adicione um alias:
   ```yaml
   services:
     app:
       extra_hosts:
         - "s3.localhost:host-gateway"
       environment:
         AWS_ENDPOINT_URL: http://s3.localhost:9000   # Laravel: AWS_ENDPOINT
   ```
   e abra `http://s3.localhost:9000/...` no navegador (`*.localhost` resolve para 127.0.0.1 nos navegadores modernos).
2. **Use um segundo cliente só para gerar as URLs pré-assinadas**, configurado com o endpoint público (`http://localhost:9000`). A assinatura é offline, então ele não precisa alcançar o servidor.

## Upload pelo navegador bloqueado por CORS

O CORS é aberto por padrão. Se você definiu `S3LAB_CORS_ORIGINS`, inclua a origem exata (scheme + host + porta, ex.: `http://localhost:5173`). O preflight aparece no log de requests como `CORSPreflight`.

## `EntityTooSmall` no `CompleteMultipartUpload`

Toda part, exceto a última, deve ter pelo menos 5 MiB, como na AWS. Aumente o tamanho das parts.

## `EntityTooLarge`

O PUT/part único é maior que `S3LAB_MAX_OBJECT_SIZE` (padrão 5 GiB). Use multipart ou aumente o limite.

## `XAmzContentSHA256Mismatch` / `IncompleteBody`

O body recebido difere do que o cliente declarou: geralmente um proxy fazendo buffer/truncando uploads, ou um bug do cliente enviando um `Content-Length` errado.

## `NotImplemented`

O cliente chamou uma operação que o s3lab não suporta (versioning, tagging, policies…). O log de requests mostra `op: Unsupported` com o método e a query. Veja a [compatibilidade](s3-compatibility.md); abra uma issue se o seu framework precisar dela.

## SDKs recentes da AWS e checksums

Desde 2025 os SDKs da AWS adicionam checksums CRC e enviam bodies `STREAMING-UNSIGNED-PAYLOAD-TRAILER` por padrão. O s3lab suporta isso. Se um proxy antigo no meio do caminho engasga com bodies chunked, defina no cliente:

```bash
AWS_REQUEST_CHECKSUM_CALCULATION=when_required
AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
```

## Os dados somem ao reiniciar

Você está com `S3LAB_STORAGE=memory`, ou `/data` não está montado como volume. Use `-v s3lab-data:/data` com o storage `fs` padrão.

## Permission denied em `/data`

A imagem roda como usuário não-root (UID `65532`). Ao fazer bind mount de um diretório do host, deixe-o gravável para esse UID:

```bash
mkdir -p ./s3data && sudo chown 65532:65532 ./s3data
```

Volumes nomeados (`-v s3lab-data:/data`) funcionam sem isso.

## Obtendo mais detalhes

- `S3LAB_LOG_LEVEL=debug` loga toda request com seu resultado e o `requestId` no stderr (`docker logs s3lab`).
- Toda resposta tem um header `x-amz-request-id`; procure por ele no log de requests ou nos logs.
