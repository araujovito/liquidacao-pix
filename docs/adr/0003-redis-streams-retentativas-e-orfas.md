# ADR 0003 — Redis Streams: retentativas, mensagens mortas e órfãs

**Status:** aceita (2026-10-21)

## Contexto
Precisamos de uma fila que (a) guarde a mensagem até ela ser processada,
(b) devolva mensagens de workers que morreram, (c) não fique tentando para
sempre algo que sempre falha, e (d) não deixe transferência pendente para
sempre se o próprio Redis perder dados.

## Decisão
- **Streams com grupo de consumo.** `XREADGROUP` entrega; a mensagem fica na
  lista de pendentes até `XACK`.
- **Confirmar = `XACK` + `XDEL` numa transação (MULTI).** O stream não cresce
  e dispensa `MAXLEN`, que poderia apagar entradas ainda não confirmadas.
- **Retentativa com espera crescente.** Cada worker, a cada meia espera base,
  olha a lista de pendentes (`XPENDING`). Uma mensagem com *n* entregas só é
  reivindicada (`XCLAIM` com `min-idle`) depois de parada por base·2^(n-1).
  O `min-idle` impede que dois workers reivindiquem a mesma ao mesmo tempo.
- **Mensagens mortas.** Ao atingir o máximo de entregas, a mensagem vai para
  `transferencias:mortas` com o último erro, e sai do stream principal.
- **Varredura de órfãs no relay.** Transferência `PENDENTE` publicada há mais
  de 30 s volta para a outbox (até 5 vezes). Cobre perda de dados no Redis.

## Consequências
- O Redis vira descartável: pode perder o stream inteiro e o sistema converge
  (teste `TestMensagemPerdidaNoRedisERecuperada`).
- Uma mensagem morta continua `PENDENTE` no banco; a varredura ainda a
  republica algumas vezes antes de desistir. Depois disso é caso de operador
  (runbook, fase 4).
- Erros permanentes (transferência inexistente) passam pelas mesmas
  retentativas que os temporários antes de morrer. É mais lento, mas evita
  classificar errado um erro temporário como permanente.

## Alternativas descartadas
- **RabbitMQ:** resolve (a)–(c) nativamente, mas é mais um sistema para operar;
  o Redis já estava na pilha e Streams cobre o necessário.
- **`XAUTOCLAIM`:** reivindica por tempo parado fixo, sem saber o número de
  entregas; a espera crescente precisa desse número, que vem do `XPENDING`.
- **`MAXLEN` no `XADD`:** pode apagar mensagens pendentes.
