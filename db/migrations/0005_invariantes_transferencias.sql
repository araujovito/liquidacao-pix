-- Mantém status, motivo e data de finalização coerentes mesmo quando uma
-- escrita não passa pelos casos de uso da aplicação.
ALTER TABLE transferencias
    ADD CONSTRAINT finalizacao_coerente
        CHECK ((status = 'PENDENTE') = (finalizada_em IS NULL)),
    ADD CONSTRAINT finalizacao_nao_antecede_criacao
        CHECK (finalizada_em IS NULL OR finalizada_em >= criada_em),
    ADD CONSTRAINT valor_dentro_do_limite
        CHECK (valor_centavos <= 100000000);
