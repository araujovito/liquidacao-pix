// Cenário de pico: 15 s de aquecimento a 200 pedidos/s, depois sobe até
// PICO pedidos/s (padrão 4000) em 10 s, segura 20 s e desce em 10 s.
// Pergunta: até onde a API aceita sem erro, e quanto a fila acumula?
import { transferir, limites, estatisticas, resumo } from './comum.js';

const PICO = parseInt(__ENV.PICO || '4000', 10);

export const options = {
  scenarios: {
    aquecimento: {
      executor: 'constant-arrival-rate', exec: 'transferir',
      rate: 200, timeUnit: '1s', duration: '15s', preAllocatedVUs: 50, maxVUs: 200,
    },
    medicao: {
      executor: 'ramping-arrival-rate', exec: 'transferir', startTime: '15s',
      startRate: 200, timeUnit: '1s', preAllocatedVUs: 400, maxVUs: 3000,
      stages: [
        { target: PICO, duration: '10s' },
        { target: PICO, duration: '20s' },
        { target: 200, duration: '10s' },
      ],
    },
  },
  thresholds: limites,
  summaryTrendStats: estatisticas,
};

export { transferir };
export const handleSummary = resumo;
