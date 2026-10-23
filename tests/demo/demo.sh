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

CAB="$(mktemp)"                    # cabeçalhos da última resposta
LOG="$(mktemp)"                    # saída do último comando de infraestrutura
WORKER_PARADO=false
ADULTERADA=""                      # "conta_id seq valor_original" se a demo alterou uma entrada
limpar() {
  local codigo=$?
  trap - EXIT
  # A entrada adulterada na etapa 8 é só para a demonstração: se ela falhar no
  # meio, devolve o valor original.
  if [[ -n "$ADULTERADA" ]]; then
    read -r conta seq valor <<<"$ADULTERADA"
    if ! psql "ALTER TABLE auditoria DISABLE TRIGGER USER; UPDATE auditoria SET valor_centavos = $valor WHERE conta_id = $conta AND seq = $seq; ALTER TABLE auditoria ENABLE TRIGGER USER;" >/dev/null; then
      printf '  \033[31m✗ não foi possível restaurar a entrada adulterada (conta %s, seq %s)\033[0m\n' "$conta" "$seq" >&2
      codigo=1
    fi
  fi
  # Se a demonstração falhar entre stop e start, não deixa o sistema que já
  # existia num estado pior do que encontrou.
  if [[ "$WORKER_PARADO" == true ]]; then
    $COMPOSE start worker >/dev/null 2>&1 || true
  fi
  rm -f "$CAB" "$CAB.1" "$LOG"
  exit "$codigo"
}
trap limpar EXIT

pedir() { # pedir <n> <de> <para> <valor> -> imprime o corpo; os cabeçalhos ficam em $CAB
  curl -s -D "$CAB" -X POST "$API/transferencias" -H 'Content-Type: application/json' \
    -d "{\"end_to_end_id\":\"$(e2e "$1")\",\"chave_pagador\":\"cliente-$(printf '%04d' "$2")@pix\",\"chave_recebedor\":\"cliente-$(printf '%04d' "$3")@pix\",\"valor_centavos\":$4}"
}
# O || true importa: com set -e e pipefail, um grep sem resultado derrubaria o
# script em silêncio, sem nenhuma mensagem. Quem chama confere se veio vazio.
campo() { { grep -o "\"$1\":\"[^\"]*\"" || true; } | head -1 | cut -d'"' -f4; }
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
if ! $COMPOSE up -d --build --wait >"$LOG" 2>&1; then
  cat "$LOG" >&2
  falha "não foi possível subir o sistema"
fi
ok "sistema no ar"
for _ in $(seq 30); do curl -sf "$API/saude" >/dev/null && break; sleep 1; done
curl -sf "$API/saude" >/dev/null || falha "a API não respondeu em $API/saude"

passo "2. Semeando 10 contas com R\$ 10.000,00 cada (o dinheiro entra pela conta técnica de liquidez, como lançamento)"
if ! $COMPOSE run --rm --no-deps semear -contas 10 -saldo 1000000 >"$LOG" 2>&1; then
  cat "$LOG" >&2
  falha "não foi possível semear as contas"
fi
ok "cliente-0001@pix … cliente-0010@pix criadas"

passo "3. Uma transferência: a API responde 202 na hora e o worker liquida depois"
corpo="$(pedir 1 1 2 2500)"
[[ "$(codigo "$CAB")" == 202 ]] || falha "esperava 202, veio $(codigo "$CAB")"
id1="$(echo "$corpo" | campo id)"
[[ -n "$id1" ]] || falha "a resposta do 202 não trouxe um id: $corpo"
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
if ! $COMPOSE stop worker >"$LOG" 2>&1; then
  cat "$LOG" >&2
  falha "não foi possível parar o worker"
fi
WORKER_PARADO=true
ids=()
for n in $(seq 11 25); do
  corpo="$(pedir "$n" $(( (n % 9) + 1 )) $(( (n % 9) + 2 )) 100)"
  [[ "$(codigo "$CAB")" == 202 ]] || falha "a API deixou de aceitar com o worker parado"
  id="$(echo "$corpo" | campo id)"
  [[ -n "$id" ]] || falha "a resposta do 202 não trouxe um id: $corpo"
  ids+=("$id")
done
sleep 2
pendentes="$(psql "SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'")"
fila="$($COMPOSE exec -T redis redis-cli XLEN transferencias | tr -d '\r')"
[[ "$pendentes" -ge 15 ]] || falha "esperava ao menos 15 pendentes, há $pendentes"
ok "a API segue aceitando (202): $pendentes transferências PENDENTE, $fila mensagens esperando no stream"

passo "6. Recuperação: religo o worker; ele consome o que ficou esperando"
if ! $COMPOSE start worker >"$LOG" 2>&1; then
  cat "$LOG" >&2
  falha "não foi possível religar o worker"
