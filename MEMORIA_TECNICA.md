# Memória técnica

Documento para retomar o trabalho sem depender de memória de ninguém: onde o
projeto está, como rodar, por que cada decisão foi tomada e o que já foi
tentado. É atualizado no mesmo commit da mudança que descreve.

- Especificação: [`docs/especificacao.md`](docs/especificacao.md)
- Roteiro com o andamento: [`docs/roteiro.md`](docs/roteiro.md)
- Pendências da revisão do código: [`docs/melhorias.md`](docs/melhorias.md)
- Decisões: [`docs/adr/`](docs/adr/)

## Estado atual

Fases 0 a 8 implementadas: fluxo completo API → outbox → relay → Redis Stream →
worker, resiliente a SIGKILL em cada serviço e à perda total do stream; teste de
carga; métricas, painel, alertas e rastreamento; `make demo`, OpenAPI e README;
**auditoria em cadeia de HMAC por conta e CPF cifrado com apagamento por
descarte de chave** (fase 7, [ADR 0006](docs/adr/0006-auditoria-em-cadeia-e-cpf-cifrado.md)).
A fase 7 é de risco crítico e está com **`REVISAO_PENDENTE`**: a revisão
independente e a auditoria final ainda não foram concluídas. Último teste
de caos com o código atual, já com a trilha: 44.337 pedidos aceitos, todos
liquidados, 0 perdidos ou duplicados, conciliação fechada e 88.874 entradas de
auditoria em 101 contas, 0 problemas.

## Ambiente

- Go 1.27 (o `go.mod` declara a versão mínima).
- Docker com Compose v2 para Postgres 17 e Redis 7.
- Nenhuma credencial real: todas as senhas do `docker-compose` são de
  desenvolvimento. O código não tem URL padrão de banco nem de Redis; o
  `Makefile` exporta `DATABASE_URL` e `REDIS_URL` de desenvolvimento.

## Como rodar

```sh
make infra          # Postgres e Redis via docker compose
make migrar         # aplica db/migrations
make semear         # cliente-0001@pix … cliente-0100@pix com R$ 10.000 cada
make api            # API em :8080 (PORTA muda)
make relay          # outbox → Redis Stream
make worker         # liquida (WORKERS=32 consumidores por processo)
make demo           # demonstração de ponta a ponta em contêineres (~1 min; 9 passos)
make observabilidade   # sistema + Prometheus, Grafana, Jaeger e rastreamento ligado
make testar-alertas    # regras de alerta com séries sintéticas (promtool)
make carga-sustentada  # k6 (ver docs/carga.md)
make sistema        # tudo em contêineres (imagem distroless, usuário não-root)
make caos           # teste de caos contra o sistema em contêineres (~2 min); precisa de banco criado depois da fase 7
make auditar        # verifica a cadeia de HMAC (ARGS=-ancora / ARGS="-conferir-ancora f")
make teste          # unidade + integração (Testcontainers sobe contêineres próprios)
make teste-unidade  # só o que roda sem Docker (-short)
make lint           # gofmt, go vet, golangci-lint
```

Exemplo de pedido:

```sh
curl -i -X POST localhost:8080/transferencias -d '{
  "end_to_end_id": "E1234567820261021200000000000001",
  "chave_pagador": "cliente-0001@pix",
  "chave_recebedor": "cliente-0002@pix",
  "valor_centavos": 2500}'
```

Os testes de integração não usam o Postgres do `make infra`: cada pacote sobe
o seu contêiner e cada teste recebe um banco novo e migrado
(`internal/testinfra`).

## Decisões em vigor

