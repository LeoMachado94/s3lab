# Guia de testes

O s3lab foi pensado para ser o S3 com o qual a sua suíte de testes conversa. A configuração recomendada:

1. **Um s3lab por execução de testes**, com `S3LAB_STORAGE=memory` (rápido, nada para limpar depois).
2. **Reset entre os testes** com `POST /_s3lab/reset`, para que cada teste comece do mesmo estado de seed.
3. **Valide o log de requests** (`GET /_s3lab/requests`) quando *como* seu código fala com o S3 importa.
4. **Injete falhas** para provar que retries, timeouts e tratamento de erros funcionam.

Todas as chamadas `/_s3lab/*` vão para a porta do console (`9001`) e não precisam de assinatura. Referência completa: [admin API](admin-api.md).

---

## Rodando no CI

### GitHub Actions

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      s3:
        image: leomachado94/s3lab
        ports: ["9000:9000", "9001:9001"]
        env:
          S3LAB_STORAGE: memory
          S3LAB_BUCKETS: uploads,avatars
        options: >-
          --health-cmd "/s3lab healthcheck"
          --health-interval 2s
          --health-retries 15
    env:
      AWS_ENDPOINT_URL: http://localhost:9000
      AWS_ACCESS_KEY_ID: s3lab
      AWS_SECRET_ACCESS_KEY: s3labsecret
      AWS_REGION: us-east-1
      S3LAB_ADMIN_URL: http://localhost:9001
    steps:
      - uses: actions/checkout@v4
      - run: make test
```

> Se o próprio job roda em um `container:`, use `http://s3:9000` (o nome do serviço) no lugar de `localhost`.

### GitLab CI

```yaml
test:
  image: python:3.13
  services:
    - name: leomachado94/s3lab
      alias: s3
  variables:
    S3LAB_STORAGE: memory
    S3LAB_BUCKETS: uploads
    AWS_ENDPOINT_URL: http://s3:9000
    AWS_ACCESS_KEY_ID: s3lab
    AWS_SECRET_ACCESS_KEY: s3labsecret
    AWS_REGION: us-east-1
    S3LAB_ADMIN_URL: http://s3:9001
  script:
    - pytest
```

### Docker Compose (stack local de testes)

```yaml
services:
  s3:
    image: leomachado94/s3lab
    environment:
      S3LAB_STORAGE: memory
      S3LAB_SEED_DIR: /seed
    volumes:
      - ./tests/fixtures/s3:/seed:ro
    healthcheck:
      test: ["CMD", "/s3lab", "healthcheck"]
      interval: 2s
      retries: 15
```

### Testcontainers

Go:

```go
req := testcontainers.ContainerRequest{
	Image:        "leomachado94/s3lab",
	ExposedPorts: []string{"9000/tcp", "9001/tcp"},
	Env:          map[string]string{"S3LAB_STORAGE": "memory", "S3LAB_BUCKETS": "test"},
	WaitingFor:   wait.ForHTTP("/_s3lab/health").WithPort("9001/tcp"),
}
c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
endpoint, _ := c.PortEndpoint(ctx, "9000/tcp", "http")
admin, _ := c.PortEndpoint(ctx, "9001/tcp", "http")
```

Python:

```python
from testcontainers.core.container import DockerContainer
from testcontainers.core.waiting_utils import wait_for_logs

s3lab = (DockerContainer("leomachado94/s3lab")
         .with_env("S3LAB_STORAGE", "memory")
         .with_env("S3LAB_BUCKETS", "test")
         .with_exposed_ports(9000, 9001))
s3lab.start()
wait_for_logs(s3lab, "listening")
endpoint = f"http://{s3lab.get_container_host_ip()}:{s3lab.get_exposed_port(9000)}"
```

---

## Reset entre testes

```bash
curl -X POST "http://localhost:9001/_s3lab/reset?requests=true&faults=true"
```

