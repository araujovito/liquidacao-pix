-- Trilha de auditoria em cadeia de HMAC, uma cadeia por conta (ADR 0006).
--
-- A entrada é gravada pela aplicação na mesma transação que finaliza a
-- transferência. O MAC é calculado com uma chave que NÃO está no banco, então
-- quem edita linhas aqui não consegue produzir uma cadeia que se verifique.
-- O gatilho abaixo protege contra engano e contra o acesso comum; contra quem
-- desliga gatilhos, quem protege é o HMAC.

-- Cabeça da cadeia de cada conta: posição e MAC da última entrada. Lida e
-- atualizada com a conta já travada (FOR UPDATE) pela liquidação, na mesma
-- ordem de trava de sempre; não cria trava nem ordem de trava nova.
ALTER TABLE contas
    ADD COLUMN auditoria_seq bigint NOT NULL DEFAULT 0
        CONSTRAINT auditoria_seq_nao_negativa CHECK (auditoria_seq >= 0),
    ADD COLUMN auditoria_mac bytea NOT NULL DEFAULT decode(repeat('00', 32), 'hex')
        CONSTRAINT auditoria_mac_32_bytes CHECK (octet_length(auditoria_mac) = 32),
    ADD CONSTRAINT auditoria_cabeca_coerente
        CHECK ((auditoria_seq = 0) = (auditoria_mac = decode(repeat('00', 32), 'hex')));

CREATE TABLE auditoria (
    conta_id         bigint NOT NULL REFERENCES contas (id),
    seq              bigint NOT NULL CHECK (seq > 0),
    transferencia_id uuid   NOT NULL REFERENCES transferencias (id),
    status           text   NOT NULL CHECK (status IN ('LIQUIDADA', 'REJEITADA')),
    motivo           text,
    pagador_id       bigint NOT NULL,
    recebedor_id     bigint NOT NULL,
    valor_centavos   bigint NOT NULL CHECK (valor_centavos > 0),
    -- Igual a transferencias.finalizada_em: um único instante, produzido pela
    -- aplicação, passa para as duas tabelas. O verificador exige a igualdade.
    decidida_em      timestamptz NOT NULL,
    chave_id         text   NOT NULL,
    mac_anterior     bytea  NOT NULL CHECK (octet_length(mac_anterior) = 32),
    mac              bytea  NOT NULL CHECK (octet_length(mac) = 32),
    PRIMARY KEY (conta_id, seq),
    -- Uma decisão aparece uma vez em cada cadeia.
    UNIQUE (conta_id, transferencia_id),
    CONSTRAINT motivo_so_quando_rejeitada
        CHECK ((status = 'REJEITADA') = (motivo IS NOT NULL))
);

CREATE INDEX auditoria_transferencia ON auditoria (transferencia_id);

CREATE FUNCTION recusar_alteracao_auditoria() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'auditoria é só de inserção (% recusado)', TG_OP;
END;
$$;

CREATE TRIGGER auditoria_imutavel
    BEFORE UPDATE OR DELETE ON auditoria
    FOR EACH ROW EXECUTE FUNCTION recusar_alteracao_auditoria();

CREATE TRIGGER auditoria_sem_truncate
    BEFORE TRUNCATE ON auditoria
    FOR EACH STATEMENT EXECUTE FUNCTION recusar_alteracao_auditoria();