| # | Decisão | Por quê |
|---|---|---|
| 1 | Go | Pedido original; concorrência leve (goroutines) e binários estáticos |
| 2 | Postgres é a fonte da verdade; a fila só transporta | Fila pode perder ou repetir; o banco tem transação |
| 3 | Outbox na mesma transação do pedido | 202 só depois do commit; Redis fora do ar não perde pedido |
| 4 | Redis Streams, não Pub/Sub | Pub/Sub não guarda mensagem |
| 5 | Entrega "pelo menos uma vez" + idempotência no consumo | "Exatamente uma vez" na fila não existe; no banco, sob trava, sim |
| 6 | Livro-razão de partidas dobradas, valores em centavos `bigint` | Conservação do dinheiro verificável; sem erro de arredondamento |
| 7 | Contas travadas em ordem crescente de id | Evita *deadlock* entre transferências cruzadas |
| 8 | Erros HTTP em *problem details* (RFC 9457) | Cliente distingue casos por `type` estável |
| 9 | Conta técnica invisível para a API | Ela pode ficar negativa; debitá-la criaria dinheiro |
| 10 | Confirmar = `XACK` + `XDEL`; sem `MAXLEN` | `MAXLEN` pode apagar mensagem não confirmada |
| 11 | Retentativa com espera base·2^(n-1), máx. 6 entregas, depois mortas | Não martelar o banco caído; não tentar para sempre |
| 12 | Varredura de órfãs no relay (30 s, até 5×) | Redis pode perder dados; o banco não deixa pedido sumir |
| 13 | Métricas de idade, não só de tamanho; fonte fora do ar não vira zero | 10 mil pendentes recentes podem ser normais, 10 de 5 minutos não |
| 14 | Contexto do rastro viaja na outbox e no stream ([ADR 0004](docs/adr/0004-contexto-do-rastro-viaja-como-dado.md)) | Nenhuma chamada liga os elos do caminho |
| 15 | Alerta de alvo fora do ar cobre `up == 0` **e** `absent(up)` | Contêiner parado some da descoberta por DNS; só o teste contra o Prometheus real mostrou |
| 16 | Índice parcial das pendentes (migração 0006) | A coleta de estado não varre a tabela; sem custo mensurável a 1.000/s |
| 17 | Auditoria: uma cadeia de HMAC **por conta**, não global (ADR 0006) | A cabeça é lida sob a trava de conta que a liquidação já toma, na mesma ordem: sem trava nova, sem serialização global |
| 18 | Chave do HMAC fora do banco (`AUDITORIA_CHAVES`), id da chave em cada entrada | Quem edita o banco não refaz a cadeia; rotação sem quebrar o passado |
| 19 | Liquidar sem chaveiro falha (`ErrSemChaveiro`) | Nada é finalizado sem trilha; só worker e semear recebem a chave |
| 20 | `decidida_em` vem da aplicação, uma vez, e nunca antes de `criada_em` | O mesmo instante em `finalizada_em` e nas duas entradas; relógio atrasado não viola a restrição |
| 21 | Decisão final sem entrada é achado do verificador | Poda do rabo de uma `LIQUIDADA` é detectada sem âncora (lançamentos imutáveis impedem apagar a transferência) |
| 22 | CPF: AES-256-GCM, chave de dados por conta embrulhada por mestra do ambiente, AAD com conta (e id da mestra no embrulho) | Apagar = `DELETE` em `chaves_cpf`; auditoria e lançamentos só citam `conta_id` |

### Auditoria e CPF

```sh
make auditar                            # íntegra: N contas, M entradas (saída 0) ou achados (1)
make auditar ARGS=-ancora > ancora.txt  # cabeças das cadeias, para guardar fora do banco
make auditar ARGS="-conferir-ancora ancora.txt"
echo 529.982.247-25 | go run ./cmd/titular cadastrar-cpf -chave cliente-0001@pix
go run ./cmd/titular mostrar-cpf -chave cliente-0001@pix   # ***.982.247-**
go run ./cmd/titular apagar-cpf  -chave cliente-0001@pix   # irreversível
```

Variáveis (sem valor padrão no código; o `Makefile` e o Compose trazem chaves de
**desenvolvimento**): `AUDITORIA_CHAVES="id:hex,..."` e `AUDITORIA_CHAVE_ATUAL`
(worker, semear, auditar); `CPF_CHAVES_MESTRAS` e `CPF_CHAVE_MESTRA_ATUAL`
(titular). Cada chave tem 32 bytes em hexadecimal. No Compose, API, relay e
conciliador **não** recebem as chaves.

Um banco criado antes da fase 7 não tem trilha das decisões antigas e o
verificador o acusa (`sem-entrada`), de propósito. Para testar em banco novo sem
apagar volumes: `COMPOSE_PROJECT_NAME=outro make caos`.

## Parâmetros de produção (cmd/relay, cmd/worker)

| Parâmetro | Valor | Onde |
|---|---|---|
| Lote do relay | 200 (`RELAY_LOTE`) | `cmd/relay` |
| Espera com outbox vazia | 10 ms | `cmd/relay` |
| Idade de órfã / máx. republicações | 30 s / 5 | `cmd/relay` |
| Consumidores por processo | 32 (`WORKERS`); com 8 o worker não acompanhava 1.500/s | `cmd/worker` |
| Espera base / máx. entregas | 5 s / 6 (≈2,5 min até morrer) | `cmd/worker` |