| Chamada | Efeito |
|---|---|
| `POST /_s3lab/reset` | Apaga todos os buckets/objetos/uploads e reaplica `S3LAB_BUCKETS` e `S3LAB_SEED_DIR` |
| `POST /_s3lab/reset?bucket=uploads` | Esvazia apenas `uploads` e refaz o seed dele |
| `&requests=true` | Também limpa o log de requests |
| `&faults=true` | Também remove todas as regras de falha |

Rode no hook "before each" do seu framework.

### Go

```go
func resetS3(t *testing.T) {
	t.Helper()
	resp, err := http.Post(os.Getenv("S3LAB_ADMIN_URL")+"/_s3lab/reset?requests=true&faults=true", "", nil)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset s3lab: %v %v", err, resp)
	}
	resp.Body.Close()
}
```

### Python (pytest)

```python
import os, pytest, requests

ADMIN = os.environ.get("S3LAB_ADMIN_URL", "http://localhost:9001")

@pytest.fixture(autouse=True)
def clean_s3():
    requests.post(f"{ADMIN}/_s3lab/reset", params={"requests": "true", "faults": "true"}).raise_for_status()
    yield
```

### JavaScript (Jest / Vitest)

```js
const ADMIN = process.env.S3LAB_ADMIN_URL ?? "http://localhost:9001";

beforeEach(async () => {
  const res = await fetch(`${ADMIN}/_s3lab/reset?requests=true&faults=true`, { method: "POST" });
  if (res.status !== 204) throw new Error(`s3lab reset failed: ${res.status}`);
});
```

### PHP / Laravel

```php
// tests/Concerns/ResetsS3lab.php
trait ResetsS3lab
{
    protected function setUpResetsS3lab(): void
    {
        Http::post(env('S3LAB_ADMIN_URL', 'http://s3:9001').'/_s3lab/reset?requests=true&faults=true')->throw();
    }
}
```

Chame `$this->setUpResetsS3lab()` no `setUp()`. Diferente do `Storage::fake()`, isso exercita o driver S3 de verdade: assinaturas, multipart, headers de visibilidade e URLs pré-assinadas.

---

## Validando requests ao S3

O log de requests registra toda chamada à porta S3:

```bash
curl "http://localhost:9001/_s3lab/requests?bucket=uploads&op=PutObject"
```

```json
{"entries": [
  {"id": 7, "op": "PutObject", "bucket": "uploads", "key": "avatars/42.png",
   "status": 200, "durationMs": 1.8, "bytesIn": 20480, "auth": "header", "...": "..."}
]}
```

