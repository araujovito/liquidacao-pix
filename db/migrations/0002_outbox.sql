-- Outbox: cada transferência aceita gera uma linha aqui, na mesma transação.
-- O relay copia as linhas não publicadas para o Redis Stream.
CREATE TABLE outbox (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transferencia_id uuid NOT NULL UNIQUE REFERENCES transferencias (id),
    criada_em        timestamptz NOT NULL DEFAULT now(),
    publicada_em     timestamptz
);

-- O relay só olha o que falta publicar; o índice parcial fica pequeno mesmo
-- com milhões de linhas já publicadas.
CREATE INDEX outbox_pendentes ON outbox (id) WHERE publicada_em IS NULL;
