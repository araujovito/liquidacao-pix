# Memória técnica

Documento para retomar o trabalho sem depender de memória de ninguém: onde o
projeto está, como rodar, por que cada decisão foi tomada e o que já foi
tentado. É atualizado no mesmo commit da mudança que descreve.

- Especificação: [`docs/especificacao.md`](docs/especificacao.md)
- Roteiro com o andamento: [`docs/roteiro.md`](docs/roteiro.md)
- Decisões: [`docs/adr/`](docs/adr/)

## Estado atual

Fase 1 concluída (livro-razão, liquidação idempotente, conciliação, teste de
concorrência). Próximo passo: fase 2, API de ingestão com outbox.

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
make teste          # unidade + integração (Testcontainers sobe contêineres próprios)
make teste-unidade  # só o que roda sem Docker (-short)
make lint           # gofmt, go vet, golangci-lint
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

## Histórico

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