Os testes usam tempos menores (espera base 200 ms, órfã 1 s).

## Teste de caos — última rodada de referência

Máquina: notebook AMD Ryzen 7 7735HS (16 threads), 13 GB de RAM, tudo no
mesmo Docker. Carga: 16 clientes por 45 s, sem limite de taxa.

| Momento | Falha |
|---|---|
| 4 s → 9 s | SIGKILL no Redis |
| 12 s → 16 s | SIGKILL no worker |
| 19 s → 25 s | SIGKILL no Postgres |
| 28 s → 32 s | SIGKILL no relay |
| 35 s → 38 s | SIGKILL na API |
| 41 s | stream do Redis apagado (perda total da fila) |

Resultado: 63.735 aceitas, 280 retentativas do cliente, 0 desistências;
todas liquidadas 37 s depois do fim da carga; 38.993 recuperadas pela
varredura de órfãs (as que estavam no stream apagado); conciliação fechada e
dinheiro dos clientes conservado.

Observação para a fase 5: a entrada (~1.400–1.900/s) superou a vazão do
worker (~2.300/s em regime, menos durante as falhas), e a fila acumulou
dezenas de milhares de mensagens. É o que o teste de carga vai medir.

## Histórico

### 2026-10-22 — fase 7: auditoria em cadeia e CPF cifrado
- ADR 0006 escrito primeiro e revisado adversarialmente antes do código; a
  revisão fechou a codificação canônica, a âncora por MAC e o AAD do embrulho. Código
  em commits pequenos: pacote `auditoria` (formato, chaveiro, vetores fixos
  calculados em Python à parte), gravação na liquidação (migração 0008),
  `cmd/auditar`, pacote `dadospessoais`, migração 0009 e `cmd/titular`.
- **A trilha entra na transação da liquidação**, uma entrada na cadeia de cada
  conta. Se gravá-la falha, saldo, lançamentos e status desfazem (teste).
- **Mutação revelou lacunas nos meus próprios testes**, três vezes: a verificação
  da cabeça da conta não tinha teste (adicionado); o AAD da conta no CPF não
  reprovava nada, porque o embrulho da chave já falhava antes (adicionado teste
  que re-embrulha a chave para outra conta); e a âncora precisa comparar o MAC,
  não só o seq.
- **O limite do ADR ficou menor do que eu escrevi.** O ADR dizia que apagar o
  rabo de uma cadeia e recuar a cabeça passa despercebido. Para uma `LIQUIDADA`
  não passa: a transferência e os lançamentos imutáveis ficam, e a verificação
  de "decisão sem entrada" acusa. Só o rabo de uma `REJEITADA`, apagado junto
  com a transferência, exige âncora. Os dois lados têm teste.
- O teste de varredura do CPF tem um controle positivo (planta um CPF em claro e
  exige que a varredura o ache). Sem ele, "não achou" não significaria nada.
- `docker compose` com `COMPOSE_PROJECT_NAME` sobrepõe o `name:` do arquivo:
  permitiu rodar caos e carga em volumes novos sem tocar nos antigos.
- **Custo da cadeia, medido contra o commit anterior** (`fde4b42`, cópia do
  repositório, banco novo, 1.000/s por 3 min): vazão igual, mediana quase igual
  (API p50 2,9 → 3,1 ms), cauda pior (p99 8,1 → 27,4 ms, 163 descartes), e as duas
  rodadas reprovam o critério da carga sustentada. Uma rodada por versão: não dá
  para dizer quanto da cauda é da cadeia. Está em `docs/carga.md`.
- **Revisão do Codex (6802573, cf80702, f6c89b8, 6f030df):** MAC com mais de 32
  bytes era truncado ao ser copiado para o array e podia continuar válido; uma
  decisão ligada a conta apagada sumia da verificação; chave de CPF ausente sem
  marcador fingia apagamento; arquivo de âncoras podia ficar parcial com saída 0;
  a demo calava falha ao restaurar a entrada. Revi: rodei as regressões com
  Postgres, mutei cada correção (todas reprovam o teste novo), forcei
  `/dev/full` na saída das âncoras (saída 2) e provoquei falha do Postgres na
  restauração da etapa 8 (a demo sai 1 e diz qual entrada ficou alterada).
  Acrescentei: um MAC de tamanho errado, ou entradas de conta que sumiu, agora
  são **achados com posição** (o `auditar` saía 2 e perdia o resto da
  verificação), e a entrada defeituosa não gera achado de elo a mais.
