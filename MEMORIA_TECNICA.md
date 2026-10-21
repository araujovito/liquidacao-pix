# Memória técnica

Documento para retomar o trabalho sem depender de memória de ninguém: onde o
projeto está, como rodar, por que cada decisão foi tomada e o que já foi
tentado. É atualizado no mesmo commit da mudança que descreve.

- Especificação: [`docs/especificacao.md`](docs/especificacao.md)
- Roteiro com o andamento: [`docs/roteiro.md`](docs/roteiro.md)
- Decisões: [`docs/adr/`](docs/adr/)

## Estado atual

Fase 0 em andamento: documentação inicial escrita, código ainda não começou.

## Ambiente

- Go 1.27 (o `go.mod` declara a versão mínima).
- Docker com Compose v2 para Postgres 17 e Redis 7.
- Nenhuma credencial real: todas as senhas do `docker-compose` são de
  desenvolvimento.

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

### 2026-10-21 — especificação e roteiro
- Ideia original revisada: a tabela "O que mudou" da especificação lista cada
  problema encontrado e a correção.
