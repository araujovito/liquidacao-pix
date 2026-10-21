-- Livro-razão de partidas dobradas.
--
-- contas.saldo_centavos é uma projeção dos lançamentos, mantida na mesma
-- transação que os grava. A conciliação confere que as duas coisas batem.

CREATE TABLE contas (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chave_pix      text   NOT NULL UNIQUE,
    titular        text   NOT NULL,
    -- Conta técnica: a de liquidez, por onde o dinheiro entra no sistema.
    -- É a única que pode ficar negativa; com ela a soma de todos os
    -- lançamentos do sistema é sempre zero.
    tecnica        boolean NOT NULL DEFAULT false,
    saldo_centavos bigint  NOT NULL DEFAULT 0,
    criada_em      timestamptz NOT NULL DEFAULT now(),
    -- Última linha de defesa: mesmo que o código erre, o banco recusa
    -- deixar cliente negativo.
    CONSTRAINT saldo_nao_negativo CHECK (tecnica OR saldo_centavos >= 0)
);

CREATE TABLE transferencias (
    id              uuid   PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Identificador de ponta a ponta do PIX; chave de idempotência.
    end_to_end_id   text   NOT NULL UNIQUE,
    pagador_id      bigint NOT NULL REFERENCES contas (id),
    recebedor_id    bigint NOT NULL REFERENCES contas (id),
    valor_centavos  bigint NOT NULL CHECK (valor_centavos > 0),
    status          text   NOT NULL DEFAULT 'PENDENTE'
                    CHECK (status IN ('PENDENTE', 'LIQUIDADA', 'REJEITADA')),
    motivo_rejeicao text,
    criada_em       timestamptz NOT NULL DEFAULT now(),
    finalizada_em   timestamptz,
    CONSTRAINT contas_distintas CHECK (pagador_id <> recebedor_id),
    CONSTRAINT motivo_so_quando_rejeitada
        CHECK ((status = 'REJEITADA') = (motivo_rejeicao IS NOT NULL))
);

CREATE TABLE lancamentos (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transferencia_id uuid   NOT NULL REFERENCES transferencias (id),
    conta_id         bigint NOT NULL REFERENCES contas (id),
    -- Negativo = débito, positivo = crédito.
    valor_centavos   bigint NOT NULL CHECK (valor_centavos <> 0),
    criado_em        timestamptz NOT NULL DEFAULT now(),
    -- Uma transferência movimenta cada conta uma única vez: liquidar a mesma
    -- transferência duas vezes viola esta restrição, mesmo que a checagem de
    -- status no código falhe.
    UNIQUE (transferencia_id, conta_id)
);

CREATE INDEX lancamentos_conta ON lancamentos (conta_id);

-- Lançamentos são imutáveis: correção se faz com novo lançamento, nunca
-- alterando ou apagando o antigo.
CREATE FUNCTION recusar_alteracao_lancamento() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'lancamentos são só de inserção (% recusado)', TG_OP;
END;
$$;

CREATE TRIGGER lancamentos_imutaveis
    BEFORE UPDATE OR DELETE ON lancamentos
    FOR EACH ROW EXECUTE FUNCTION recusar_alteracao_lancamento();

CREATE TRIGGER lancamentos_sem_truncate
    BEFORE TRUNCATE ON lancamentos
    FOR EACH STATEMENT EXECUTE FUNCTION recusar_alteracao_lancamento();

INSERT INTO contas (chave_pix, titular, tecnica)
VALUES ('liquidez@sistema', 'Conta técnica de liquidez', true);
