# ADR 0004 — O contexto do rastro viaja como dado, pela outbox e pelo stream

**Status:** aceita (2026-10-22)

## Contexto
Um pedido atravessa três processos e duas filas (API → outbox → relay → Redis →
worker) sem nenhuma chamada direta entre os elos. A propagação usual do
OpenTelemetry, por cabeçalho HTTP, não tem onde acontecer: o relay e o worker
nunca são chamados pela API.

## Decisão
O contexto (`traceparent`, formato W3C) é gravado **como dado**: a API o grava
na linha da outbox (migração 0007); o relay o lê na mesma consulta que pega o
lote, abre o seu span e grava o próprio contexto na mensagem do stream; o worker
continua dali. Resultado: uma árvore por pedido, com o tempo de espera de cada
fila visível entre os spans.

- Rastreamento nunca é caminho crítico: `traceparent` inválido ou ausente abre um
  rastro novo e a liquidação segue; falha ao ligar o exportador desliga o
  rastreamento e o serviço sobe.
- Sem `OTEL_EXPORTER_OTLP_ENDPOINT` nada é ligado e a mensagem do stream fica
  idêntica à de antes.
- Só atributos opacos (id da transferência, status, nº da entrega). Chave PIX,
  CPF, valor e `end_to_end_id` nunca entram; um teste percorre todos os spans.

## Consequências
- A coluna é nula sem *default*: só metadado no Postgres 11+, sem reescrever a
  tabela quente.
- O span do relay é um por pedido, mas o envio é um só (pipeline do Redis), então
  cada span cobre o lote. O intervalo entre spans é aproximado e pode aparecer
  negativo: o span do relay só fecha quando o `XADD` retorna, e a mensagem já está
  visível antes. O que é garantido é a ordem causal.
- Medido: sem custo mensurável a 1.000 pedidos/s com 100% de amostragem.

## Alternativas descartadas
- **Um *link* entre spans em vez de pai e filho:** mostraria o rastro do pedido
  separado do rastro do processamento, o que dificulta a leitura de uma única
  árvore. Fica como ponto a rever.
- **Buscar o `traceparent` numa tabela à parte no worker:** mais uma consulta por
  liquidação, sem ganho.
- **Propagar só até o relay:** deixaria o worker, onde está o trabalho caro, fora
  do rastro.
