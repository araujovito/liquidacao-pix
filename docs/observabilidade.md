# Observabilidade

Métricas Prometheus em todos os programas, painel Grafana provisionado, uma
conciliação periódica que vira métrica e rastreamento distribuído
(OpenTelemetry) de um pedido da API até a liquidação.

## Como rodar

```sh
make observabilidade        # sistema + Prometheus + Grafana
make carga-sustentada       # gera carga para o painel ter o que mostrar
```

| Serviço | Endereço | Observação |
|---|---|---|
| Grafana | http://localhost:3001 | painel **Liquidação PIX**; leitura anônima, admin/admin (só desenvolvimento) |
| Prometheus | http://localhost:9090 | alvos em *Status → Targets* |
| Jaeger | http://localhost:16686 | traces dos serviços `pix-api`, `pix-relay`, `pix-worker`; também é fonte de dados no Grafana |
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

## Rastreamento (OpenTelemetry)

```sh
make observabilidade        # liga o rastreamento e sobe o Jaeger
```

Sem `OTEL_EXPORTER_OTLP_ENDPOINT` o rastreamento fica desligado e não custa
nada (os spans viram no-op); `make sistema` sobe assim. O `make
observabilidade` aponta para o Jaeger (`http://jaeger:4318`).

### O desafio: o pedido não passa por uma chamada HTTP entre os elos

O caminho é API → outbox → relay → Redis → worker. Nenhuma chamada liga um
elo ao seguinte, então o contexto do rastro **viaja como dado**:

```text
POST /transferencias            (servidor, pix-api)
└─ aceitar_pedido               grava o traceparent na linha da outbox
   └─ relay.publicar            (produtor, pix-relay) abre o traceparent da
      │                          outbox e grava o seu próprio na mensagem
      └─ worker.liquidar        (consumidor, pix-worker) continua dali
```

O formato é o `traceparent` do W3C. A API o grava em `outbox.traceparent`
(migração 0007); o relay o lê na mesma consulta que pega o lote; a mensagem do
stream o leva no campo `traceparent`. O intervalo entre o fim do
`relay.publicar` e o início do `worker.liquidar` é o tempo que a mensagem
esperou na fila; entre o fim de `aceitar_pedido` e o início de
`relay.publicar`, o tempo na outbox.

Verificado num pedido real: 1 trace, 3 serviços, 4 spans, cadeia de pais
correta (consulta ao Jaeger, não só teste).

### Decisões

- **Rastreamento nunca é caminho crítico.** `traceparent` inválido ou ausente na
  mensagem abre um rastro novo e a liquidação segue (teste). Falha ao ligar o
  exportador desliga o rastreamento e o serviço sobe.
- **Erro do cliente não marca o span como erro.** Chave desconhecida, valor
  inválido e conflito de `end_to_end_id` são respostas 4xx: o serviço fez o que
  devia. Só falha do serviço (banco fora, erro interno) marca o span.
- **O span da API tem o nome do padrão da rota** (`GET /transferencias/{id}`),
  nunca o caminho, pelo mesmo motivo da cardinalidade das métricas. `/saude`
  não gera span.
- **O span do relay é um por pedido, mas o envio é um só.** O lote vai num
  pipeline do Redis; cada span começa no início do lote e termina quando ele
  acaba. Mostra a parte da vida do pedido passada no relay, não um tempo
  individual de envio.
- **Amostragem pelas variáveis padrão** (`OTEL_TRACES_SAMPLER`,
  `OTEL_TRACES_SAMPLER_ARG`). Padrão do Compose: `parentbased_traceidratio`
  com razão 1. Sob carga pesada, baixe a razão.

### O que nunca vai para um atributo de span

Chave PIX, CPF, nome do titular, valor e `end_to_end_id`. Só entram o id da
transferência (UUID opaco), o status final e o número da entrega. O corpo das
requisições não é registrado. O teste do fluxo completo percorre todos os
atributos, eventos e descrições de status de todos os spans e falha se achar
as chaves PIX ou o `end_to_end_id` do pedido.

### Custo sob carga

4 rodadas de 1.000 pedidos/s por 1 min (32 consumidores, Prometheus ligado), em
sequência no mesmo banco, que cresce a cada rodada:

| Ordem | Rastreamento | Erros / descartes | API p50 | p95 | p99 | Relatório |
|---|---|---|---|---|---|---|
| 1 | desligado | 0 / 0 | 3,0 ms | 4,7 ms | 6,5 ms | `…-2327` |
| 2 | ligado, 1% | 0 / 0 | 3,1 ms | 4,8 ms | 6,9 ms | `…-2329` |
| 3 | ligado, 100% | 0 / 0 | 3,6 ms | 8,4 ms | 12,1 ms | `…-2331` |
| 4 | desligado | 0 / 0 | 3,6 ms | 8,5 ms | 12,3 ms | `…-2333` |

**Sem custo mensurável a 1.000/s.** A rodada de 100% (3ª) é indistinguível da
rodada seguinte sem rastreamento (4ª): a subida de 6,5 para 12 ms ao longo da
sequência acompanha o crescimento do banco, não o rastreamento. A ordem foi
escolhida para penalizar o rastreamento (100% rodou com o banco já maior), e
mesmo assim não houve diferença atribuível a ele. Não foi medido acima de
1.000/s. O Jaeger guarda os traces em memória (até 20 mil), então o que
sobra de uma rodada de carga é só a amostra mais recente.

