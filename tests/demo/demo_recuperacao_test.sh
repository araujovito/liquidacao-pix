#!/usr/bin/env bash
# Prova a promessa de recuperação da demonstração: se ela falhar DEPOIS de parar
# o worker e ANTES de religá-lo, o tratador de saída precisa religá-lo, para a
# demo não deixar o sistema pior do que o encontrou. Roda sem Docker, com um
# Compose falso (que registra o que lhe pedem) e uma API falsa.
set -euo pipefail

TMP="$(mktemp -d)"
SERVIDOR_PID=""
trap '[[ -n "$SERVIDOR_PID" ]] && kill "$SERVIDOR_PID" 2>/dev/null || true; rm -rf "$TMP"' EXIT

# API falsa com o comportamento que a demo observa: /saude, 202 novo, 202
# repetido com Idempotent-Replayed, 409 para a mesma chave com outro valor.
cat >"$TMP/api.py" <<'PY'
import json, sys, uuid
from http.server import BaseHTTPRequestHandler, HTTPServer
vistos = {}
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _json(self, codigo, corpo, extra=None):
        b = json.dumps(corpo, separators=(", ", ": ") if len(sys.argv) > 1 else (",", ":")).encode()
        self.send_response(codigo)
        self.send_header("Content-Type", "application/json")
        for k, v in (extra or {}).items(): self.send_header(k, v)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.path == "/saude": return self._json(200, {"status": "ok"})
        return self._json(200, {"status": "LIQUIDADA"})
    def do_POST(self):
        p = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        chave, valor = p["end_to_end_id"], p["valor_centavos"]
        if chave in vistos:
            id_, v = vistos[chave]
            if v != valor: return self._json(409, {"type": "x"})
            return self._json(202, {"id": id_, "status": "LIQUIDADA"}, {"Idempotent-Replayed": "true"})
        id_ = str(uuid.uuid4()); vistos[chave] = (id_, valor)
        self._json(202, {"id": id_, "status": "PENDENTE"})
s = HTTPServer(("127.0.0.1", 0), H)
print(s.server_address[1], flush=True)
s.serve_forever()
PY
python3 "$TMP/api.py" >"$TMP/porta" &
SERVIDOR_PID=$!
for _ in $(seq 50); do [[ -s "$TMP/porta" ]] && break; sleep 0.1; done
PORTA="$(cat "$TMP/porta")"

# Compose falso: tudo dá certo, registra stop/start do worker, e o banco
# responde 0 pendentes, o que faz a demo falhar logo depois de parar o worker.
cat >"$TMP/compose-falso" <<EOF2
#!/usr/bin/env bash
echo "\$*" >> "$TMP/chamadas"
case "\$*" in
  *psql*) echo 0 ;;
  *redis-cli*) echo 0 ;;
esac
exit 0
EOF2
chmod +x "$TMP/compose-falso"

set +e
saida="$(COMPOSE="$TMP/compose-falso" API="http://127.0.0.1:$PORTA" tests/demo/demo.sh 2>&1)"
codigo=$?
set -e

[[ "$codigo" -ne 0 ]] || { echo "a demo terminou com sucesso apesar de 0 pendentes" >&2; echo "$saida" >&2; exit 1; }
grep -q "esperava ao menos 15 pendentes" <<<"$saida" || { echo "a falha não foi a esperada:" >&2; echo "$saida" >&2; exit 1; }

paradas="$(grep -c '^stop worker' "$TMP/chamadas" || true)"
religadas="$(grep -c '^start worker' "$TMP/chamadas" || true)"
[[ "$paradas" -eq 1 ]] || { echo "esperava 1 parada do worker, houve $paradas" >&2; cat "$TMP/chamadas" >&2; exit 1; }
[[ "$religadas" -eq 1 ]] || { echo "a demo falhou depois de parar o worker e NÃO o religou ($religadas religações)" >&2; cat "$TMP/chamadas" >&2; exit 1; }
# E o religamento vem depois da parada, não antes.
[[ "$(grep -n '^stop worker' "$TMP/chamadas" | cut -d: -f1)" -lt "$(grep -n '^start worker' "$TMP/chamadas" | cut -d: -f1)" ]] \
  || { echo "o worker foi religado antes de ser parado" >&2; exit 1; }

# --- Segundo caso: formato de resposta inesperado -----------------------------
# Antes, um grep sem resultado derrubava a demo em silêncio (set -e + pipefail),
# sem nenhuma mensagem. Agora ela precisa dizer o que não entendeu.
kill "$SERVIDOR_PID" 2>/dev/null || true
: >"$TMP/porta"
python3 "$TMP/api.py" espacado >"$TMP/porta" &
SERVIDOR_PID=$!
for _ in $(seq 50); do [[ -s "$TMP/porta" ]] && break; sleep 0.1; done
PORTA="$(cat "$TMP/porta")"
set +e
saida="$(COMPOSE="$TMP/compose-falso" API="http://127.0.0.1:$PORTA" tests/demo/demo.sh 2>&1)"
codigo=$?
set -e
[[ "$codigo" -ne 0 ]] || { echo "a demo aceitou uma resposta que não conseguiu interpretar" >&2; exit 1; }
grep -q "não trouxe um id" <<<"$saida" || { echo "a demo morreu sem explicar o motivo (saída muda):" >&2; echo "$saida" >&2; exit 1; }
