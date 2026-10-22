# Memória técnica

Documento para retomar o trabalho sem depender de memória de ninguém: onde o
projeto está, como rodar, por que cada decisão foi tomada e o que já foi
tentado. É atualizado no mesmo commit da mudança que descreve.

- Especificação: [`docs/especificacao.md`](docs/especificacao.md)
- Roteiro com o andamento: [`docs/roteiro.md`](docs/roteiro.md)
- Pendências da revisão do código: [`docs/melhorias.md`](docs/melhorias.md)
- Decisões: [`docs/adr/`](docs/adr/)

## Estado atual

Fases 0 a 4 concluídas: fluxo completo API → outbox → relay → Redis Stream →
worker, resiliente a SIGKILL em cada serviço e à perda total do stream
(teste de caos passando). Próximo passo: fase 5 (teste de carga com k6).

## Ambiente

- Go 1.27 (o `go.mod` declara a versão mínima).
- Docker com Compose v2 para Postgres 17 e Redis 7.
- Nenhuma credencial real: todas as senhas do `docker-compose` são de
  desenvolvimento. O código não tem URL padrão de banco nem de Redis; o
  `Makefile` exporta `DATABASE_URL` e `REDIS_URL` de desenvolvimento.

## Como rodar

```sh
make infra          # Postgres e Redis via docker compose
make migrar         # aplica db/migrations
make semear         # cliente-0001@pix … cliente-0100@pix com R$ 10.000 cada
make api            # API em :8080 (PORTA muda)
make relay          # outbox → Redis Stream
make worker         # liquida (WORKERS=8 consumidores por processo)
make sistema        # tudo em contêineres (imagem distroless, usuário não-root)
make caos           # teste de caos contra o sistema em contêineres (~2 min)
make teste          # unidade + integração (Testcontainers sobe contêineres próprios)
make teste-unidade  # só o que roda sem Docker (-short)
make lint           # gofmt, go vet, golangci-lint
```

Exemplo de pedido:

```sh
curl -i -X POST localhost:8080/transferencias -d '{
  "end_to_end_id": "E1234567820261021200000000000001",
  "chave_pagador": "cliente-0001@pix",
  "chave_recebedor": "cliente-0002@pix",
  "valor_centavos": 2500}'
```

Os testes de integração não usam o Postgres do `make infra`: cada pacote sobe
o seu contêiner e cada teste recebe um banco novo e migrado
(`internal/testinfra`).

## Decisões em vigor

| # | Decisão | Por quê |
|---|---|---|
| 1 | Go | Pedido original; concorrência leve (goroutines) e binários estáticos |
| 2 | Postgres é a fonte da verdade; a fila só transporta | Fila pode perder ou repetir; o banco tem transação |
| 3 | Outbox na mesma transação do pedido | 202 só depois do commit; Redis fora do ar não perde pedido |
| 4 | Redis Streams, não Pub/Sub | Pub/Sub não guarda mensagem |
| 5 | Entrega "pelo menos uma vez" + idempotência no consumo | "Exatamente uma vez" na fila não existe; no banco, sob trava, sim |
| 6 | Livro-razão de partidas dobradas, valores em centavos `bigint` | Conservação do dinheiro verificável; sem erro de arredondamento |
| 7 | Contas travadas em ordem crescente de id | Evita *deadlock* entre transferências cruzadas |
| 8 | Erros HTTP em *problem details* (RFC 9457) | Cliente distingue casos por `type` estável |
| 9 | Conta técnica invisível para a API | Ela pode ficar negativa; debitá-la criaria dinheiro |
| 10 | Confirmar = `XACK` + `XDEL`; sem `MAXLEN` | `MAXLEN` pode apagar mensagem não confirmada |
| 11 | Retentativa com espera base·2^(n-1), máx. 6 entregas, depois mortas | Não martelar o banco caído; não tentar para sempre |
| 12 | Varredura de órfãs no relay (30 s, até 5×) | Redis pode perder dados; o banco não deixa pedido sumir |

## Parâmetros de produção (cmd/relay, cmd/worker)

| Parâmetro | Valor | Onde |
|---|---|---|
| Lote do relay | 200 (`RELAY_LOTE`) | `cmd/relay` |
| Espera com outbox vazia | 10 ms | `cmd/relay` |
| Idade de órfã / máx. republicações | 30 s / 5 | `cmd/relay` |
| Consumidores por processo | 8 (`WORKERS`) | `cmd/worker` |
| Espera base / máx. entregas | 5 s / 6 (≈2,5 min até morrer) | `cmd/worker` |

Os testes usam tempos menores (espera base 200 ms, órfã 1 s).

## Teste de caos — última rodada de referência

Máquina: notebook AMD Ryzen 7 7735HS (16 threads), 13 GB de RAM, tudo no
mesmo Docker. Carga: 16 clientes por 45 s, sem limite de taxa.

| Momento | Falha |
|---|---|
| 4 s → 9 s | SIGKILL no Redis |
| 12 s → 16 s | SIGKILL no worker |
| 19 s → 25 s | SIGKILL no Postgres |
| 28 s → 32 s | SIGKILL no relay |
| 35 s → 38 s | SIGKILL na API |
| 41 s | stream do Redis apagado (perda total da fila) |

Resultado: 63.735 aceitas, 280 retentativas do cliente, 0 desistências;
todas liquidadas 37 s depois do fim da carga; 38.993 recuperadas pela
varredura de órfãs (as que estavam no stream apagado); conciliação fechada e
dinheiro dos clientes conservado.

