#!/usr/bin/env bash
set -euo pipefail

# A demo precisa preservar a causa original quando a infraestrutura não sobe.
# Um Compose falso torna essa regressão rápida e independente de Docker.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cat >"$TMP/compose-falso" <<'EOF'
#!/usr/bin/env bash
echo "falha simulada do Compose" >&2
exit 42
EOF
chmod +x "$TMP/compose-falso"

if saida="$(COMPOSE="$TMP/compose-falso" tests/demo/demo.sh 2>&1)"; then
  echo "a demo aceitou uma falha ao subir a infraestrutura" >&2
  exit 1
fi
grep -q "falha simulada do Compose" <<<"$saida" || {
  echo "a demo ocultou a causa da falha:" >&2
  echo "$saida" >&2
  exit 1
}
grep -q "não foi possível subir o sistema" <<<"$saida" || {
  echo "a demo não contextualizou a falha:" >&2
  echo "$saida" >&2
  exit 1
}
