# Roteiro

Cada fase termina com algo que roda e um teste que prova a propriedade da fase.
O estado de cada item fica marcado aqui e resumido na `MEMORIA_TECNICA.md`.
Itens menores vindos da revisão do código estão em [`melhorias.md`](melhorias.md)
e entram nas fases abaixo conforme o assunto.

## Fase 0 — Esqueleto
- [x] Módulo Go, `Makefile`, `docker-compose` com Postgres e Redis
- [x] Migrações SQL e um comando para aplicá-las
- [x] CI no GitHub Actions (gofmt, vet, golangci-lint, testes com `-race`)

## Fase 1 — Livro-razão e liquidação
- [x] Domínio: dinheiro em centavos, conta, transferência, lançamento
- [x] Esquema: contas, transferências, lançamentos só de inserção, conta de liquidez
- [x] Caso de uso `Liquidar`: trava em ordem, idempotente, rejeita sem saldo
- [x] Teste de concorrência: milhares de transferências cruzadas, nenhum saldo
      negativo, soma global zero
- [x] Conciliação (soma global, saldo × lançamentos, pares de lançamentos)

## Fase 2 — API de ingestão
- [x] `POST /transferencias` com transferência + outbox na mesma transação
- [x] Idempotência por `end_to_end_id` (mesmo pedido → mesmo resultado; conteúdo diferente → 409)
- [x] `GET /transferencias/{id}`
- [x] `GET /saude` e encerramento gracioso

## Fase 3 — Fila resiliente
- [x] Relay da outbox para Redis Stream (`SKIP LOCKED`)
- [x] Worker com grupo de consumo, `XACK`, `XCLAIM` (ver ADR 0003)
- [x] Novas tentativas com espera crescente e stream de mensagens mortas
- [x] Teste: mensagem entregue duas vezes é liquidada uma vez
- [x] Varredura de órfãs: mensagem perdida no Redis é republicada

## Fase 4 — Caos
- [x] Programa que derruba Redis, worker, Postgres, relay e API durante a carga e apaga o stream (`tests/caos`)
- [x] Verificação final: nada perdido, nada duplicado, conciliação fechada
- [x] `docs/runbook.md`

## Fase 5 — Carga
- [x] Scripts k6 (pico e carga sustentada)
- [x] Números e máquina no README

## Fase 6 — Observabilidade
- [x] Métricas Prometheus, painel Grafana provisionado e alertas baseados em idade (`make testar-alertas`)
- [x] Rastreamento OpenTelemetry API → stream → worker

## Fase 7 — Auditoria e dados pessoais
- [ ] Auditoria em cadeia de hashes (HMAC) + verificador
- [ ] CPF cifrado com chave por titular + *crypto-shredding*

## Fase 8 — Apresentação
- [x] README final (resumo em inglês, diagrama, como rodar, números)
- [x] ADRs (0001 a 0004)
- [x] `make demo` e contrato OpenAPI (`docs/openapi.yaml`)
