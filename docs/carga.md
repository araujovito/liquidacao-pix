# Testes de carga

Como o sistema se comporta sob carga, medido com k6 contra os programas
rodando em contêineres. Os relatórios de cada rodada ficam em
[`tests/carga/resultados/`](../tests/carga/resultados/).

## Como rodar

```sh
make carga-pico                     # sobe até 4.000 pedidos/s (PICO=…)
make carga-sustentada               # 1.500 pedidos/s por 3 min (TAXA=…, DURACAO=…)
WORKERS=8 make carga-sustentada     # muda os consumidores do worker
```

O script [`tests/carga/rodar.sh`](../tests/carga/rodar.sh) sobe o sistema
(`make sistema`), semeia 1.000 contas, espera a fila de rodadas anteriores
esvaziar, roda o k6 (imagem `grafana/k6:1.3.0`, sem instalar nada), espera a
fila desta rodada esvaziar, roda a conciliação e escreve o relatório.

## O que é medido

| Fonte | Medida |
|---|---|
| k6 | pedidos, erros (contagem), pedidos que o k6 deixou de enviar, latência da API (p50, p95, p99, máx.) |
| Banco | vazão de aceite e de liquidação por segundo (mediana e máxima), atraso entre aceitar e liquidar (p50, p95, p99, máx.), fila ao fim da carga, tempo para esvaziar |
| Conciliação | invariantes do livro-razão depois da carga |

- **Só a fase de medição conta.** Cada cenário começa com 15 s de aquecimento
  a 200 pedidos/s. O `end_to_end_id` carrega a fase (`a` ou `m`), e tanto o
  k6 (sub-métricas por cenário) quanto as consultas no banco filtram por ela.
- **"Descartados pelo k6"** são pedidos que o k6 não enviou porque todos os
  usuários virtuais estavam ocupados esperando resposta. Diferente de zero
  significa que a taxa planejada **não** foi atingida, e a latência real
  percebida seria pior que a medida.
- Na carga sustentada, qualquer erro ou descarte reprova a rodada. O relatório
  ainda é escrito para diagnóstico, mas o comando termina com código diferente
  de zero. No pico, saturação e descartes são parte do objeto da medição.
- Uma divergência de conciliação também reprova a execução. A mensagem fica no
  relatório para investigação.
- O k6 não reenvia pedidos com erro; um cliente real reenviaria (o teste de
  caos cobre esse comportamento).

## Máquina

Notebook AMD Ryzen 7 7735HS (16 threads), 13 GB de RAM, Docker 29.9.1. k6,
API, relay, worker, Postgres e Redis rodam **todos na mesma máquina**,
disputando a mesma CPU: os números servem para comparar versões e
configurações entre si, não como capacidade de produção.

## Resultados (2026-10-21)

### Carga sustentada, 1.500 pedidos/s por 3 min

*Estas duas rodadas tinham descartes e hoje seriam reprovadas pelos critérios
da revisão; valem para comparar 8 com 32 consumidores, não como capacidade.*

| Consumidores no worker | Liquidação (mediana) | Fila ao fim | Atraso p50 / p95 | API p50 / p95 / p99 | Descartados |
|---|---|---|---|---|---|
| 8 | 1.410/s | 38.802 | 418 ms / 25,7 s | 5,0 / 772 / 1.470 ms | 3.303 |
| 32 | 1.501/s | 0 | 25 ms / 386 ms | 4,1 / 238 / 841 ms | 1.877 |

Relatórios: `sustentada-2026-10-21-2144.md` (8) e `sustentada-2026-10-21-2149.md` (32).

### Pico, até 4.000 pedidos/s

| Consumidores no worker | Aceite (mediana / máx.) | Liquidação (mediana) | Fila ao fim | Atraso p50 | API p50 / p99 | Descartados | Erros |
|---|---|---|---|---|---|---|---|
| 8 | 2.862 / 3.886/s | 1.548/s | 43.325 | 17,1 s | 783 / 1.111 ms | 17.483 | 0 |
| 32 | 2.550 / 2.843/s | 2.225/s | 650 | 2,4 s | 1.089 / 1.340 ms | 31.670 | 1 |

