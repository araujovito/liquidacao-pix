#!/usr/bin/env bash
# Demonstração de ponta a ponta do motor de liquidação.
#
# Roda tudo em contêineres; precisa só de docker, docker compose e curl.
#   make demo
#
# Roteiro: sobe o sistema, semeia contas, transfere, mostra a idempotência,
# derruba o worker no MEIO da carga e mostra que nada se perde, e termina com a
# conciliação do livro-razão. Sai com código diferente de zero se qualquer
# afirmação da demonstração não se confirmar.
set -euo pipefail

COMPOSE="${COMPOSE:-docker compose -f deployments/docker-compose.yml --profile sistema}"
API="${API:-http://localhost:8080}"
RUN="$(date +%N | cut -c1-3)"            # evita colisão de end_to_end_id entre execuções
STAMP="$(date -u +%Y%m%d%H%M)"

passo() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
ok()    { printf '  \033[32m✓\033[0m %s\n' "$*"; }
falha() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
psql()  { $COMPOSE exec -T postgres psql -U pix -d pix -XAtq -c "$1"; }

e2e() { printf 'E99999999%s%03d%08d' "$STAMP" "$RUN" "$1"; }   # 1 + 8 + 12 + 11 = 32

CAB="$(mktemp)"; trap 'rm -f "$CAB" "$CAB.1"' EXIT   # cabeçalhos da última resposta

pedir() { # pedir <n> <de> <para> <valor> -> imprime o corpo; os cabeçalhos ficam em $CAB
  curl -s -D "$CAB" -X POST "$API/transferencias" -H 'Content-Type: application/json' \
    -d "{\"end_to_end_id\":\"$(e2e "$1")\",\"chave_pagador\":\"cliente-$(printf '%04d' "$2")@pix\",\"chave_recebedor\":\"cliente-$(printf '%04d' "$3")@pix\",\"valor_centavos\":$4}"
}
campo() { grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4; }
codigo() { head -1 "$1" | cut -d' ' -f2; }

status_de() { curl -s "$API/transferencias/$1" | campo status; }
esperar_liquidada() { # esperar_liquidada <id> [segundos]
  local id="$1" limite="${2:-30}" s=""
  for _ in $(seq "$limite"); do
    s="$(status_de "$id")"; [[ "$s" == LIQUIDADA ]] && return 0; sleep 1
  done
  falha "transferência $id ficou $s depois de ${limite}s"
}

passo "1. Subindo o sistema (Postgres, Redis, migrações, API, relay, worker, conciliador)"
$COMPOSE up -d --build --wait >/dev/null 2>&1
ok "sistema no ar"
for _ in $(seq 30); do curl -sf "$API/saude" >/dev/null && break; sleep 1; done
curl -sf "$API/saude" >/dev/null || falha "a API não respondeu em $API/saude"

passo "2. Semeando 10 contas com R\$ 10.000,00 cada (o dinheiro entra pela conta técnica de liquidez, como lançamento)"
$COMPOSE run --rm --no-deps --entrypoint /app/semear migrar -contas 10 -saldo 1000000 >/dev/null 2>&1
ok "cliente-0001@pix … cliente-0010@pix criadas"

passo "3. Uma transferência: a API responde 202 na hora e o worker liquida depois"
corpo="$(pedir 1 1 2 2500)"
[[ "$(codigo "$CAB")" == 202 ]] || falha "esperava 202, veio $(codigo "$CAB")"
id1="$(echo "$corpo" | campo id)"
ok "202 Accepted; status imediato: $(echo "$corpo" | campo status)"
esperar_liquidada "$id1"
ok "status depois de alguns ms: LIQUIDADA"

passo "4. Idempotência: reenviar o MESMO pedido devolve a mesma transferência, sem mover dinheiro de novo"
corpo2="$(pedir 1 1 2 2500)"
[[ "$(echo "$corpo2" | campo id)" == "$id1" ]] || falha "o reenvio criou outra transferência"
grep -qi '^idempotent-replayed: true' "$CAB" || falha "faltou o cabeçalho Idempotent-Replayed"
ok "mesmo id ($id1), cabeçalho Idempotent-Replayed: true"
pedir 1 1 2 9999 >/dev/null
[[ "$(codigo "$CAB")" == 409 ]] || falha "esperava 409 para mesma chave com valor diferente, veio $(codigo "$CAB")"
ok "mesma chave com valor diferente: 409 Conflict"

passo "5. Falha controlada: derrubo o worker e continuo recebendo pedidos"
$COMPOSE stop worker >/dev/null 2>&1
ids=()
for n in $(seq 11 25); do
  corpo="$(pedir "$n" $(( (n % 9) + 1 )) $(( (n % 9) + 2 )) 100)"
  [[ "$(codigo "$CAB")" == 202 ]] || falha "a API deixou de aceitar com o worker parado"
  ids+=("$(echo "$corpo" | campo id)")
done
sleep 2
pendentes="$(psql "SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'")"
fila="$($COMPOSE exec -T redis redis-cli XLEN transferencias | tr -d '\r')"
[[ "$pendentes" -ge 15 ]] || falha "esperava ao menos 15 pendentes, há $pendentes"
ok "a API segue aceitando (202): $pendentes transferências PENDENTE, $fila mensagens esperando no stream"

passo "6. Recuperação: religo o worker; ele consome o que ficou esperando"
$COMPOSE start worker >/dev/null 2>&1
for id in "${ids[@]}"; do esperar_liquidada "$id" 60; done
ok "as ${#ids[@]} transferências aceitas durante a queda foram todas liquidadas"
[[ "$(psql "SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'")" == 0 ]] || falha "ainda há pendentes"

passo "7. Conciliação do livro-razão: soma global zero, saldos = soma dos lançamentos, cada transferência com débito e crédito"
if $COMPOSE run --rm --no-deps --entrypoint /app/conciliador conciliador -uma-vez 2>&1 | grep -q 'conciliação fechada'; then
  ok "conciliação fechada, sem divergências"
else
  falha "a conciliação acusou divergência"
fi

printf '\n\033[1;32mDemonstração concluída: nada se perdeu, nada foi liquidado duas vezes, o dinheiro foi conservado.\033[0m\n'
printf 'Para ver métricas e traces: make observabilidade   (Grafana :3001, Jaeger :16686)\n'
