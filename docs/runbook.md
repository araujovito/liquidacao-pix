# Runbook

O que fazer quando algo dá errado em produção. Cada seção diz o sintoma, como
confirmar e o que fazer. Os comandos supõem o `docker compose` deste
repositório; em outro ambiente, troque pelo equivalente.

```sh
C="docker compose -f deployments/docker-compose.yml --profile sistema"
psql() { $C exec -T postgres psql -U pix -c "$1"; }
rcli() { $C exec -T redis redis-cli "$@"; }
```

## Princípio

O Postgres é a fonte da verdade (ADR 0001). Nenhuma ação deste runbook
apaga ou altera lançamentos; os lançamentos nem aceitam isso (gatilho). O
Redis pode ser esvaziado sem perda: a varredura de órfãs do relay republica o
que estiver pendente (ADR 0003).

## Outbox crescendo

**Sintoma:** transferências ficam `PENDENTE` e a outbox não esvazia.

```sh
psql "SELECT count(*) FROM outbox WHERE publicada_em IS NULL"
$C logs relay --since 5m | grep WARN
```

- Log `publicação falhou` → Redis inacessível. Resolver o Redis; o relay volta
  sozinho (espera até 5 s entre tentativas). Nada se perde: a outbox guarda.
- Sem erro e o número só cresce → vazão do relay menor que a entrada. Subir
  outra instância do relay (`SKIP LOCKED` divide os lotes) ou aumentar
  `RELAY_LOTE`.

## Stream crescendo (atraso de liquidação)

**Sintoma:** outbox vazia, mas transferências demoram a liquidar.

```sh
rcli XLEN transferencias
rcli XINFO GROUPS transferencias      # campo "lag" e "pending"
$C logs worker --since 5m | grep -E 'WARN|ERROR'
```

- Log `liquidação falhou` em massa → Postgres com problema; as mensagens ficam
  pendentes e voltam com espera crescente quando ele se recupera.
- Log `grupo de consumo sumiu; recriando` → o Redis perdeu o stream (reinício
  sem dados ou apagado). Os workers recriam o grupo sozinhos; a varredura de
  órfãs republica em até ~30 s o que se perdeu.
- Sem erro → falta vazão. Subir mais workers
  (`$C up -d --scale worker=3`) ou aumentar `WORKERS`.

## Mensagens mortas

**Sintoma:** `transferencias:mortas` não está vazio; log
`mensagens movidas para mortas`.

```sh
rcli XRANGE transferencias:mortas - +
```

Cada entrada traz `transferencia_id`, `entregas` e `ultimo_erro`.

1. Corrigir a causa indicada em `ultimo_erro`.
2. Ver o status da transferência:
   `psql "SELECT status FROM transferencias WHERE id = '<id>'"`.
   - Se já está final, a morte foi depois de liquidada (ex.: falha ao
     confirmar); só apagar a entrada morta.
   - Se está `PENDENTE`, devolvê-la à outbox:
     `psql "UPDATE outbox SET publicada_em = NULL WHERE transferencia_id = '<id>'"`.
     Liquidar de novo é seguro: a liquidação é idempotente (ADR 0002).
3. Apagar a entrada morta: `rcli XDEL transferencias:mortas <id-da-entrada>`.

## Transferência pendente que não sai

A varredura de órfãs republica até 5 vezes. Depois disso, a transferência
precisa de operador:

```sh
psql "SELECT t.id, t.criada_em, o.republicacoes, o.publicada_em
        FROM transferencias t JOIN outbox o ON o.transferencia_id = t.id
       WHERE t.status = 'PENDENTE' AND t.criada_em < now() - interval '5 minutes'"
```

Investigar pelo log do worker (`transferencia=<id>`), corrigir a causa e
zerar `republicacoes` / `publicada_em` da linha da outbox.

## Conciliação divergente

**Sintoma:** a conciliação (`repository.Conciliar`, também rodada no fim do
teste de caos) devolve divergência.

Isto **não deveria acontecer**: as restrições do banco (`UNIQUE` por
transferência e conta, `CHECK` de saldo, lançamentos imutáveis) impedem os
casos conhecidos. Uma divergência indica defeito de código ou alteração manual
no banco.

1. Parar os workers (`$C stop worker`): a fila segura o resto.
2. Guardar a saída da conciliação e um `pg_dump` das tabelas `contas`,
   `transferencias` e `lancamentos`.
3. `SALDO_DIVERGENTE` → a projeção de saldo saiu da soma dos lançamentos. Os
   lançamentos são a verdade; a projeção pode ser recalculada a partir deles.
4. `TRANSFERENCIA_INCONSISTENTE` ou `SOMA_GLOBAL` → não corrigir à mão:
   escalar. Correção de livro-razão é lançamento novo, nunca alteração.

## Consumidores fantasmas no grupo

Cada reinício do worker com outro hostname deixa um consumidor sem uso no
grupo. É inofensivo (as pendentes dele são reivindicadas por outros), mas
polui o `XINFO CONSUMERS`. Limpar os que têm `pending` 0 e estão parados há
muito tempo:

```sh
rcli XINFO CONSUMERS transferencias liquidadores
rcli XGROUP DELCONSUMER transferencias liquidadores <nome>
```