Relatórios: `pico-2026-10-21-2142.md` (8) e `pico-2026-10-21-2201.md` (32).

Em todas as rodadas a conciliação fechou sem divergências.

## O que os números mostram

1. **Com 8 consumidores, o worker era o gargalo.** Liquidava ~1.400/s, menos
   que os 1.500/s de entrada, e a fila crescia sem parar (38.802 ao fim de
   3 min; atraso p95 de 25 s). Cada liquidação faz ~6 idas ao banco e um
   commit, uns 5 ms, quase todo esperando o Postgres: 8 consumidores × ~200/s
   ≈ 1.600/s, o que bate com o medido. Com 32, o worker acompanha a entrada e
   a fila termina vazia. **O padrão passou a ser 32.**
2. **No pico, API e worker disputam o mesmo Postgres.** Com 32 consumidores,
   a liquidação sobe (1.548 → 2.225/s) e o atraso cai (17 s → 2,4 s), mas o
   aceite da API cai (2.862 → 2.550/s) e o k6 descarta mais pedidos. O teto do
   conjunto, nesta máquina, fica em torno de 2.500–2.900 pedidos/s.
3. **A API tem uma cauda intermitente.** Na carga sustentada o p50 fica em
   3–5 ms, mas algumas rodadas têm p99 acima de 800 ms; outras, com a mesma
   configuração, ficam abaixo de 50 ms (ver abaixo). Ainda sem causa
   confirmada; a investigação está em "Rodada limpa" (no fim).

## Impacto das validações do commit `d1fa55f`

O commit `d1fa55f` acrescentou CHECKs em `transferencias` (migração 0005), a
validação do `Content-Type` e a recusa de conteúdo depois do JSON. Para medir
o custo, a carga sustentada (1.500/s, 1 min, 32 consumidores) rodou duas vezes
em banco novo **antes** (`7ff7b4b`) e **depois** (`d1fa55f`):

| Versão | Rodada | API p50 | p95 | p99 | Descartados |
|---|---|---|---|---|---|
| antes | 1ª (banco novo) | 3,3 ms | 827 ms | 1.181 ms | 1.994 |
| antes | 2ª | 3,3 ms | 29 ms | 42 ms | 0 |
| depois | 1ª (banco novo) | 5,4 ms | 37 ms | 49 ms | 0 |
| depois | 2ª | 3,1 ms | 8 ms | 18 ms | 0 |

A variação entre rodadas da mesma versão é muito maior que qualquer diferença
entre as versões: **não há custo mensurável** das validações nesta escala. As
rodadas "depois" estão em `sustentada-2026-10-21-2157.md` e `…-2159.md`; as
"antes" rodaram numa cópia do repositório naquele commit e não geraram
relatório aqui.

## Limitações

- Uma rodada por configuração nos cenários de 3 min; a cauda intermitente
  (item 3) mostra que isso não basta para afirmar diferenças de p95/p99.
- Tudo na mesma máquina: o k6 também consome CPU.
- O pool de conexões do Postgres é o padrão do pgx (máx(4, CPUs) = 16 por
  processo). Com 32 consumidores, metade espera conexão; o ganho veio mesmo
  assim, mas o tamanho do pool não foi estudado.
- `synchronous_commit` e demais parâmetros do Postgres estão no padrão.

## Pontos em aberto

```text
Entrega: testes de carga (ciclo 1)
Propriedade: números reproduzíveis de vazão, latência e atraso de
  liquidação, só da fase de medição, com conciliação conferida ao fim.
Validação: make carga-pico (8 e 32 consumidores), make carga-sustentada
  (8 e 32), 4 rodadas antes/depois de d1fa55f; conciliação fechada em todas.
Riscos restantes: cauda intermitente da API sem causa; uma rodada por
  configuração; pool de conexões não estudado.
Revisar especialmente:
  - a causa da cauda: autoanalyze foi refutada e checkpoints não se alinham
    (ver "Rodada limpa"); restam disputa de CPU/IO, pool de conexões e
    tamanho do banco;
  - se o filtro por fase (end_to_end_id) exclui de fato o aquecimento;
  - o 1 erro do pico com 32 consumidores (o k6 não guardou o motivo: 5xx
    ou tempo esgotado?);
  - se 32 consumidores é um bom padrão fora desta máquina.
```

