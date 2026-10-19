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
| `S3LAB_CONSOLE_ENABLED` | `--console` (`--console=false`) | `true` | Use `false` para desabilitar totalmente o console e a admin API |
| `S3LAB_CONSOLE_AUTH` | `--console-auth` | `true` | Exige login (access key + secret key do S3) no console e na admin API. `false` deixa os dois abertos. Veja [notas de segurança](#notas-de-segurança) |
| `S3LAB_CONSOLE_ALLOWED_HOSTS` | `--console-allowed-hosts` | — | `Host` extras (separados por vírgula) aceitos pelo console/admin API (veja [notas de segurança](#notas-de-segurança)) |
| `S3LAB_ACCESS_KEY` | `--access-key` | `s3lab` | Access key com a qual os clientes devem assinar |
| `S3LAB_SECRET_KEY` | `--secret-key` | `s3labsecret` | Secret key |
| `S3LAB_REGION` | `--region` | `us-east-1` | Região retornada por `GetBucketLocation`. Requests assinadas para **qualquer** região são aceitas |
| `S3LAB_ANONYMOUS` | `--anonymous` | `false` | Aceita requests sem assinatura (acesso anônimo a tudo, **inclusive escritas e remoções**) |
| `S3LAB_STORAGE` | `--storage` | `fs` | `fs` (persistente em disco) ou `memory` (perdido ao encerrar) |
| `S3LAB_DATA_DIR` | `--data-dir` | `/data` (imagem), `./data` (binário) | Diretório raiz do backend `fs` |
| `S3LAB_BUCKETS` | `--buckets` | — | Buckets separados por vírgula, criados no boot e após o reset, ex.: `uploads,avatars` |
| `S3LAB_SEED_DIR` | `--seed-dir` | — | Diretório copiado para os buckets no boot e após o reset (veja abaixo) |
| `S3LAB_FAULTS` | `--faults` | — | Array JSON de regras de falha carregadas no boot (veja [testes](testing.md#injeção-de-falhas)) |
| `S3LAB_DOMAIN` | `--domain` | — | Habilita o estilo virtual-host: `<bucket>.<domain>`. O path-style continua funcionando |
| `S3LAB_CORS_ORIGINS` | `--cors-origins` | `*` | Origens permitidas para requests de navegador, separadas por vírgula. String vazia desabilita o CORS |
| `S3LAB_MAX_OBJECT_SIZE` | `--max-object-size` | `5GiB` | Tamanho máximo de um único PUT ou part; também limita uploads pelo console/admin API (`413 EntityTooLarge`). Veja [tamanhos](#validação-e-flags) |
| `S3LAB_REQUEST_LOG_SIZE` | `--request-log-size` | `1000` | Número de requests mantidas no log de requests (buffer circular, máximo `100000`) |
| `S3LAB_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error` (`debug` loga toda request). Os logs vão para o stderr |
| `S3LAB_LOG_FORMAT` | `--log-format` | `text` | `text` ou `json` |

### Validação e flags

- A inicialização falha se a access key ou a secret key estiver vazia, a menos que `S3LAB_ANONYMOUS=true`. A região não pode ser vazia. `S3LAB_REQUEST_LOG_SIZE` aceita no máximo `100000`.
- Flags booleanas aceitam `--console=false` e `--console false`.
- Tamanhos (`S3LAB_MAX_OBJECT_SIZE`): `B`, `KiB`, `MiB`, `GiB`, `TiB` (base 1024), `KB`, `MB`, `GB`, `TB` (base 1000) ou bytes puros; sem diferenciar maiúsculas de minúsculas.
- `s3lab healthcheck` consulta o endpoint de health do console (`127.0.0.1` e depois `[::1]`, na porta de `S3LAB_CONSOLE_ADDR`). Com o console desabilitado, consulta `GET /minio/health/live` na porta S3. Ele lê **apenas variáveis de ambiente**, não flags da CLI; no Docker, defina as portas via env (`S3LAB_CONSOLE_ADDR`), não com `--console-addr`.
- Os logs são escritos no stderr.

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
    image: leomachado/s3lab
    environment:
      S3LAB_SEED_DIR: /seed
    volumes:
      - ./tests/fixtures/s3:/seed:ro
```

O Content-Type é deduzido pela extensão do arquivo. O seed roda na inicialização e a cada `POST /_s3lab/reset`. Objetos existentes com a mesma key são sobrescritos.

Regras do seed:

- Arquivos diretamente na raiz do diretório de seed (fora de um diretório de bucket) são ignorados.
- `.DS_Store` e `Thumbs.db` são pulados.
- Symlinks que escapam do diretório do seu bucket são pulados.
- Um diretório de primeiro nível com nome inválido de bucket aborta a inicialização.

## Notas de segurança

O s3lab é uma ferramenta de desenvolvimento:

- O console/admin API (porta 9001) **exige login** com a access key e a secret key do S3 (HTTP Basic auth ou o cookie de sessão do console). Troque os padrões (`s3lab` / `s3labsecret`) sempre que outras pessoas alcançarem a porta. Continua sendo ferramenta de dev: HTTP puro, sem rate limit além de 1 s de espera por tentativa errada. Não exponha publicamente; em ambientes compartilhados, faça também o bind em localhost ou defina `S3LAB_CONSOLE_ENABLED=false`.
- `S3LAB_CONSOLE_AUTH=false` remove o login. Use só quando a porta estiver publicada apenas no loopback.
- O bind em localhost (`S3LAB_CONSOLE_ADDR=127.0.0.1:9001`) vale para o binário puro. No Docker, faça o bind normal dentro do container e publique a porta apenas no loopback: `-p 127.0.0.1:9001:9001`.
- **Allowlist de Host (proteção contra DNS rebinding).** O console/admin API só responde a valores de `Host` que sejam IPs literais, `localhost` / `*.localhost`, nomes de um único label (nomes de serviço do Docker, como `s3`), `*.local`, `*.test`, `*.internal`, ou listados em `S3LAB_CONSOLE_ALLOWED_HOSTS`. Qualquer outro recebe `403 Forbidden`.
- **Regra de cross-origin.** Requests que não sejam GET e tragam um header `Origin` cujo host difere do `Host` recebem `403 Forbidden`.
- Troque as credenciais padrão se a porta S3 for acessível por outras pessoas.
- Não há TLS. Coloque um reverse proxy na frente se precisar de HTTPS.
