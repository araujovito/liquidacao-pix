# Especificação — Motor de Liquidação de Transferências Instantâneas

Versão revisada do texto que deu origem ao projeto. Cada ponto fraco do texto
original foi corrigido; o motivo está na seção "O que mudou em relação à ideia
original", no fim.

## Objetivo

Um motor que recebe pedidos de transferência instantânea no formato do PIX,
aceita o pedido em poucos milissegundos, liquida de forma assíncrona e **prova**,
com testes e medições, quatro propriedades:

1. **Nenhum pedido aceito se perde**, mesmo com o Redis, o processador ou o
   Postgres caindo no meio da carga.
2. **Nenhum pedido é liquidado duas vezes**, mesmo que a fila entregue a mesma
   mensagem mais de uma vez ou que o cliente reenvie o pedido.
3. **O dinheiro é conservado**: a soma de todos os lançamentos é sempre zero e
   nenhuma conta de cliente fica com saldo negativo, sob qualquer nível de
   concorrência.
4. **A trilha de auditoria denuncia adulteração**: alterar ou apagar uma linha
   antiga é detectado na verificação.

O domínio é brasileiro (PIX, CPF, LGPD); a engenharia segue o que as empresas de
pagamento europeias exigem (GDPR, DORA).

## Arquitetura

```
cliente ──HTTP──▶ API (Go) ──uma transação──▶ Postgres
                    │                          ├─ transferencias (PENDENTE)
                    │ 202 Accepted             └─ outbox
                    ▼
            GET /transferencias/{id}           relay ──XADD──▶ Redis Stream
            (consulta de status)                                   │
                                                 XREADGROUP        ▼
                                       worker (Go) ◀───────── grupo de consumo
                                          │
                                          └─ uma transação: trava contas em ordem,
                                             grava lançamentos, saldo, status e
                                             auditoria; depois XACK
```

### API de ingestão (`cmd/api`)
- `POST /transferencias` valida o JSON, grava a transferência como `PENDENTE`
  **e** um registro na tabela `outbox` na mesma transação do Postgres, e só então
  responde `202 Accepted` com o identificador e o link de status.
- Idempotência pela chave `end_to_end_id` (o identificador de ponta a ponta do
  PIX): reenviar o mesmo pedido devolve o pedido já gravado; reenviar a mesma
  chave com conteúdo diferente devolve `409 Conflict`.
- `GET /transferencias/{id}` devolve o status (`PENDENTE`, `LIQUIDADA`,
  `REJEITADA` e o motivo).
- O 202 só sai depois do commit no Postgres. A fila não é a fonte da verdade.

### Relay da outbox (`cmd/relay`)
- Lê a `outbox` com `FOR UPDATE SKIP LOCKED` (vários relays podem rodar ao mesmo
  tempo), publica no Redis Stream com `XADD` e marca como publicado.
- Se o Redis estiver fora, os pedidos esperam na outbox; nada se perde.
- Pode publicar a mesma mensagem duas vezes (cai depois do `XADD` e antes de
  marcar). Isso é aceito de propósito: quem garante "uma vez só" é o worker.

### Fila (Redis Streams)
- Redis **Streams** com grupo de consumidores, não Pub/Sub: a mensagem fica
  guardada até o worker confirmar (`XACK`); se o worker morrer, outro reivindica
  a mensagem pendente (`XCLAIM`; ver ADR 0003 sobre por que não `XAUTOCLAIM`).
- Novas tentativas com espera crescente; depois de N falhas a mensagem vai para
  um stream de mensagens mortas (*dead letter*), com o erro anotado.

### Worker de liquidação (`cmd/worker`)
- Abre uma transação, trava a transferência e as duas contas com
  `SELECT … FOR UPDATE` **sempre em ordem crescente de id** (evita *deadlock*
  entre A→B e B→A simultâneas).
- Se a transferência já não estiver `PENDENTE`, não faz nada (idempotência do
  lado do consumo).
- Saldo insuficiente → `REJEITADA` com motivo. Caso contrário grava dois
  lançamentos (débito e crédito), atualiza os saldos e muda o status para
  `LIQUIDADA`.

### Livro-razão de partidas dobradas
- `lancamentos` é só de inserção (um gatilho no banco bloqueia `UPDATE` e
  `DELETE`). O saldo da conta é uma projeção mantida na mesma transação.
- Cada transferência gera lançamentos que somam zero. O dinheiro entra no
  sistema por uma conta técnica de liquidez, a única que pode ficar negativa;
  assim a soma de **todos** os lançamentos é sempre zero.
- Valores em centavos, `bigint`. Nunca ponto flutuante.

### Conciliação (`cmd/conciliador` ou rotina do worker)
- Confere: soma global dos lançamentos = 0; saldo de cada conta = soma dos
  lançamentos dela; toda transferência `LIQUIDADA` tem exatamente dois
  lançamentos que somam zero. Qualquer divergência é alerta.