- Caos com a trilha: ver "Estado atual". O banco de desenvolvimento anterior
  (112 mil transferências sem trilha) continua no volume `liquidacao-pix_postgres`
  e é acusado pelo verificador, como esperado.

### 2026-10-22 — o contrato deixa de poder divergir da API
- `TestAPIRespeitaOContratoOpenAPI` (kin-openapi): requisições reais, Postgres
  real, cada resposta validada contra `docs/openapi.yaml` nos dois sentidos. Para
  provocar 500 e 503 de verdade, um segundo servidor usa um banco já fechado.
  Três mutações confirmam: renomear um campo da API, tirar o 413 do contrato e
  documentar um 429 que a API nunca devolve reprovam o teste, cada uma pelo
  motivo certo.
- O linter só vê o arquivo: `.redocly.yaml` desliga duas regras, cada uma com o
  motivo escrito; `make lint-openapi` (versão fixa) termina com código 0. O
  Redocly procura `redocly.yaml`, não `.redocly.yaml`; sem `--config` explícito
  a configuração era ignorada em silêncio.
- A idempotência, que o OpenAPI não modela, aparece como dois exemplos de 202
  (novo e repetido) e como ponteiro para os testes que a provam.
- ADR 0005: autenticação fora do escopo, de propósito, com o que um sistema real
  exigiria. A lacuna mais séria hoje é que qualquer chamador pode debitar
  qualquer conta.

### 2026-10-22 — demonstração, contrato e apresentação
- `make demo`: 7 passos, sai com código ≠ 0 se algo não se confirmar. Rodou do
  zero e sobre banco já usado. Conciliador ganhou `-uma-vez` (roda sem Go).
- `docs/openapi.yaml` validado com redocly; cruzado com os `type` de erro do
  código, o que achou `chave-desconhecida` fora do contrato. O linter acusou a
  falta de segurança declarada: a API é aberta de propósito, e o contrato diz.
- README com resumo em inglês e uma seção do que o projeto **não** garante.
  Números refeitos (caos rodado de novo) em vez de copiados.
- Revisão cruzada da métrica HTTP achou duas falhas: respostas 1xx faziam o
  cliente receber 200 no lugar do status final, e handler com pânico sumia das
  métricas. Revisão dos alertas achou um ponto cego: com o conciliador caído as
  regras de idade ficavam mudas, e `up == 0` nunca dispara para contêiner parado.

### 2026-10-21/22 — carga, observabilidade e rastreamento
- k6 (pico e sustentada): com 8 consumidores o worker era o gargalo; padrão
  passou a 32. **Taxa sustentada comprovada: 1.000 pedidos/s** (limpa, 3 min);
  acima de ~1.250/s há pausas de 1 a 14 s de causa desconhecida. Hipóteses
  testadas e descartadas: `autoanalyze`; checkpoints sem relação clara.
- Métricas Prometheus (cardinalidade controlada, sem dado sensível), painel
  Grafana, seis alertas, conciliador periódico, índice parcial das pendentes
  (sem custo mensurável).
- Rastreamento OpenTelemetry: 1 trace, 3 serviços, 4 spans por pedido; sem custo
  mensurável a 1.000/s com 100% de amostragem. Um teste meu exigia uma ordem
  entre spans que o desenho não garante; passou localmente e falhou no CI.
- CI: `govulncheck` antigo dava pânico com o Go 1.27 e escondia 3
  vulnerabilidades reais (`x/crypto`, `moby/go-archive`); atualizadas, 0
  alcançáveis.

### 2026-10-21 — teste de caos e duas falhas que ele encontrou
- `tests/caos`: carga + SIGKILL em cada serviço + stream apagado; cliente
  reenvia o mesmo pedido em 5xx/erro de rede; verificação no fim.
- **Falha 1 — varredura de órfãs gerava duplicatas.** A versão inicial
  republicava toda PENDENTE publicada havia mais de 30 s. Com a fila
  atrasada, as que só esperavam eram tratadas como perdidas: 14.417
  republicações numa rodada sem perda real. Correção: a outbox guarda o id
  da mensagem (migração 0004) e o relay pergunta ao Redis (`XRANGE id id`)
  se ela ainda existe. Teste de regressão + mutação.
