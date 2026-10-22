// Partes comuns dos cenários de carga.
//
// Cada pedido leva um end_to_end_id único com a forma
//   E 12345678 <CARIMBO 12> <RODADA 2> <fase 1> <sequência 8>
// A fase é "a" no aquecimento e "m" na medição. Assim o script rodar.sh acha,
// no banco, exatamente as transferências da medição para calcular o atraso
// até a liquidação, que o k6 não consegue ver.
import http from 'k6/http';
import exec from 'k6/execution';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const API = __ENV.API || 'http://localhost:8080';
const CONTAS = parseInt(__ENV.CONTAS || '1000', 10);
const PREFIXO = `E12345678${__ENV.CARIMBO}${__ENV.RODADA}`;

if (!/^\d{12}$/.test(__ENV.CARIMBO || '') || !/^[0-9a-z]{2}$/.test(__ENV.RODADA || '')) {
  throw new Error('defina CARIMBO (AAAAMMDDHHMM) e RODADA (2 caracteres); use tests/carga/rodar.sh');
}

const parametros = { headers: { 'Content-Type': 'application/json' }, timeout: '10s' };
const errosHTTP = new Counter('erros_http');
const errosTransporte = new Counter('erros_transporte');

function chave(n) {
  return `cliente-${String(n).padStart(4, '0')}@pix`;
}

export function transferir() {
  const fase = exec.scenario.name === 'aquecimento' ? 'a' : 'm';
  const seq = String(exec.scenario.iterationInTest).padStart(8, '0');
  const de = 1 + Math.floor(Math.random() * CONTAS);
  const para = 1 + ((de + Math.floor(Math.random() * (CONTAS - 1))) % CONTAS);

  const res = http.post(`${API}/transferencias`, JSON.stringify({
    end_to_end_id: `${PREFIXO}${fase}${seq}`,
    chave_pagador: chave(de),
    chave_recebedor: chave(para),
    valor_centavos: 1 + Math.floor(Math.random() * 5000),
  }), parametros);

  if (res.status >= 400) {
    errosHTTP.add(1, { status: String(res.status) });
  } else if (res.status === 0) {
    errosTransporte.add(1, { codigo: String(res.error_code || 0) });
  }

  check(res, { 'status 202': (r) => r.status === 202 });
}

// Limites "frouxos" de propósito: servem para o k6 calcular as métricas só
// da fase de medição (sub-métricas por cenário), não para reprovar a rodada.
export const limites = {
  'http_req_duration{scenario:medicao}': ['max>=0'],
  'http_req_failed{scenario:medicao}': ['rate>=0'],
  'http_reqs{scenario:medicao}': ['count>=0'],
  'checks{scenario:medicao}': ['rate>=0'],
};

export const estatisticas = ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'];

export function resumo(data) {
  return { '/resultados/ultimo.json': JSON.stringify(data, null, 2) };
}