Filtros: `bucket`, `op`, `key` (prefixo), `status`, `errors=true` (status ≥ 400), `since=<id>`, `limit`. Veja a [admin API](admin-api.md#log-de-requests).

O que vale a pena validar:

- **O upload realmente aconteceu**, com a key e o tamanho certos.
- **Sem chamadas desperdiçadas**: por exemplo, seu código não faz `HeadObject` antes de todo `GetObject`.
- **Multipart é usado** em arquivos grandes (`op=CreateMultipartUpload`).
- **Houve retries** após uma falha injetada (duas entradas de `PutObject`, a primeira com `503`).
- **Nada falhou em silêncio**: `errors=true` retorna uma lista vazia.

Exemplo em Python:

```python
def s3_requests(**filters):
    r = requests.get(f"{ADMIN}/_s3lab/requests", params=filters)
    r.raise_for_status()
    return r.json()["entries"]

def test_avatar_upload_writes_once(client):
    client.post("/avatar", files={"file": b"..."})
    puts = s3_requests(bucket="avatars", op="PutObject")
    assert len(puts) == 1
    assert puts[0]["key"] == "users/1.png"
    assert s3_requests(errors="true") == []
```

Use `since` para olhar apenas o que aconteceu depois de um ponto do teste:

```python
last = (s3_requests(limit=1) or [{"id": 0}])[-1]["id"]
do_something()
new_calls = s3_requests(since=last)
```

---

## Injeção de falhas

Uma regra de falha casa requests por operação, bucket e glob de key, e então retorna um erro S3 e/ou adiciona latência.

```bash
curl -X POST localhost:9001/_s3lab/faults -H 'Content-Type: application/json' -d '{
  "op": "PutObject",
  "bucket": "uploads",
  "status": 503,
  "code": "SlowDown",
  "count": 2
}'
```

| Campo | Significado |
|---|---|
| `op` | Operação S3 (`PutObject`, `GetObject`, `ListObjectsV2`, …). Vazio = qualquer |
| `bucket` | Bucket exato. Vazio = qualquer |
| `key` | Glob, ex.: `uploads/*.png` (`*` não atravessa `/`). Vazio = qualquer |
| `status` / `code` / `message` | Erro a retornar. `status: 0` = apenas latência |
| `delayMs` | Espera antes de responder (ou antes de falhar) |
| `probability` | `0.3` = 30% das requests que casam. Padrão `1` |
| `count` | Para após disparar N vezes. Padrão `0` = para sempre |
| `target` | `http` (padrão) ou `storage` (falha dentro do backend, depois da autenticação e do parsing) |

A resposta inclui um `id`; remova com `DELETE /_s3lab/faults/{id}` ou remova todas com `DELETE /_s3lab/faults`. Toda request atingida por uma regra mostra `faultId` no log de requests.

### Receitas

| O que você quer testar | Regra |
|---|---|
| Retry em throttling | `{"op":"PutObject","status":503,"code":"SlowDown","count":2}` |
| Leituras instáveis | `{"op":"GetObject","status":500,"probability":0.5}` |
| Timeout do cliente | `{"op":"GetObject","delayMs":30000}` |
| Rede lenta em tudo | `{"delayMs":800}` |
| Tratamento de arquivo inexistente | `{"op":"GetObject","key":"reports/*/*.pdf","status":404,"code":"NoSuchKey"}` |
| Erros de permissão | `{"bucket":"private","status":403,"code":"AccessDenied"}` |
| Falha na conclusão do multipart | `{"op":"CompleteMultipartUpload","status":500,"count":1}` |
| Falhar as primeiras requests após o boot | `S3LAB_FAULTS='[{"status":503,"code":"SlowDown","count":3}]'` |

Teste em Go com uma falha:

```go
func addFault(t *testing.T, rule string) {
	t.Helper()
	resp, err := http.Post(os.Getenv("S3LAB_ADMIN_URL")+"/_s3lab/faults", "application/json", strings.NewReader(rule))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("add fault: %v %v", err, resp)
	}
	resp.Body.Close()
}

type s3Request struct {
	Op        string `json:"op"`
	Status    int    `json:"status"`
	ErrorCode string `json:"errorCode"`
}

func listRequests(t *testing.T, query string) []s3Request {
	t.Helper()
	resp, err := http.Get(os.Getenv("S3LAB_ADMIN_URL") + "/_s3lab/requests?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Entries []s3Request }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Entries
}

func TestUploadRetriesOnSlowDown(t *testing.T) {
	resetS3(t)
	addFault(t, `{"op":"PutObject","status":503,"code":"SlowDown","count":1}`)

	// uploader é o seu código sob teste
	if err := uploader.Upload(ctx, "uploads", "a.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("upload should succeed after retry: %v", err)
	}
	puts := listRequests(t, "op=PutObject")
	if len(puts) != 2 || puts[0].Status != 503 || puts[1].Status != 200 {
		t.Fatalf("expected one failure then success, got %+v", puts)
	}
}
```

---

## Dicas

- **Isole testes paralelos** dando a cada teste o seu próprio bucket (ex.: `test-<random>`) em vez de fazer reset global, ou faça o reset com `?bucket=`.
- **Arquivos grandes**: o `memory` mantém tudo na RAM; para testes de vários GB use `fs` em um mount `tmpfs`.
- **Depure um teste que falha** abrindo <http://localhost:9001/#/requests> enquanto ele roda.
- **Logs**: `S3LAB_LOG_LEVEL=debug` imprime toda request no stdout (`docker logs`).
