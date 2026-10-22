// Cenário de carga sustentada: 15 s de aquecimento a 200 pedidos/s, depois
// TAXA pedidos/s (padrão 1500) constantes por DURACAO (padrão 3m).
// Pergunta: o sistema liquida no mesmo ritmo em que aceita, sem a fila crescer?
import { transferir, limites, estatisticas, resumo } from './comum.js';

export const options = {
  scenarios: {
    aquecimento: {
      executor: 'constant-arrival-rate', exec: 'transferir',
      rate: 200, timeUnit: '1s', duration: '15s', preAllocatedVUs: 50, maxVUs: 200,
    },
    medicao: {
      executor: 'constant-arrival-rate', exec: 'transferir', startTime: '15s',
      rate: parseInt(__ENV.TAXA || '1500', 10), timeUnit: '1s',
      duration: __ENV.DURACAO || '3m', preAllocatedVUs: 300, maxVUs: 2000,
    },
  },
  thresholds: limites,
  summaryTrendStats: estatisticas,
};

export { transferir };
export const handleSummary = resumo;
