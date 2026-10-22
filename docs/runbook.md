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

## Auditoria acusa adulteração

**Sintoma:** `make auditar` (ou `docker compose run --rm auditar`) sai com
código 1 e imprime achados `conta N seq M: <tipo>: ...`. Código 2 significa que
não foi possível verificar (banco, chave ou arquivo), não que a cadeia esteja
quebrada.

Cada achado aponta uma conta e uma posição. Os tipos:

| Tipo | O que diz |
|---|---|
| `mac` | o conteúdo da entrada não bate com a assinatura: foi alterado, ou a assinatura foi forjada sem a chave |
| `elo` | `mac_anterior` não é o MAC da entrada anterior: entrada removida do meio, inserida ou reordenada |
| `seq` | buraco ou repetição na posição |
| `cabeca` | `contas.auditoria_seq/mac` não aponta para a última entrada |
| `sem-entrada` | uma transferência finalizada não tem entrada nesta cadeia |
| `transferencia` | a entrada diz outra coisa que a transferência que registra |
| `chave` | assinada por uma chave que este ambiente não tem: confira `AUDITORIA_CHAVES` |
| `ancora` | com `-conferir-ancora`: a entrada ancorada sumiu ou foi trocada |

1. **Não corrigir à mão e não "refazer" a cadeia.** Refazer exige a chave e
   apaga a evidência.
2. Antes de concluir adulteração, descarte erro de ambiente: o `chave` quase
   sempre é a variável errada, não um ataque.
3. Guardar a saída completa do verificador e um `pg_dump` das tabelas
   `auditoria`, `contas`, `transferencias` e `lancamentos`.
4. `sem-entrada` em **todas** as transferências antigas e em nenhuma recente
   indica um banco criado antes da fase 7 (a trilha começa na migração 0008),
   não adulteração. Recrie o banco de desenvolvimento.
5. Escalar. A cadeia mostra **onde** e **o que**; **quem** exige os registros
   de acesso ao banco, que este projeto não tem.

**Âncora.** Para detectar o apagamento do rabo de uma cadeia, guarde as cabeças
fora do banco: `make auditar ARGS=-ancora`, ou
`go run ./cmd/auditar -ancora > ancora.txt`, e depois
`go run ./cmd/auditar -conferir-ancora ancora.txt`. O arquivo não pode morar
no mesmo lugar que o banco.

## Apagamento de CPF (LGPD art. 18)

```sh
go run ./cmd/titular apagar-cpf -chave cliente-0001@pix
```

Descarta a chave de dados da conta; o texto cifrado fica e deixa de poder ser
lido. É idempotente e irreversível. A trilha e os lançamentos não mudam. Se
houver cópia do banco com a tabela `chaves_cpf` (backup, réplica), o
apagamento só vale quando essa cópia também expirar ou for tratada.

## Consumidores fantasmas no grupo

Cada reinício do worker com outro hostname deixa um consumidor sem uso no
grupo. É inofensivo (as pendentes dele são reivindicadas por outros), mas
polui o `XINFO CONSUMERS`. Limpar os que têm `pending` 0 e estão parados há
muito tempo:

```sh
rcli XINFO CONSUMERS transferencias liquidadores
rcli XGROUP DELCONSUMER transferencias liquidadores <nome>
```
