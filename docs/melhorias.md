# Melhorias propostas

Este documento reúne melhorias técnicas e de apresentação para o projeto. A
ordem abaixo prioriza correção financeira e resiliência antes de recursos mais
sofisticados. Itens que já tenham sido implementados devem ser marcados após a
validação por testes.

Revisão externa do código feita em 2026-10-21, depois da fase 2. As marcações
foram atualizadas ao fim da fase 4; o que continua aberto entra nas próximas
fases do [roteiro](roteiro.md).

## 1. Núcleo financeiro

- [x] Travar contas em ordem crescente de `id`, eliminando o deadlock clássico
  entre transferências A → B e B → A.
- [x] Tratar liquidações repetidas de forma idempotente.
- [x] Representar dinheiro em centavos, sem ponto flutuante.
- [x] Proteger a formatação de `Centavos` no limite mínimo de `int64`.
- [ ] Garantir que cada lançamento pertença ao pagador ou recebedor da
  transferência correspondente.
- [x] Reforçar a relação entre `status`, `motivo_rejeicao` e `finalizada_em`
  com constraints no Postgres.
- [x] Verificar separadamente, na conciliação, o débito do pagador e o crédito
  do recebedor, além da soma zero.
- [ ] Definir e testar o comportamento de contas bloqueadas, encerradas ou
  inexistentes durante a liquidação.
- [ ] Tornar a recuperação de aportes interrompidos explícita. Atualmente, a
  criação e a liquidação de um aporte podem acontecer em transações distintas.

## 2. Testes de correção e concorrência

- [x] Testar transferências concorrentes e entregas duplicadas.
- [x] Usar fuzzing para validar limites e conservação do dinheiro.
- [ ] Criar um teste direcionado para A → B e B → A simultâneas.
- [ ] Executar muitas liquidações concorrentes para o mesmo identificador.
- [ ] Testar rollback entre inserção dos lançamentos e atualização dos saldos.
- [ ] Testar timeout e cancelamento de contexto durante operações de banco.
- [ ] Verificar que a conciliação detecta individualmente cada adulteração
  possível.
- [ ] Testar a aplicação repetida das migrações.
- [ ] Executar os testes de concorrência com várias sementes aleatórias, além
  da semente determinística usada para reprodução.
- [ ] Separar comandos de testes unitários, integração e suíte completa no
  `Makefile`.

## 3. API e idempotência

- [x] Gravar transferência e outbox na mesma transação.
- [x] Responder `202 Accepted` somente depois do commit.
- [x] Disponibilizar consulta de status por identificador.
- [x] Diferenciar repetição idêntica de conflito de conteúdo.
- [x] Limitar o tamanho do corpo e rejeitar campos JSON desconhecidos.
- [x] Rejeitar conteúdo adicional depois do primeiro objeto JSON.
- [x] Validar o `Content-Type` das requisições.
- [ ] Adicionar um identificador de requisição aos logs e respostas.
- [ ] Documentar o contrato da API com OpenAPI.
- [x] Testar dezenas de pedidos simultâneos com a mesma chave de idempotência,
  incluindo pedidos conflitantes. *(20 iguais simultâneos feito; faltam os
  conflitantes)*

## 4. Outbox, relay e Redis Streams

- [x] Consumir a outbox com `FOR UPDATE SKIP LOCKED`, permitindo vários relays.
- [x] Manter um identificador determinístico em cada mensagem (`transferencia_id`).
- [x] Confirmar a mensagem com `XACK` somente após o commit no Postgres.
- [x] Recuperar mensagens órfãs (com `XPENDING` + `XCLAIM`, não `XAUTOCLAIM`:
  ver ADR 0003).
- [ ] Implementar retentativas com espera crescente e jitter. *(espera
  crescente feita; falta o jitter)*
- [ ] Registrar quantidade de tentativas, último erro e timestamps. *(entregas
  e último erro feitos; faltam os timestamps)*
- [x] Mover falhas definitivas para uma dead-letter stream.
- [ ] Definir política de retenção e limpeza dos streams. *(stream principal:
  `XDEL` ao confirmar; falta a das mortas)*
