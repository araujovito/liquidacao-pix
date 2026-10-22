-- Índice parcial para as métricas de operação: quantas transferências estão
-- PENDENTE e há quanto tempo a mais antiga espera. Sem ele, cada coleta do
-- Prometheus varreria a tabela inteira (milhões de linhas).
--
-- Custo conhecido: como a coluna status entra no predicado, a atualização de
-- status na finalização deixa de ser HOT (heap-only tuple). O impacto foi
-- medido com a carga sustentada; resultado em docs/carga.md.
CREATE INDEX transferencias_pendentes ON transferencias (criada_em) WHERE status = 'PENDENTE';