- **Falha 1b — varredura lenta.** Olhava só as 1.000 primeiras candidatas a
  cada 15 s (~67/s): com 50 mil mensagens perdidas não convergia em 4 min.
  Agora percorre todas em páginas com cursor.
- **Falha 2 — workers presos em NOGROUP.** Apagar o stream apaga o grupo de
  consumo; o `XADD` do relay recria o stream sem grupo, e o grupo só era
  criado na partida do worker. Correção: worker recria o grupo ao receber
  `NOGROUP`. Teste de regressão.
- Dockerfile multi-stage, imagem distroless não-root com todos os programas;
  serviços no compose sob o profile `sistema`.
- `docs/runbook.md`.
- `LOG_TESTE=1` mostra os logs do relay/worker nos testes de fila.

### 2026-10-21 — fila resiliente
- `internal/queue`: Streams, grupo `liquidadores`, chaves
  `transferencias`, `transferencias:mortas`, `transferencias:erros` (último
  erro por mensagem).
- Relay e worker como laços em `internal/usecase`; comandos em `cmd/relay` e
  `cmd/worker`. Erro de Redis/Postgres não derruba nenhum dos dois: esperam
  (até 5 s) e tentam de novo.
- Worker liquida com contexto próprio (10 s), desligado do SIGTERM: o
  encerramento não desfaz uma liquidação no meio.
- Testes com Redis real: fluxo completo (300 transferências, 3 workers),
  mensagem repetida 4×, worker que morre sem confirmar, falha persistente →
  mortas (com checagem de que houve espera), Redis fora do ar, stream apagado.
- Mutação: com a varredura de órfãs desligada, o teste do stream apagado
  fica com 1 pendente para sempre.
- Ponta a ponta manual com os binários: aceita → liquidada em ~9 ms.
- Observação: durante a fase 2, uma revisão do código corrigiu um estouro em
  `Centavos.String` (`math.MinInt64`) e acrescentou fuzz tests ao domínio;
  essas mudanças entraram no commit da API. O gosec acusou as conversões int64→uint64; foram
  anotadas, sem mudar o comportamento.

### 2026-10-21 — API de ingestão
- `POST /transferencias`: chaves PIX → ids, validação, transferência + outbox
  na mesma transação, 202 com `Location`. Repetição idêntica devolve o mesmo
  corpo com `Idempotent-Replayed: true`; mesma chave com outro conteúdo → 409.
- Teste com 20 pedidos simultâneos iguais: um id, uma linha na outbox.
- Furo encontrado e fechado durante a escrita: a chave `liquidez@sistema`
  era aceita como pagador, e a conta de liquidez pode ficar negativa, ou
  seja, qualquer cliente criaria dinheiro. `IDDeContaCliente` ignora contas
  técnicas; há teste.
- Prazos no `http.Server` (sem eles um cliente lento segura a conexão) e
  encerramento gracioso de 15 s no SIGTERM.
- Status de erro: 400 JSON malformado/campo desconhecido, 413 corpo > 4 KiB,
  422 chave desconhecida ou regra de domínio, 409 conflito.

### 2026-10-21 — liquidação, conciliação e teste de concorrência
- `usecase.Liquidar`: trava a transferência, depois as contas em ordem
  crescente de id, decide (`domain.Decidir`), grava lançamentos + saldo e
  finaliza. Transferência já finalizada → devolve o gravado sem mexer.
- `repository.Conciliar`: soma global zero, saldo = soma dos lançamentos,
  liquidada tem débito + crédito do valor certo; tudo numa fotografia
  `REPEATABLE READ`.
- `TestConcorrenciaConservaDinheiro`: 4.000 transferências entre 12 contas,
  cada uma liquidada 2× em paralelo por 32 goroutines; ~5 s. Resultado com a
  semente 42: 3.494 liquidadas, 506 rejeitadas.
- **Teste de mutação** (para provar que o teste pega o erro):
  - sem ordenar as travas → 189 deadlocks (40P01) e falha. Antes do prazo de
    60 s no teste, essa regressão só deixava o teste lento (10 min) em vez de
    falhar; por isso o prazo existe.
  - sem a checagem de status final → a restrição
    `UNIQUE (transferencia_id, conta_id)` recusa a segunda liquidação.

### 2026-10-21 — especificação e roteiro
- Ideia original revisada: a tabela "O que mudou" da especificação lista cada
  problema encontrado e a correção.