fi
WORKER_PARADO=false
for id in "${ids[@]}"; do esperar_liquidada "$id" 60; done
ok "as ${#ids[@]} transferências aceitas durante a queda foram todas liquidadas"
[[ "$(psql "SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'")" == 0 ]] || falha "ainda há pendentes"

passo "7. Conciliação do livro-razão: soma global zero, saldos = soma dos lançamentos, cada transferência com débito e crédito"
if $COMPOSE run --rm --no-deps --entrypoint /app/conciliador conciliador -uma-vez 2>&1 | grep -q 'conciliação fechada'; then
  ok "conciliação fechada, sem divergências"
else
  falha "a conciliação acusou divergência"
fi

AUDITAR="$COMPOSE --profile ferramentas run --rm --no-deps -T auditar"
TITULAR="$COMPOSE --profile ferramentas run --rm --no-deps -T titular"

passo "8. Auditoria: cada decisão é assinada numa cadeia de HMAC; a chave não está no banco"
if ! saida="$($AUDITAR 2>&1)"; then
  echo "$saida" >&2
  falha "a cadeia não verifica. Se o banco é anterior à fase 7 (decisões sem trilha), recrie os volumes: make infra-parar && docker volume rm liquidacao-pix_postgres"
fi
ok "$(grep -o 'íntegra:.*' <<<"$saida")"
conta="$(psql "SELECT id FROM contas WHERE chave_pix = 'cliente-0001@pix'")"
valor="$(psql "SELECT valor_centavos FROM auditoria WHERE conta_id = $conta AND seq = 1")"
[[ -n "$conta" && -n "$valor" ]] || falha "a conta cliente-0001@pix não tem entrada de auditoria"
ADULTERADA="$conta 1 $valor"
psql "ALTER TABLE auditoria DISABLE TRIGGER USER; UPDATE auditoria SET valor_centavos = valor_centavos + 1 WHERE conta_id = $conta AND seq = 1; ALTER TABLE auditoria ENABLE TRIGGER USER;" >/dev/null
if saida="$($AUDITAR 2>&1)"; then
  falha "alterei uma entrada com os gatilhos desligados e o verificador não percebeu"
fi
grep -q "conta $conta seq 1: mac:" <<<"$saida" || { echo "$saida" >&2; falha "o verificador não apontou a entrada alterada (conta $conta, seq 1)"; }
ok "alterei uma entrada direto no banco, com os gatilhos desligados: o verificador apontou conta $conta, seq 1 (mac)"
psql "ALTER TABLE auditoria DISABLE TRIGGER USER; UPDATE auditoria SET valor_centavos = $valor WHERE conta_id = $conta AND seq = 1; ALTER TABLE auditoria ENABLE TRIGGER USER;" >/dev/null
ADULTERADA=""
$AUDITAR >"$LOG" 2>&1 || { cat "$LOG" >&2; falha "a cadeia não voltou a verificar depois de restaurar a entrada"; }
ok "valor restaurado; a cadeia volta a verificar"

passo "9. CPF cifrado, e apagamento por descarte de chave (a auditoria não é tocada)"
CHAVE="demo-$STAMP$RUN@pix"
psql "INSERT INTO contas (chave_pix, titular) VALUES ('$CHAVE', 'Titular da demonstração')" >/dev/null
echo "529.982.247-25" | $TITULAR cadastrar-cpf -chave "$CHAVE" >"$LOG" 2>&1 || { cat "$LOG" >&2; falha "não foi possível cadastrar o CPF"; }
[[ "$(psql "SELECT count(*) FROM contas WHERE cpf_cifrado IS NOT NULL AND chave_pix = '$CHAVE' AND position('52998224725' in encode(cpf_cifrado, 'escape')) = 0")" == 1 ]] \
  || falha "o CPF cifrado não está no banco, ou aparece em claro"
mascarado="$($TITULAR mostrar-cpf -chave "$CHAVE" 2>&1)" || falha "não foi possível ler o CPF: $mascarado"
# O Compose escreve o progresso no mesmo fluxo; fica só a linha mascarada.
mascarado="$(grep -o '\*\*\*\.[0-9]\{3\}\.[0-9]\{3\}-\*\*' <<<"$mascarado" || true)"
[[ "$mascarado" == '***.982.247-**' ]] || falha "a leitura mascarada veio errada: '$mascarado'"
ok "CPF cadastrado cifrado e lido pela chave da conta (só se vê $mascarado)"
$AUDITAR >"$LOG" 2>&1 || { cat "$LOG" >&2; falha "a cadeia deixou de verificar"; }
antes="$(psql "SELECT md5(string_agg(a::text, '|' ORDER BY a.conta_id, a.seq)) FROM auditoria a")"
$TITULAR apagar-cpf -chave "$CHAVE" >"$LOG" 2>&1 || { cat "$LOG" >&2; falha "não foi possível apagar o CPF"; }
if $TITULAR mostrar-cpf -chave "$CHAVE" >"$LOG" 2>&1; then falha "o CPF ainda é legível depois do apagamento"; fi
grep -q "apagado" "$LOG" || { cat "$LOG" >&2; falha "esperava a mensagem de CPF apagado"; }
[[ "$(psql "SELECT count(*) FROM chaves_cpf WHERE conta_id = (SELECT id FROM contas WHERE chave_pix = '$CHAVE')")" == 0 ]] || falha "a chave de dados ainda existe"
[[ "$(psql "SELECT count(*) FROM contas WHERE chave_pix = '$CHAVE' AND cpf_cifrado IS NOT NULL")" == 1 ]] || falha "o texto cifrado deveria continuar no banco"
[[ "$(psql "SELECT md5(string_agg(a::text, '|' ORDER BY a.conta_id, a.seq)) FROM auditoria a")" == "$antes" ]] || falha "o apagamento alterou a auditoria"
$AUDITAR >"$LOG" 2>&1 || { cat "$LOG" >&2; falha "a cadeia deixou de verificar depois do apagamento"; }
ok "chave descartada: o CPF não pode mais ser lido; a trilha financeira continua idêntica e verificável"

printf '\n\033[1;32mDemonstração concluída: nada se perdeu, nada foi liquidado duas vezes, o dinheiro foi conservado, a trilha é verificável.\033[0m\n'
printf 'Para ver métricas e traces: make observabilidade   (Grafana :3001, Jaeger :16686)\n'
