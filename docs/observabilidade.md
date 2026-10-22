# Observabilidade

Métricas Prometheus em todos os programas, painel Grafana provisionado e uma
conciliação periódica que vira métrica. O rastreamento distribuído
(OpenTelemetry) tem seção própria no fim deste documento.

## Como rodar

```sh
make observabilidade        # sistema + Prometheus + Grafana
make carga-sustentada       # gera carga para o painel ter o que mostrar
```

| Serviço | Endereço | Observação |
|---|---|---|
| Grafana | http://localhost:3001 | painel **Liquidação PIX**; leitura anônima, admin/admin (só desenvolvimento) |
| Prometheus | http://localhost:9090 | alvos em *Status → Targets* |
| `/metrics` dos programas | porta 9100 **dentro** da rede do Compose | não é publicada no host |

Tudo preso ao `127.0.0.1`: o painel não fica exposto na rede local.

## Programas e métricas

| Programa | Métricas |
|---|---|
| `api` | `pix_api_requisicao_duracao_segundos{rota,codigo}` (histograma), `pix_api_pedidos_aceitos_total{resultado=novo\|repetido}` |
| `worker` | `pix_liquidacoes_total{status}`, `pix_rejeicoes_total{motivo}`, `pix_liquidacao_duracao_segundos`, `pix_atraso_aceite_liquidacao_segundos`, `pix_worker_falhas_total{etapa}`, `pix_mensagens_mortas_total`, `pix_grupo_consumo_recriado_total` |
| `relay` | `pix_relay_publicadas_total`, `pix_relay_falhas_total`, `pix_relay_republicadas_total` |
| `conciliador` | estado, lido na hora do scrape: `pix_outbox_pendentes`, `pix_outbox_mais_antiga_segundos`, `pix_transferencias_pendentes`, `pix_transferencia_pendente_mais_antiga_segundos`, `pix_stream_tamanho`, `pix_stream_pendentes_confirmacao`, `pix_stream_mortas_tamanho`, `pix_estado_coleta_sucesso{fonte}`; conciliação: `pix_conciliacao_divergencias`, `pix_conciliacao_ultima_execucao_timestamp_segundos`, `pix_conciliacao_duracao_segundos`, `pix_conciliacao_execucoes_total{resultado}` |
| todos | métricas do runtime Go e do processo (`go_*`, `process_*`) |

### Decisões

- **Idade, não só tamanho.** 10 mil pendentes recém-chegadas podem ser normais,
  10 pendentes de 5 minutos não. Por isso há `…_mais_antiga_segundos` para a
  outbox e para as transferências pendentes, e o painel de resumo usa a idade.
- **Fonte fora do ar não vira zero.** Se o Postgres ou o Redis não respondem na
  coleta de estado, as séries dela são omitidas e
  `pix_estado_coleta_sucesso{fonte}` vai a 0. Um zero seria uma mentira que
  parece saúde. O painel mostra "FALHANDO".
- **O atraso conta só a primeira finalização.** Uma mensagem entregue 3 vezes
  gera 1 observação, não 3 (hook `AoFinalizar`, com teste e mutação).
- **Uma instância do conciliador.** O estado é lido no scrape; réplicas
  duplicariam as séries. Os outros programas podem ter quantas réplicas quiser.

## Regras de cardinalidade e dados sensíveis

Rótulos só podem ter conjuntos **pequenos e fixos** de valores:

| Rótulo | Valores |
|---|---|
| `rota` | padrão do roteador (`POST /transferencias`, `GET /transferencias/{id}`, `GET /saude`) ou `nao_encontrada` |
| `codigo` | código HTTP |
| `status` | `LIQUIDADA`, `REJEITADA` |
| `motivo` | motivos do domínio (hoje `SALDO_INSUFICIENTE`) |
| `etapa` | `liquidar`, `confirmar`, `ler` |
| `resultado` | `novo`/`repetido`; `fechada`/`divergente`/`erro` |
| `fonte` | `postgres`, `redis` |

**Nunca** vão para rótulo: id de transferência, `end_to_end_id`, chave PIX, CPF,
número de conta, texto de erro. O que é por-pedido pertence a logs e traces. O
teste `TestMetricasDaAPINaoExplodemACardinalidade` manda 1.000 ids e 500
caminhos aleatórios e confere que sobram 3 séries.

## Painel

Resumo (verde = tudo bem; as cores de status são fixas e vêm sempre com texto
ou número, nunca só a cor), fluxo, backlog, latência da API, atraso até a
liquidação, idade do que espera e falhas. Cada painel tem uma descrição (ícone
`i`) dizendo o que ler nele. As cores de série são as três primeiras da paleta
categórica validada, com a mesma cor para o mesmo conceito em painéis vizinhos
(p50 azul, p95 laranja, p99 verde-água).

| Se o painel mostra… | Ver no runbook |
|---|---|
| Outbox mais antiga subindo | [Outbox crescendo](runbook.md#outbox-crescendo) |
| Pendente mais antiga subindo com outbox vazia | [Stream crescendo](runbook.md#stream-crescendo-atraso-de-liquidação) |
| Mensagens mortas > 0 | [Mensagens mortas](runbook.md#mensagens-mortas) |
| Conciliação DIVERGENTE | [Conciliação divergente](runbook.md#conciliação-divergente) |
| Coleta de estado FALHANDO | Postgres ou Redis fora do ar; ver os logs do `conciliador` |

## Custo da observabilidade

- A coleta de estado faz 2 consultas ao Postgres (ambas pelos índices parciais
  de pendentes, ver migração 0006) e 3 ao Redis a cada scrape (5 s).
- A conciliação **varre as tabelas inteiras**. O intervalo padrão é de 120 s
  (`CONCILIACAO_INTERVALO_SEGUNDOS`); com bancos grandes, ajuste olhando
  `pix_conciliacao_duracao_segundos`.
- Impacto do índice parcial na carga: ver "Índice das pendentes" abaixo.
