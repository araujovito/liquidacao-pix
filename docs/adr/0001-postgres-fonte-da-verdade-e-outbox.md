# ADR 0001 — Postgres é a fonte da verdade; a fila só transporta

**Status:** aceita (2026-10-21)

## Contexto
A API precisa responder rápido (202) e liquidar depois. A forma mais curta seria
publicar o pedido na fila e responder. Mas se a fila cair entre o "aceito" e a
liquidação, o pedido aceito some, e o cliente já ouviu que deu certo.

## Decisão
A API grava a transferência (`PENDENTE`) e uma linha na `outbox` **na mesma
transação** do Postgres e só responde 202 depois do commit. Um relay separado
copia a outbox para o Redis Stream.

## Consequências
- Redis fora do ar não perde pedido: a outbox acumula e o relay esvazia quando
  o Redis volta.
- O relay pode publicar a mesma mensagem duas vezes (cai entre o `XADD` e a
  marcação). Por isso o worker é idempotente (ADR 0002).
- A latência da API inclui um commit no Postgres. É o preço da garantia; o teste
  de carga mede quanto custa.

## Alternativas descartadas
- **Publicar direto na fila:** perde pedido aceito se a fila cair.
- **Redis Pub/Sub:** não guarda mensagem; consumidor fora do ar = mensagem perdida.
- **Gravar no banco e na fila sem outbox (dual write):** uma das escritas pode
  falhar depois da outra, sem como desfazer.