- [x] Provar em teste que publicação ou entrega duplicada não duplica a
  liquidação.

## 5. Banco de dados

- [x] Criar índice parcial para itens pendentes da outbox.
- [ ] Criar índices adequados para transferências pendentes e consultas por
  status.
- [ ] Escrever testes para todas as constraints relevantes.
- [ ] Avaliar queries críticas com `EXPLAIN (ANALYZE, BUFFERS)`.
- [ ] Manter migrações pequenas, progressivas e idempotentes no processo de
  aplicação.
- [ ] Separar dados pessoais do núcleo financeiro, usando identificadores
  opacos sempre que possível.

## 6. Segurança e dados pessoais

- [ ] Cifrar CPF com AES-GCM e uma chave por titular.
- [ ] Implementar *crypto-shredding* e demonstrá-lo em teste.
- [ ] Implementar auditoria encadeada com HMAC-SHA256.
- [ ] Criar verificador capaz de apontar a primeira quebra da cadeia.
- [ ] Não registrar chaves PIX, CPF, credenciais ou corpos completos em logs.
- [ ] Executar `govulncheck` na integração contínua.
- [ ] Revisar permissões do usuário do Postgres para que a proteção do
  livro-razão não dependa apenas de convenção no código.

## 7. Observabilidade

- [ ] Emitir logs JSON com `request_id`, `transferencia_id` e `end_to_end_id`
  quando apropriado.
- [ ] Medir idade do item mais antigo da outbox.
- [ ] Medir transferências pendentes por faixa de tempo.
- [ ] Medir duplicidades, rejeições por motivo e latência até a liquidação.
- [ ] Medir mensagens recuperadas, retentativas e tamanho da DLQ.
- [ ] Expor o resultado e o horário da última conciliação.
- [ ] Instrumentar API, relay e worker com OpenTelemetry.
- [ ] Provisionar Prometheus e Grafana no ambiente local.

## 8. Resiliência e operação

- [x] Criar testes de caos que interrompam Redis, worker e Postgres durante a
  carga.
- [x] Depois de cada falha, provar que nada aceito foi perdido ou liquidado
  duas vezes.
- [x] Criar `docs/runbook.md` para fila acumulada, DLQ, falha de conciliação e
  indisponibilidade do banco.
- [ ] Configurar timeouts, encerramento gracioso e health checks em todos os
  processos. *(timeouts e encerramento gracioso feitos; health check só na API)*
- [ ] Documentar objetivos de recuperação e limitações reais da arquitetura.

## 9. CI e qualidade

- [x] Executar `gofmt`, `go vet`, lint e testes com detector de corrida.
- [ ] Publicar cobertura sem transformar percentual em objetivo isolado.
- [ ] Executar fuzzing por tempo limitado na CI.
- [ ] Construir todos os binários e imagens Docker na CI.
- [ ] Validar o `docker-compose` e as migrações na pipeline.
- [ ] Fixar versões das ferramentas utilizadas pela CI.
- [ ] Revisar dependências com `go mod tidy` antes de cada entrega.

## 10. Apresentação do projeto

- [ ] Criar um `README.md` com resumo em inglês, visão geral em português,
  diagrama, comandos e exemplos com `curl`.
- [ ] Criar `make demo` para subir a infraestrutura, migrar, semear contas,
  transferir e executar a conciliação.
- [ ] Documentar garantias comprovadas e também o que o sistema não garante.
- [x] Registrar decisões importantes em ADRs curtos.
- [ ] Publicar somente números de desempenho acompanhados da máquina, cenário,
  duração, p50, p95, p99 e taxa de erros.

## Ordem sugerida de execução

1. Encerrar e validar o núcleo financeiro.
2. Entregar API e outbox como uma fatia completa.
3. Concluir relay e worker com recuperação de mensagens.
4. Automatizar testes de concorrência, integração e caos.
5. Adicionar métricas, rastreamento e runbook.
6. Medir carga e finalizar a apresentação.
7. Implementar auditoria criptográfica e proteção de dados pessoais.

O critério principal é profundidade: é melhor demonstrar claramente
consistência financeira, idempotência e recuperação de falhas do que acumular
recursos sem testes que provem suas garantias.
