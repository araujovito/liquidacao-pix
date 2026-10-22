-- Id da entrada no Redis Stream, gravado quando o relay publica. Permite à
-- varredura de órfãs perguntar ao Redis se a mensagem ainda está lá (na fila
-- ou em processamento) antes de concluir que se perdeu.
ALTER TABLE outbox ADD COLUMN mensagem_id text;
