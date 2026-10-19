# Primeiros passos

## 1. Rode o s3lab

### Docker

```bash
docker run --rm --name s3lab \
  -p 9000:9000 -p 9001:9001 \
  -e S3LAB_BUCKETS=dev \
  leomachado94/s3lab
```

Os dados ficam em `/data` dentro do container. Para mantê-los entre reinícios, use um volume:

```bash
docker run -d --name s3lab -p 9000:9000 -p 9001:9001 -v s3lab-data:/data leomachado94/s3lab
```

Ou dispense o disco por completo (mais rápido, tudo se perde ao parar):

```bash
docker run --rm -p 9000:9000 -e S3LAB_STORAGE=memory leomachado94/s3lab
```

### Docker Compose

```yaml
services:
  s3:
    image: leomachado94/s3lab
    ports:
      - "9000:9000"   # S3 API
      - "9001:9001"   # console + admin API
    environment:
      S3LAB_ACCESS_KEY: local
      S3LAB_SECRET_KEY: local-secret
      S3LAB_BUCKETS: uploads,avatars
    volumes:
      - s3data:/data
    healthcheck:
      test: ["CMD", "/s3lab", "healthcheck"]
      interval: 2s
      timeout: 2s
      retries: 15

  app:
    build: .
    depends_on:
      s3:
        condition: service_healthy
    environment:
      AWS_ENDPOINT_URL: http://s3:9000
      AWS_ACCESS_KEY_ID: local
      AWS_SECRET_ACCESS_KEY: local-secret
      AWS_REGION: us-east-1

volumes:
  s3data:
```

> Dentro da rede do compose o endpoint é `http://s3:9000` (nome do serviço); da sua máquina é `http://localhost:9000`.

### Binário

Baixe na [página de releases](https://github.com/LeoMachado94/s3lab/releases) ou compile:

```bash
go install github.com/LeoMachado94/s3lab/cmd/s3lab@latest
s3lab --data-dir ./data --buckets dev
```

## 2. Credenciais padrão

| Configuração | Padrão | Altere com |
|---|---|---|
| Endpoint | `http://localhost:9000` | `S3LAB_ADDR` |
| Access key | `s3lab` | `S3LAB_ACCESS_KEY` |
| Secret key | `s3labsecret` | `S3LAB_SECRET_KEY` |
| Região | `us-east-1` | `S3LAB_REGION` |
| Endereçamento | path-style (`http://host:9000/bucket/key`) | `S3LAB_DOMAIN` para estilo virtual-host |

A maioria dos SDKs precisa do **path-style** habilitado ao usar um endpoint customizado. Veja [configuração de clientes](clients.md).

## 3. Primeiro bucket e objeto

Com o AWS CLI:

```bash
export AWS_ACCESS_KEY_ID=s3lab AWS_SECRET_ACCESS_KEY=s3labsecret AWS_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:9000   # AWS CLI >= 2.13

aws s3 mb s3://photos
aws s3 cp ./cat.jpg s3://photos/2026/cat.jpg
aws s3 ls s3://photos --recursive
aws s3 presign s3://photos/2026/cat.jpg --expires-in 300
```

Com `curl` puro (sem assinatura: use a admin API):

```bash
curl -X POST localhost:9001/_s3lab/buckets -d '{"name":"photos"}'
curl -X PUT "localhost:9001/_s3lab/buckets/photos/object?key=hello.txt" \
  -H 'Content-Type: text/plain' --data-binary 'hello'
curl "localhost:9001/_s3lab/buckets/photos/objects"
```

## 4. Abra o console

Acesse <http://localhost:9001>: navegue pelos buckets, envie arquivos e abra a aba **Requests** para ver ao vivo cada chamada S3 que seu app faz. Veja [console](console.md).

## 5. Próximos passos

- Configure sua linguagem: [configuração de clientes](clients.md)
- Use no CI e nos testes: [guia de testes](testing.md)
- Ajuste o servidor: [configuração](configuration.md)