### Auditoria e dados pessoais
- Tabela de auditoria só de inserção, em **cadeia de hashes**: cada linha guarda
  o HMAC-SHA256 do conteúdo + o hash da linha anterior. Alterar ou apagar uma
  linha quebra a cadeia, e o verificador aponta onde.
- O CPF do titular é gravado **cifrado** (AES-GCM) com uma chave por titular. O
  direito ao apagamento (GDPR art. 17 / LGPD art. 18) é atendido apagando a
  chave (*crypto-shredding*): o histórico financeiro, que a regulação obriga a
  guardar, continua íntegro, e o dado pessoal fica ilegível.

## Resiliência (o que a DORA pede, em forma de teste)
- **Teste de caos**: durante um teste de carga, derrubar o Redis, o worker e o
  Postgres, um de cada vez; depois conferir que toda transferência aceita foi
  liquidada ou rejeitada, nenhuma duas vezes, e que a conciliação fecha.
- **Runbook** curto em `docs/runbook.md`: o que fazer quando a fila cresce,
  quando há mensagens mortas, quando a conciliação diverge.

## Desempenho
- Teste de carga com **k6**; o README traz os números (requisições por segundo,
  p50/p95/p99 da API, atraso da fila) e a máquina em que foram medidos. Nenhum
  número de vazão aparece no README sem medição por trás.

## Observabilidade
- Logs estruturados (`log/slog`, JSON) com o id da transferência.
- Rastreamento com OpenTelemetry da API até o worker, passando o contexto do
  rastro dentro da mensagem do stream.
- Métricas no Prometheus (pedidos aceitos, liquidados, rejeitados, tamanho da
  outbox, atraso do stream, mensagens mortas) e um painel no Grafana já
  provisionado no `docker-compose`.

## Qualidade
- Testes de unidade no domínio; testes de integração com Postgres e Redis reais
  em contêiner (Testcontainers), também no CI.
- Teste de concorrência: milhares de transferências cruzadas e simultâneas entre
  poucas contas; ao final nenhum saldo de cliente negativo e conciliação fechada.
- CI no GitHub Actions: `gofmt`, `go vet`, `golangci-lint`, testes com `-race`.
- Decisões registradas em `docs/adr/` (um arquivo curto por decisão).

## Estrutura do repositório

```
├── .github/workflows/     # CI: lint + testes (unidade e integração)
├── cmd/
│   ├── api/               # ingestão HTTP
│   ├── relay/             # outbox → Redis Stream
│   └── worker/            # liquidação
├── internal/
│   ├── domain/            # entidades e regras puras (Conta, Transferência, Lançamento)
│   ├── usecase/           # casos de uso (aceitar pedido, liquidar, conciliar)
│   ├── repository/        # Postgres (pgx, SQL escrito à mão)
│   ├── queue/             # Redis Streams
│   └── web/               # handlers e rotas HTTP
├── db/migrations/         # SQL versionado
├── deployments/           # Dockerfile, docker-compose, Prometheus, Grafana
├── tests/carga/           # scripts k6
├── docs/                  # especificação, roteiro, ADRs, runbook
├── MEMORIA_TECNICA.md     # estado atual, decisões e histórico
├── README.md              # em português, com resumo em inglês no topo
└── Makefile
```

## O que mudou em relação à ideia original

| Ideia original | Problema | Correção |
|---|---|---|
| Redis Pub/Sub como fila | Pub/Sub não guarda mensagem; worker fora do ar = pedido perdido | Redis Streams com grupo de consumo + outbox no Postgres |
| 202 logo após jogar na fila | Se a fila cair, o pedido aceito some | 202 só depois do commit no Postgres (outbox) |
| Sem forma de saber o resultado | Cliente fica sem resposta | `GET /transferencias/{id}` |
| "Milhares de requisições por segundo" | Afirmação sem prova | Teste de carga k6 com números e máquina no README |
| "Log de auditoria criptografado" | Criptografia esconde, mas não impede adulteração | Cadeia de hashes com HMAC; criptografia só no dado pessoal |
| Citar GDPR e DORA | Sem nada concreto, vira enfeite | *Crypto-shredding*; teste de caos e runbook |
| Debitar e creditar o saldo | Sem trilha; difícil conciliar | Livro-razão de partidas dobradas + conciliação |
| Sem tratamento de repetição | Fila entrega "pelo menos uma vez" | Idempotência na API (`end_to_end_id`) e no worker (status sob trava) |
| Sem cuidado com concorrência | *Deadlock* e saldo negativo | Trava em ordem fixa + teste com milhares de transferências simultâneas |
| Só `cmd/api` | Faltam os outros programas | `cmd/api`, `cmd/relay`, `cmd/worker` |
| README só em português | Recrutador irlandês não lê | Resumo em inglês no topo |
