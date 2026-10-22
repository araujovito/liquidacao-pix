#!/usr/bin/env bash
# Roda um cenário k6 contra o sistema em contêineres e escreve um relatório
# em tests/carga/resultados/<cenario>-<data>.md.
#
# Uso: tests/carga/rodar.sh pico|sustentada   (ou make carga-pico / carga-sustentada)
# Variáveis opcionais: PICO, TAXA, DURACAO (repassadas ao k6), CONTAS (1000).
#
# Além do que o k6 mede (latência da API, erros, pedidos descartados), o
# script mede no banco o que o k6 não vê: vazão de aceite e de liquidação por
# segundo, atraso entre aceitar e liquidar, e a fila que sobrou ao fim da
# carga. Só entram as transferências da fase de medição (o aquecimento fica
# de fora).
set -euo pipefail
export LC_NUMERIC=C # printf com ponto decimal, que é o que o jq produz

cenario="${1:?uso: $0 pico|sustentada}"
[[ -f "tests/carga/$cenario.js" ]] || { echo "cenário desconhecido: $cenario" >&2; exit 2; }

K6_IMAGEM="grafana/k6:1.3.0"
CONTAS="${CONTAS:-1000}"
compose=(docker compose -f deployments/docker-compose.yml --profile sistema)
sql() { "${compose[@]}" exec -T postgres psql -U pix -d pix -XAtq -F $'\t' -c "$1"; }

carimbo="$(date -u +%Y%m%d%H%M)"
rodada="$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 2 || true)"
padrao="E12345678${carimbo}${rodada}m%"
saida="tests/carga/resultados"
relatorio="$saida/$cenario-$(date +%Y-%m-%d-%H%M).md"

echo "» subindo o sistema e semeando $CONTAS contas"
make -s sistema >/dev/null
go run ./cmd/semear -contas "$CONTAS" -saldo 100000000 >/dev/null

