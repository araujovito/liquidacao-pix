# Memória técnica

Documento para retomar o trabalho sem depender de memória de ninguém: onde o
projeto está, como rodar, por que cada decisão foi tomada e o que já foi
tentado. É atualizado no mesmo commit da mudança que descreve.

- Especificação: [`docs/especificacao.md`](docs/especificacao.md)
- Roteiro com o andamento: [`docs/roteiro.md`](docs/roteiro.md)
- Decisões: [`docs/adr/`](docs/adr/)

## Estado atual

Fases 0, 1 e 2 concluídas: livro-razão, liquidação idempotente, conciliação,
API de ingestão com outbox. As transferências aceitas ficam `PENDENTE` e
acumulam na outbox: **ainda não há relay nem worker** (fase 3).

## Ambiente

- Go 1.27 (o `go.mod` declara a versão mínima).
- Docker com Compose v2 para Postgres 17 e Redis 7.
- Nenhuma credencial real: todas as senhas do `docker-compose` são de
  desenvolvimento. O código não tem URL padrão de banco; o `Makefile` exporta
  `DATABASE_URL` de desenvolvimento.

## Como rodar

```sh
make infra          # Postgres e Redis via docker compose
make migrar         # aplica db/migrations
make semear         # cliente-0001@pix … cliente-0100@pix com R$ 10.000 cada
make api            # API em :8080 (PORTA muda)
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

## Histórico

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
