# Configuração

Toda configuração pode ser passada como variável de ambiente ou flag de linha de comando. Flags têm prioridade sobre variáveis de ambiente.

```bash
s3lab [serve] [flags]     # default command
s3lab healthcheck         # exits 0 if the console health endpoint answers (used by Docker HEALTHCHECK)
s3lab version
```

## Referência

| Variável de ambiente | Flag | Padrão | Descrição |
|---|---|---|---|
| `S3LAB_ADDR` | `--addr` | `:9000` | Endereço de escuta da API S3 |
| `S3LAB_CONSOLE_ADDR` | `--console-addr` | `:9001` | Endereço de escuta do console (UI) + admin API |
| `S3LAB_CONSOLE_ENABLED` | `--console` | `true` | Use `false` para desabilitar totalmente o console e a admin API |
| `S3LAB_ACCESS_KEY` | `--access-key` | `s3lab` | Access key com a qual os clientes devem assinar |
| `S3LAB_SECRET_KEY` | `--secret-key` | `s3labsecret` | Secret key |
| `S3LAB_REGION` | `--region` | `us-east-1` | Região retornada por `GetBucketLocation`. Requests assinadas para **qualquer** região são aceitas |
| `S3LAB_ANONYMOUS` | `--anonymous` | `false` | Aceita requests sem assinatura (acesso anônimo a tudo) |
| `S3LAB_STORAGE` | `--storage` | `fs` | `fs` (persistente em disco) ou `memory` (perdido ao encerrar) |
| `S3LAB_DATA_DIR` | `--data-dir` | `/data` (imagem), `./data` (binário) | Diretório raiz do backend `fs` |
| `S3LAB_BUCKETS` | `--buckets` | — | Buckets separados por vírgula, criados no boot e após o reset, ex.: `uploads,avatars` |
| `S3LAB_SEED_DIR` | `--seed-dir` | — | Diretório copiado para os buckets no boot e após o reset (veja abaixo) |
| `S3LAB_FAULTS` | `--faults` | — | Array JSON de regras de falha carregadas no boot (veja [testes](testing.md#injeção-de-falhas)) |
| `S3LAB_DOMAIN` | `--domain` | — | Habilita o estilo virtual-host: `<bucket>.<domain>`. O path-style continua funcionando |
| `S3LAB_CORS_ORIGINS` | `--cors-origins` | `*` | Origens permitidas para requests de navegador, separadas por vírgula. String vazia desabilita o CORS |
| `S3LAB_MAX_OBJECT_SIZE` | `--max-object-size` | `5GiB` | Tamanho máximo de um único PUT ou part. Aceita `B`, `KiB`, `MiB`, `GiB` |
| `S3LAB_REQUEST_LOG_SIZE` | `--request-log-size` | `1000` | Número de requests mantidas no log de requests (buffer circular) |
| `S3LAB_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error` (`debug` loga toda request) |
| `S3LAB_LOG_FORMAT` | `--log-format` | `text` | `text` ou `json` |

### Aliases de compatibilidade com MinIO

Usados apenas quando o equivalente `S3LAB_*` não está definido:

| Variável do MinIO | Equivale a |
|---|---|
| `MINIO_ROOT_USER`, `MINIO_ACCESS_KEY` | `S3LAB_ACCESS_KEY` |
| `MINIO_ROOT_PASSWORD`, `MINIO_SECRET_KEY` | `S3LAB_SECRET_KEY` |
| `MINIO_REGION`, `MINIO_SITE_REGION` | `S3LAB_REGION` |
| `MINIO_DOMAIN` | `S3LAB_DOMAIN` |

## Backends de storage

| | `fs` | `memory` |
|---|---|---|
| Sobrevive a reinícios | ✅ (monte `/data`) | ❌ |
| Velocidade | rápido | o mais rápido |
| Limite de tamanho do objeto | espaço em disco | RAM |
| Ideal para | desenvolvimento local | CI, testes unitários/de integração |

> O backend `fs` usa um layout próprio em `/data` (os nomes dos arquivos são hasheados, então qualquer key S3 é segura). Não edite os arquivos ali manualmente; use a API S3, o console ou `S3LAB_SEED_DIR`.

## Seed

`S3LAB_BUCKETS` cria buckets vazios. `S3LAB_SEED_DIR` envia arquivos: cada **diretório de primeiro nível** vira um bucket e cada arquivo dentro dele vira um objeto cuja key é o caminho relativo.

```
seed/
├── avatars/
│   └── default.png        → s3://avatars/default.png
└── documents/
    └── terms/v1.pdf       → s3://documents/terms/v1.pdf
```

```yaml
services:
  s3:
    image: leomachado94/s3lab
    environment:
      S3LAB_SEED_DIR: /seed
    volumes:
      - ./tests/fixtures/s3:/seed:ro
```

O Content-Type é deduzido pela extensão do arquivo. O seed roda na inicialização e a cada `POST /_s3lab/reset`. Objetos existentes com a mesma key são sobrescritos.

## Notas de segurança

O s3lab é uma ferramenta de desenvolvimento:

- O console/admin API (porta 9001) **não tem autenticação**. Qualquer pessoa que alcance essa porta pode ler e apagar dados. Não a exponha publicamente; em ambientes compartilhados, defina `S3LAB_CONSOLE_ENABLED=false` ou faça o bind em localhost (`S3LAB_CONSOLE_ADDR=127.0.0.1:9001`).
- Troque as credenciais padrão se a porta S3 for acessível por outras pessoas.
- Não há TLS. Coloque um reverse proxy na frente se precisar de HTTPS.
