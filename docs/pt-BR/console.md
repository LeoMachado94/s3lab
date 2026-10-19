# Console web

Abra <http://localhost:9001> e entre com a access key e a secret key do S3 (`S3LAB_ACCESS_KEY` / `S3LAB_SECRET_KEY`, padrão `s3lab` / `s3labsecret`). A sessão dura 12 horas ou até o s3lab reiniciar; o botão **Log out** fica no cabeçalho. O console fala apenas com a [admin API](admin-api.md), então tudo o que você faz aqui também pode ser automatizado via script.

## Cabeçalho

- **Versão, backend de storage e região** do servidor em execução.
- **Connect**: endpoint (o host vem do endereço do navegador), access key, um placeholder no lugar da secret key (nunca a real) e região prontos para copiar, mais um lembrete para habilitar o path-style. Cole direto no seu `.env`.
- **Reset**: apaga todos os dados e reaplica o seed. *requests* vem marcado por padrão e *faults* não; eles também limpam o log de requests e as regras de falha. O reset é global: o reset por bucket só existe pela [API](admin-api.md#reset).
- **Alternar tema**: alterna entre auto (segue o sistema operacional) → claro → escuro.

## Buckets — `#/buckets`

- Lista de buckets com data de criação.
- **Create bucket**: os nomes são validados pelas regras do S3 (3–63 caracteres, minúsculas, dígitos, `-`, `.`).
- **Delete**: recusa buckets não vazios, a menos que você marque *force* (apaga antes os objetos e uploads pendentes).

## Objetos — `#/buckets/<name>`

- Navegação estilo pastas: prefixos terminados em `/` aparecem como pastas, com um breadcrumb para voltar.
- **Filter** por prefixo, **Load more** para buckets grandes.
- **Upload**: escolha arquivos ou arraste e solte na lista; eles vão para a pasta atual, com progresso por arquivo.
- **Painel do objeto** (clique em um objeto): tamanho, ETag, última modificação, número de parts, content type, headers de sistema (`Cache-Control`, `Content-Disposition`, …), metadados do usuário (`x-amz-meta-*`), preview de imagens/texto/JSON até 1 MB, **Copy key**, **Download** e **Delete**.
- **Delete prefix**: apaga todos os objetos sob o prefixo atual; na raiz do bucket, esvazia o bucket.

## Requests — `#/requests`

O log ao vivo de toda request S3 que suas aplicações enviam.

- Novas requests chegam em streaming assim que acontecem (Server-Sent Events).
- Colunas: horário, método, operação, bucket/key, status (colorido), código de erro, duração, bytes de entrada/saída e um badge quando uma regra de falha foi acionada.
- **Filtros**: bucket, operação, prefixo da key, somente erros.
- **Pause** congela a visualização enquanto o tráfego continua sendo registrado; **Clear** esvazia o log.
- Clique em uma linha para ver a entrada completa em JSON (request ID, user agent, método de autenticação, endereço remoto…).

Usos típicos: entender por que um `SignatureDoesNotMatch` acontece (host errado ou configuração de path-style), conferir quantas chamadas um carregamento de página faz, confirmar que o multipart entra em ação em uploads grandes.

## Faults — `#/faults`

- Regras com suas condições de match, efeito e **hits** (atualiza a cada 3 s).
- **Presets**: um clique para adicionar cenários comuns (503 SlowDown no PutObject, GetObject falhando 50% das vezes, 2 s de latência em tudo, throttling nas 3 primeiras requests).
- Formulário de **regra customizada** com todos os campos, exceto `id`, da [referência de regras](admin-api.md#injeção-de-falhas).
- Apague uma regra ou todas.

## Desabilitando o console

`S3LAB_CONSOLE_ENABLED=false` desliga a UI **e** a admin API. Só a porta S3 continua de pé.
