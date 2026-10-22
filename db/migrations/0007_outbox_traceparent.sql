-- Contexto do rastro (W3C traceparent) do pedido que gerou a linha. O relay o
-- lê para continuar o mesmo rastro depois da fila (internal/telemetria).
-- Nulo quando o rastreamento está desligado. Coluna nula sem default: só
-- metadado do catálogo no Postgres 11+, sem reescrever a tabela.
ALTER TABLE outbox ADD COLUMN traceparent text;
