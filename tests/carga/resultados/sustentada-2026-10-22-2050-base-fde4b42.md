# Carga: sustentada — 2026-10-22 20:54

- Máquina: AMD Ryzen 7 7735HS with Radeon Graphics, 16 threads, 13 GB de RAM; k6, API, relay, worker,
  Postgres e Redis no mesmo Docker (29.9.2).
- Massa: 1000 contas; valores de 1 a 5.000 centavos; pares aleatórios.
- Cenário: `tests/carga/sustentada.js` (PICO=padrão TAXA=1000 DURACAO=padrão).
- Worker: 32 consumidores; banco reaproveitado de rodadas anteriores.
  Os números abaixo são só da fase de medição; o aquecimento fica de fora.
- Commit: `fde4b42`

## API (k6)

| Pedidos | Erros | HTTP ≥ 400 | Transporte/timeout | Descartados pelo k6 | p50 | p95 | p99 | máx. |
|---|---|---|---|---|---|---|---|---|
| 180000 | 1 (0.001 %) | 1 | 0 | 0 | 2.9 ms | 4.6 ms | 8.1 ms | 1002.2 ms |

"Descartados" são pedidos que o k6 deixou de enviar por falta de usuários
virtuais livres: se for diferente de zero, a taxa planejada não foi atingida.

## Liquidação (banco)

| Transferências | Liquidadas | Rejeitadas por saldo | Fila ao fim da carga | Tempo para esvaziar |
|---|---|---|---|---|
| 179999 | 179999 | 0 | 0 | 0 s |

| Vazão por segundo | mediana | máxima |
|---|---|---|
| Aceite (API) | 1000/s | 1002/s |
| Liquidação (worker) | 1000/s | 1042/s |

| Atraso aceite → liquidação | p50 | p95 | p99 | máx. |
|---|---|---|---|---|
| | 14.5 ms | 24.5 ms | 29.0 ms | 478.4 ms |

Conciliação depois da carga: fechada, sem divergências.