# Pendências de rodadas anteriores atrapalhariam a medição de vazão.
for _ in $(seq 120); do
  [[ "$(sql "SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'")" == 0 ]] && break
  sleep 1
done
pendentes_anteriores="$(sql "SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'")"
if (( pendentes_anteriores != 0 )); then
  echo "há $pendentes_anteriores transferências pendentes de rodadas anteriores; medição cancelada" >&2
  exit 1
fi

echo "» rodando k6 ($cenario, rodada $rodada)"
# Impede que uma falha ao iniciar o k6 reutilize silenciosamente o resumo da
# rodada anterior.
rm -f "$saida/ultimo.json"
set +e
docker run --rm --network host --user "$(id -u)" \
  -v "$PWD/tests/carga:/scripts:ro" -v "$PWD/$saida:/resultados" \
  -e API="http://127.0.0.1:8080" -e CARIMBO="$carimbo" -e RODADA="$rodada" -e CONTAS="$CONTAS" \
  -e PICO="${PICO:-}" -e TAXA="${TAXA:-}" -e DURACAO="${DURACAO:-}" \
  "$K6_IMAGEM" run --quiet "/scripts/$cenario.js"
k6_rc=$?
set -e
if [[ ! -s "$saida/ultimo.json" ]]; then
  echo "k6 não produziu resumo; execução abortada (código $k6_rc)" >&2
  (( k6_rc == 0 )) && k6_rc=1
  exit "$k6_rc"
fi

fila_no_fim="$(sql "SELECT count(*) FROM transferencias WHERE end_to_end_id LIKE '$padrao' AND status = 'PENDENTE'")"
echo "» carga encerrada; $fila_no_fim pendentes da medição. Esperando a fila esvaziar."
inicio_dreno=$SECONDS
until [[ "$(sql "SELECT count(*) FROM transferencias WHERE end_to_end_id LIKE '$padrao' AND status = 'PENDENTE'")" == 0 ]]; do
  (( SECONDS - inicio_dreno > 600 )) && { echo "fila não esvaziou em 10 min" >&2; exit 1; }
  sleep 1
done
dreno=$(( SECONDS - inicio_dreno ))

# --- números do banco --------------------------------------------------------
read -r total liquidadas rejeitadas <<<"$(sql "
  SELECT count(*), count(*) FILTER (WHERE status = 'LIQUIDADA'), count(*) FILTER (WHERE status = 'REJEITADA')
    FROM transferencias WHERE end_to_end_id LIKE '$padrao'")"
if (( total == 0 )); then
  echo "nenhuma transferência da medição chegou ao banco; veja os erros do k6 acima" >&2
  exit 1
fi

read -r atraso_p50 atraso_p95 atraso_p99 atraso_max <<<"$(sql "
  SELECT round(percentile_cont(0.50) WITHIN GROUP (ORDER BY d)::numeric, 1),
         round(percentile_cont(0.95) WITHIN GROUP (ORDER BY d)::numeric, 1),
         round(percentile_cont(0.99) WITHIN GROUP (ORDER BY d)::numeric, 1),
         round(max(d)::numeric, 1)
    FROM (SELECT extract(epoch FROM finalizada_em - criada_em) * 1000 AS d
            FROM transferencias WHERE end_to_end_id LIKE '$padrao') x")"

# Vazão por segundo de relógio, desprezando o primeiro e o último segundo
# (incompletos).
vazao() {
  sql "
    WITH s AS (SELECT date_trunc('second', $1) AS seg, count(*) AS n
                 FROM transferencias WHERE end_to_end_id LIKE '$padrao' GROUP BY 1),
         m AS (SELECT * FROM s WHERE seg > (SELECT min(seg) FROM s) AND seg < (SELECT max(seg) FROM s))
    SELECT coalesce(round(percentile_cont(0.5) WITHIN GROUP (ORDER BY n)), 0), coalesce(max(n), 0) FROM m"
}
read -r aceite_med aceite_max <<<"$(vazao criada_em)"
read -r liq_med liq_max <<<"$(vazao finalizada_em)"

set +e
divergencias="$(go run ./tests/carga/conciliar 2>&1)"
conciliacao_rc=$?
set -e

# --- números do k6 -----------------------------------------------------------
k6="$saida/ultimo.json"
m() { jq -r "$1" "$k6"; }
ms() { printf '%.1f' "$(m "$1")"; }
api_p50=$(ms '.metrics["http_req_duration{scenario:medicao}"].values.med')
api_p95=$(ms '.metrics["http_req_duration{scenario:medicao}"].values["p(95)"]')
api_p99=$(ms '.metrics["http_req_duration{scenario:medicao}"].values["p(99)"]')
api_max=$(ms '.metrics["http_req_duration{scenario:medicao}"].values.max')
pedidos=$(m '.metrics["http_reqs{scenario:medicao}"].values.count')
# No http_req_failed do k6, "passes" conta os pedidos que FALHARAM. Mostra a
# contagem, não só a taxa: 1 falha em 90 mil arredonda para "0 %".
falhas_n=$(m '.metrics["http_req_failed{scenario:medicao}"].values.passes')
falhas="$falhas_n ($(m '.metrics["http_req_failed{scenario:medicao}"].values.rate * 100 | . * 1000 | round / 1000') %)"
descartados=$(m '.metrics.dropped_iterations.values.count // 0')
erros_http=$(m '.metrics.erros_http.values.count // 0')
erros_transporte=$(m '.metrics.erros_transporte.values.count // 0')

# --- relatório ----------------------------------------------------------------
cpu="$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)"
mem="$(free -g | awk '/^Mem/ {print $2}')"
cat >"$relatorio" <<MD
# Carga: $cenario — $(date '+%Y-%m-%d %H:%M')

- Máquina: $cpu, $(nproc) threads, ${mem} GB de RAM; k6, API, relay, worker,
  Postgres e Redis no mesmo Docker ($(docker version -f '{{.Server.Version}}')).
- Massa: $CONTAS contas; valores de 1 a 5.000 centavos; pares aleatórios.
- Cenário: \`tests/carga/$cenario.js\` (PICO=${PICO:-padrão} TAXA=${TAXA:-padrão} DURACAO=${DURACAO:-padrão}).
- Worker: ${WORKERS:-32} consumidores; banco ${BANCO:-reaproveitado de rodadas anteriores}.
  Os números abaixo são só da fase de medição; o aquecimento fica de fora.
- Commit: \`$(git rev-parse --short HEAD)\`$(git diff --quiet || echo ' (com alterações não commitadas)')

## API (k6)

| Pedidos | Erros | HTTP ≥ 400 | Transporte/timeout | Descartados pelo k6 | p50 | p95 | p99 | máx. |
|---|---|---|---|---|---|---|---|---|
| $pedidos | $falhas | $erros_http | $erros_transporte | $descartados | $api_p50 ms | $api_p95 ms | $api_p99 ms | $api_max ms |

"Descartados" são pedidos que o k6 deixou de enviar por falta de usuários
virtuais livres: se for diferente de zero, a taxa planejada não foi atingida.

## Liquidação (banco)

| Transferências | Liquidadas | Rejeitadas por saldo | Fila ao fim da carga | Tempo para esvaziar |
|---|---|---|---|---|
| $total | $liquidadas | $rejeitadas | $fila_no_fim | ${dreno} s |

| Vazão por segundo | mediana | máxima |
|---|---|---|
| Aceite (API) | $aceite_med/s | $aceite_max/s |
| Liquidação (worker) | $liq_med/s | $liq_max/s |

| Atraso aceite → liquidação | p50 | p95 | p99 | máx. |
|---|---|---|---|---|
| | $atraso_p50 ms | $atraso_p95 ms | $atraso_p99 ms | $atraso_max ms |

Conciliação depois da carga: $divergencias
MD

echo "» relatório em $relatorio"
cat "$relatorio"

if (( conciliacao_rc != 0 )); then
  echo "conciliação falhou; rodada inválida" >&2
  exit "$conciliacao_rc"
fi
if (( k6_rc != 0 )); then
  echo "um ou mais critérios do k6 falharam; relatório preservado em $relatorio" >&2
  exit "$k6_rc"
fi