Observação para a fase 5: a entrada (~1.400–1.900/s) superou a vazão do
worker (~2.300/s em regime, menos durante as falhas), e a fila acumulou
dezenas de milhares de mensagens. É o que o teste de carga vai medir.

## Histórico

### 2026-10-21 — teste de caos e duas falhas que ele encontrou
- `tests/caos`: carga + SIGKILL em cada serviço + stream apagado; cliente
  reenvia o mesmo pedido em 5xx/erro de rede; verificação no fim.
- **Falha 1 — varredura de órfãs gerava duplicatas.** A versão inicial
  republicava toda PENDENTE publicada havia mais de 30 s. Com a fila
  atrasada, as que só esperavam eram tratadas como perdidas: 14.417
  republicações numa rodada sem perda real. Correção: a outbox guarda o id
  da mensagem (migração 0004) e o relay pergunta ao Redis (`XRANGE id id`)
  se ela ainda existe. Teste de regressão + mutação.
- **Falha 1b — varredura lenta.** Olhava só as 1.000 primeiras candidatas a
  cada 15 s (~67/s): com 50 mil mensagens perdidas não convergia em 4 min.
  Agora percorre todas em páginas com cursor.
- **Falha 2 — workers presos em NOGROUP.** Apagar o stream apaga o grupo de
  consumo; o `XADD` do relay recria o stream sem grupo, e o grupo só era
  criado na partida do worker. Correção: worker recria o grupo ao receber
  `NOGROUP`. Teste de regressão.
- Dockerfile multi-stage, imagem distroless não-root com todos os programas;
  serviços no compose sob o profile `sistema`.
- `docs/runbook.md`.
- `LOG_TESTE=1` mostra os logs do relay/worker nos testes de fila.

### 2026-10-21 — fila resiliente
- `internal/queue`: Streams, grupo `liquidadores`, chaves
  `transferencias`, `transferencias:mortas`, `transferencias:erros` (último
  erro por mensagem).
- Relay e worker como laços em `internal/usecase`; comandos em `cmd/relay` e
  `cmd/worker`. Erro de Redis/Postgres não derruba nenhum dos dois: esperam
  (até 5 s) e tentam de novo.
- Worker liquida com contexto próprio (10 s), desligado do SIGTERM: o
  encerramento não desfaz uma liquidação no meio.
- Testes com Redis real: fluxo completo (300 transferências, 3 workers),
  mensagem repetida 4×, worker que morre sem confirmar, falha persistente →
  mortas (com checagem de que houve espera), Redis fora do ar, stream apagado.
- Mutação: com a varredura de órfãs desligada, o teste do stream apagado
  fica com 1 pendente para sempre.
- Ponta a ponta manual com os binários: aceita → liquidada em ~9 ms.
- Observação: durante a fase 2, uma revisão do código corrigiu um estouro em
  `Centavos.String` (`math.MinInt64`) e acrescentou fuzz tests ao domínio;
  essas mudanças entraram no commit da API. O gosec acusou as conversões int64→uint64; foram
  anotadas, sem mudar o comportamento.

### 2026-10-21 — API de ingestão
- `POST /transferencias`: chaves PIX → ids, validação, transferência + outbox
  na mesma transação, 202 com `Location`. Repetição idêntica devolve o mesmo
  corpo com `Idempotent-Replayed: true`; mesma chave com outro conteúdo → 409.
- Teste com 20 pedidos simultâneos iguais: um id, uma linha na outbox.
- Furo encontrado e fechado durante a escrita: a chave `liquidez@sistema`
  era aceita como pagador, e a conta de liquidez pode ficar negativa, ou
  seja, qualquer cliente criaria dinheiro. `IDDeContaCliente` ignora contas
  técnicas; há teste.
- Prazos no `http.Server` (sem eles um cliente lento segura a conexão) e
  encerramento gracioso de 15 s no SIGTERM.
- Status de erro: 400 JSON malformado/campo desconhecido, 413 corpo > 4 KiB,
  422 chave desconhecida ou regra de domínio, 409 conflito.

### 2026-10-21 — liquidação, conciliação e teste de concorrência
- `usecase.Liquidar`: trava a transferência, depois as contas em ordem
  crescente de id, decide (`domain.Decidir`), grava lançamentos + saldo e
  finaliza. Transferência já finalizada → devolve o gravado sem mexer.
- `repository.Conciliar`: soma global zero, saldo = soma dos lançamentos,
  liquidada tem débito + crédito do valor certo; tudo numa fotografia
  `REPEATABLE READ`.
- `TestConcorrenciaConservaDinheiro`: 4.000 transferências entre 12 contas,
  cada uma liquidada 2× em paralelo por 32 goroutines; ~5 s. Resultado com a
  semente 42: 3.494 liquidadas, 506 rejeitadas.
- **Teste de mutação** (para provar que o teste pega o erro):
  - sem ordenar as travas → 189 deadlocks (40P01) e falha. Antes do prazo de
    60 s no teste, essa regressão só deixava o teste lento (10 min) em vez de
    falhar; por isso o prazo existe.
  - sem a checagem de status final → a restrição
    `UNIQUE (transferencia_id, conta_id)` recusa a segunda liquidação.

### 2026-10-21 — especificação e roteiro
- Ideia original revisada: a tabela "O que mudou" da especificação lista cada
  problema encontrado e a correção.