## Revisão adversarial

A revisão do ciclo 1 encontrou três riscos no executor, corrigidos depois das
primeiras medições:

- o teste podia começar mesmo com backlog antigo após esgotar a espera de dois
  minutos; agora cancela a rodada;
- a conciliação usava `|| true`, registrava a divergência no relatório, mas
  ainda terminava com sucesso; agora uma divergência reprova a execução;
- erros de HTTP e falhas de transporte apareciam apenas como total agregado;
  novas rodadas também registram as duas categorias separadamente.

A carga sustentada passou a exigir zero erro, zero descarte e 100% de respostas
202. Portanto, o resultado de três minutos com 1.877 descartes continua útil
para diagnóstico, mas não deve ser descrito como capacidade sustentada
comprovada. É necessária uma nova rodada limpa para definir a maior taxa que
atende esses critérios.

## Rodada limpa: qual taxa sustentada passa nos critérios?

Resposta à pendência da revisão: carga sustentada de 3 min, 32 consumidores,
critérios do script (zero erro, zero descarte, 100% de 202, conciliação
fechada), banco reaproveitado entre as rodadas.

| Taxa planejada | Rodada | Descartados | API p50 / p95 / p99 | Passou? |
|---|---|---|---|---|
| 1.000/s | `…-2211` | 0 | 3,2 / 7,7 / 19,7 ms | **sim** |
| 1.250/s | `…-2219` | 2.509 | 4,1 / 756 / 1.607 ms | não |
| 1.250/s | `…-2223` | 2.024 | 4,1 / 603 / 1.221 ms | não |
| 1.250/s | `…-2228` (autovacuum desligado) | 1.848 | 3,8 / 79 / 1.210 ms | não |
| 1.500/s | `…-2215` | 926 | 4,4 / 38 / 331 ms | não |

**Maior taxa sustentada comprovada nesta máquina: 1.000 pedidos/s.** É uma
rodada limpa; acima disso nenhuma passou. A rodada de 1.500/s descartou
menos que as de 1.250/s, então o limite não é uma função simples da taxa: o
que reprova é a presença de pausas, não a média.

### O que são as pausas

Olhando a vazão de aceite segundo a segundo, as rodadas reprovadas têm
pausas de 1 a ~14 s em que a API aceita 400–900/s em vez da taxa planejada,
seguidas de um pico de recuperação. O k6 descarta pedidos durante a pausa
porque os usuários virtuais ficam presos esperando resposta.

Hipóteses testadas:

- **`autoanalyze` do Postgres: refutada.** Numa rodada, o `autoanalyze` de
  `lancamentos` terminou exatamente quando uma pausa de 14 s acabou. Com
  `autovacuum=off` (só para o experimento), as pausas continuaram.
- **Checkpoints: sem relação clara.** Com `log_checkpoints=on` (agora ligado
  no compose), os checkpoints são disparados por volume de WAL
  (`max_wal_size` padrão de 1 GB) e duram ~65 s. Uma pausa começou 19 s depois
  de um checkpoint, outra coincidiu com o fim de um, e um checkpoint inteiro
  passou sem pausa.
- **Ainda abertas:** disputa de CPU/IO com outros processos da máquina (o
  notebook rodava o Docker do Supabase e outras ferramentas durante os
  testes; a carga média ficou entre 5 e 8), tamanho do pool de conexões da
  API e crescimento do banco entre rodadas (chegou a 2 milhões de
  transferências e 4 milhões de lançamentos).

Conclusão honesta: o limite de 1.000/s vale **para esta máquina nas condições
das rodadas**; a causa das pausas não foi encontrada. Uma medição melhor
exigiria isolar a máquina e usar banco novo a cada rodada, o que o
`rodar.sh` ainda não faz.

