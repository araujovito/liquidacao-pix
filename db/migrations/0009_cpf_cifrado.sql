-- CPF do titular cifrado, com uma chave de dados por conta (ADR 0006).
--
-- O banco só vê bytes: o CPF cifrado com AES-256-GCM e a chave de dados da
-- conta embrulhada por uma chave-mestra que NÃO está aqui. Apagar o dado
-- pessoal (LGPD art. 18, GDPR art. 17) é apagar a linha de chaves_cpf: o
-- texto cifrado fica e deixa de poder ser lido. Lançamentos e auditoria
-- referenciam só conta_id e continuam íntegros e verificáveis.

ALTER TABLE contas
    ADD COLUMN cpf_cifrado    bytea,
    ADD COLUMN cpf_apagado_em timestamptz,
    ADD CONSTRAINT cpf_apagado_exige_cifrado
        CHECK (cpf_apagado_em IS NULL OR cpf_cifrado IS NOT NULL);

CREATE TABLE chaves_cpf (
    conta_id         bigint PRIMARY KEY REFERENCES contas (id),
    mestra_id        text   NOT NULL,
    nonce_embrulho   bytea  NOT NULL CHECK (octet_length(nonce_embrulho) = 12),
    -- chave de dados (32 bytes) cifrada com a mestra, mais a tag de 16 bytes
    chave_embrulhada bytea  NOT NULL CHECK (octet_length(chave_embrulhada) = 48),
    criada_em        timestamptz NOT NULL DEFAULT now()
);
