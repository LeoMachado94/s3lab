# s3lab

**Um servidor compatível com S3, minúsculo, feito para desenvolvimento e testes de integração.**
Substituto direto do MinIO no seu `docker compose` e no CI, com as ferramentas que você realmente precisa ao testar código S3: log de requests ao vivo, injeção de falhas, reset em uma chamada e seed.

[English](../../README.md)

```bash
docker run --rm -p 9000:9000 -p 9001:9001 -e S3LAB_BUCKETS=dev leomachado/s3lab
```

- API S3: `http://localhost:9000` — access key `s3lab`, secret `s3labsecret`, região `us-east-1`
- Console: `http://localhost:9001` — login com a mesma access key e secret

```bash
aws --endpoint-url http://localhost:9000 s3 cp ./photo.jpg s3://dev/photo.jpg
```

## Por que s3lab

| | s3lab | MinIO |
|---|---|---|
| Tamanho da imagem | ~17 MB, um único binário estático | ~100+ MB |
| Startup | bem abaixo de um segundo | segundos |
| Modo em memória | ✅ `S3LAB_STORAGE=memory` | ❌ |
| Log de requests com API JSON (para fazer assert nos testes) | ✅ | ❌ |
| Injeção de falhas (erros, latência, throttling) | ✅ | ❌ |
| Reset do estado entre testes em uma chamada HTTP | ✅ | ❌ |
| Seed de buckets/arquivos no boot | ✅ | parcial |
| Licença | MIT | AGPLv3 |

O s3lab **não** é storage de produção: sem replicação, sem erasure coding, sem políticas IAM. Use onde você usaria um mock, mas com o comportamento real do S3.

## Recursos

- **API S3** usada por apps reais: buckets, objetos, ranges, requests condicionais, copy, multipart upload, batch delete, URLs pré-assinadas, CORS, SigV4 (header, presigned e payloads streaming/chunked). Veja [compatibilidade](s3-compatibility.md).
- **Dois backends**: `fs` (persistente, padrão) e `memory` (rápido, efêmero; perfeito para CI).
- **Console web**: navegue pelos buckets, faça upload/download, inspecione metadados, acompanhe requests ao vivo, gerencie regras de falha.
- **Log de requests**: toda chamada S3 registrada com operação, status, código de erro, latência e bytes, consultável via `GET /_s3lab/requests`.
- **Injeção de falhas**: faça `PutObject` retornar `503 SlowDown` em 30% das vezes, adicione 2 s de latência ao `GetObject`, falhe as 3 primeiras requests… para testar retries e timeouts.
- **Seed e reset**: crie buckets e envie arquivos no boot; restaure esse estado com `POST /_s3lab/reset`.
- **Aliases de env do MinIO**: `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` funcionam, então migrar é trocar a imagem em uma linha.

## Documentação

| Guia | O que tem |
|---|---|
| [Primeiros passos](getting-started.md) | Rodar com Docker, Compose, binário; primeiro bucket e objeto |
| [Configuração](configuration.md) | Todas as variáveis de ambiente e flags |
| [Configuração de clientes](clients.md) | AWS CLI, Go, Python, Node.js, PHP/Laravel, Java, .NET, Ruby, Django, rclone |
| [Guia de testes](testing.md) | CI (GitHub Actions, GitLab), reset entre testes, validação de requests, receitas de injeção de falhas |
| [Console web](console.md) | Tour da interface |
| [Admin API](admin-api.md) | Referência dos endpoints `/_s3lab/*` |
| [Compatibilidade S3](s3-compatibility.md) | Operações suportadas e diferenças conhecidas |
| [Migrando do MinIO](migrating-from-minio.md) | Compose lado a lado, mapeamento de env |
| [Solução de problemas](troubleshooting.md) | `SignatureDoesNotMatch`, buckets estilo DNS, CORS e mais |

## Compose rápido

```yaml
services:
  s3:
    image: leomachado/s3lab
    ports: ["9000:9000", "9001:9001"]
    environment:
      S3LAB_BUCKETS: uploads,avatars
    volumes:
      - s3data:/data
volumes:
  s3data:
```

## Licença

MIT — veja [LICENSE](../../LICENSE).
