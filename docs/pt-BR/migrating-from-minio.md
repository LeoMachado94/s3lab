# Migrando do MinIO

Na maioria dos projetos é uma mudança de uma linha: trocar a imagem. As variáveis de credenciais do MinIO continuam funcionando.

## Docker Compose

Antes:

```yaml
services:
  minio:
    image: minio/minio
    command: server /data --console-address ":9001"
    ports: ["9000:9000", "9001:9001"]
    environment:
      MINIO_ROOT_USER: admin
      MINIO_ROOT_PASSWORD: password
    volumes:
      - minio-data:/data

  createbuckets:
    image: minio/mc
    depends_on: [minio]
    entrypoint: >
      /bin/sh -c "
      mc alias set local http://minio:9000 admin password &&
      mc mb -p local/uploads &&
      mc anonymous set download local/uploads"
```

Depois:

```yaml
services:
  minio:                       # keep the service name so app config doesn't change
    image: leomachado/s3lab
    ports: ["9000:9000", "9001:9001"]
    environment:
      MINIO_ROOT_USER: admin
      MINIO_ROOT_PASSWORD: password
      S3LAB_BUCKETS: uploads   # replaces the mc init container
    volumes:
      - minio-data:/data
```

- Não precisa de `command:`. Se você mantiver `command: server /data ...`, o s3lab ignora os argumentos do MinIO e loga um aviso.
- Apague o container de init do `mc`: use `S3LAB_BUCKETS` / `S3LAB_SEED_DIR`.
- As portas são as mesmas: `9000` API, `9001` console.

## Mapeamento de variáveis de ambiente

| MinIO | s3lab |
|---|---|
| `MINIO_ROOT_USER` / `MINIO_ACCESS_KEY` | `S3LAB_ACCESS_KEY` (alias respeitado) |
| `MINIO_ROOT_PASSWORD` / `MINIO_SECRET_KEY` | `S3LAB_SECRET_KEY` (alias respeitado) |
| `MINIO_REGION` / `MINIO_SITE_REGION` | `S3LAB_REGION` (alias respeitado) |
| `MINIO_DOMAIN` | `S3LAB_DOMAIN` (alias respeitado) |
| `--console-address :9001` | `S3LAB_CONSOLE_ADDR=:9001` (padrão) |
| `mc mb local/bucket` | `S3LAB_BUCKETS=bucket` |
| `mc cp -r ./fixtures local/bucket` | `S3LAB_SEED_DIR` |
| `mc anonymous set download` | `S3LAB_ANONYMOUS=true` (global, não por bucket; **também permite escritas e remoções anônimas**) |
| `/minio/health/live` | `GET :9001/_s3lab/health` ou `s3lab healthcheck` |

## Healthchecks

Setups com MinIO costumam usar `curl -f http://localhost:9000/minio/health/live`. A imagem do s3lab não tem shell nem curl (distroless); use o comando embutido:

```yaml
healthcheck:
  test: ["CMD", "/s3lab", "healthcheck"]
  interval: 2s
  retries: 15
```

O s3lab também responde `200` a `GET /minio/health/live` e `/minio/health/ready` na porta S3, então checagens externas continuam funcionando. Apenas esses dois estão implementados (não `/minio/health/cluster`).

## Dados

Os formatos em disco são diferentes: o s3lab não consegue ler um diretório `/data` do MinIO. Comece com um volume novo ou copie os dados com os dois servidores rodando:

```bash
# with both servers up (MinIO on 9000, s3lab on 9100)
aws --endpoint-url http://localhost:9000 s3 sync s3://uploads ./export
aws --endpoint-url http://localhost:9100 s3 sync ./export s3://uploads
```

## O que você perde

- APIs específicas do MinIO (`mc admin`, bucket notifications, ILM, policies, versioning). Veja [compatibilidade](s3-compatibility.md).
- Policies anônimas por bucket: o `S3LAB_ANONYMOUS` do s3lab é tudo ou nada e, diferente de `mc anonymous set download` (somente leitura), também permite escritas e remoções anônimas.

## O que você ganha

- [Log de requests](testing.md#validando-requests-ao-s3) e [injeção de falhas](testing.md#injeção-de-falhas) para testes.
- `S3LAB_STORAGE=memory` e [reset](testing.md#reset-entre-testes) em uma chamada.
- Imagem de ~17 MB, startup quase instantâneo, licença MIT.
