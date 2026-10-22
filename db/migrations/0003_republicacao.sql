-- Quantas vezes o relay recolocou esta linha para publicar porque a
-- transferência continuava PENDENTE muito depois de publicada (mensagem
-- perdida no Redis, ou presa em retentativas). O limite evita republicar para
-- sempre algo que falha sempre; acima dele, é caso para o operador (runbook).
ALTER TABLE outbox ADD COLUMN republicacoes int NOT NULL DEFAULT 0;
