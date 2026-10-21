# Roteiro

Cada fase termina com algo que roda e um teste que prova a propriedade da fase.
O estado de cada item fica marcado aqui e resumido na `MEMORIA_TECNICA.md`.

## Fase 0 — Esqueleto
- [ ] Módulo Go, `Makefile`, `docker-compose` com Postgres e Redis
- [ ] Migrações SQL e um comando para aplicá-las
- [ ] CI no GitHub Actions (gofmt, vet, golangci-lint, testes com `-race`)

## Fase 1 — Livro-razão e liquidação
- [x] Domínio: dinheiro em centavos, conta, transferência, lançamento
- [x] Esquema: contas, transferências, lançamentos só de inserção, conta de liquidez
- [x] Caso de uso `Liquidar`: trava em ordem, idempotente, rejeita sem saldo
- [x] Teste de concorrência: milhares de transferências cruzadas, nenhum saldo
      negativo, soma global zero
- [x] Conciliação (soma global, saldo × lançamentos, pares de lançamentos)

## Fase 2 — API de ingestão
- [ ] `POST /transferencias` com transferência + outbox na mesma transação
- [ ] Idempotência por `end_to_end_id` (mesmo pedido → mesmo resultado; conteúdo diferente → 409)
- [ ] `GET /transferencias/{id}`
- [ ] `GET /saude` e encerramento gracioso

## Fase 3 — Fila resiliente
- [ ] Relay da outbox para Redis Stream (`SKIP LOCKED`)
- [ ] Worker com grupo de consumo, `XACK`, `XAUTOCLAIM`
- [ ] Novas tentativas com espera crescente e stream de mensagens mortas
- [ ] Teste: mensagem entregue duas vezes é liquidada uma vez

## Fase 4 — Caos
- [ ] Script que derruba Redis, worker e Postgres durante a carga
- [ ] Verificação final: nada perdido, nada duplicado, conciliação fechada
- [ ] `docs/runbook.md`

## Fase 5 — Carga
- [ ] Scripts k6 (pico e carga sustentada)
- [ ] Números e máquina no README

## Fase 6 — Observabilidade
- [ ] Métricas Prometheus e painel Grafana provisionado
- [ ] Rastreamento OpenTelemetry API → stream → worker

## Fase 7 — Auditoria e dados pessoais
- [ ] Auditoria em cadeia de hashes (HMAC) + verificador
- [ ] CPF cifrado com chave por titular + *crypto-shredding*

## Fase 8 — Apresentação
- [ ] README final (resumo em inglês, diagrama, como rodar, números)
- [ ] ADRs revisados
