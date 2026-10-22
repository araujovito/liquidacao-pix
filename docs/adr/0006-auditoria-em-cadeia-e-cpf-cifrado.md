# ADR 0006 — Auditoria em cadeia de HMAC e CPF cifrado por conta

**Status:** implementada em 2026-10-22; **revisão e auditoria independentes
pendentes** (`REVISAO_PENDENTE`). Risco **crítico** (HMAC, AES-GCM e gestão de
chaves): a fase só se considera concluída depois delas. A seção "O que a
implementação mudou no desenho" registra onde o código se afastou do texto
original.

## Contexto
A especificação promete duas coisas na fase 7:

1. alterar ou apagar uma linha antiga da trilha de auditoria é **detectado**;
2. o CPF do titular é gravado cifrado e o apagamento (LGPD art. 18, GDPR art.
   17) se faz descartando a chave, sem tocar no histórico financeiro.

Hoje o banco já recusa `UPDATE`/`DELETE`/`TRUNCATE` em `lancamentos` por gatilho.
Isso não basta como auditoria: quem tem acesso de superusuário ao Postgres
desliga o gatilho, edita a linha e religa. A trilha precisa denunciar a
adulteração **mesmo quando o atacante controla o banco**, e para isso o segredo
tem de ficar fora dele.

## Decisão 1 — O que se audita
Uma entrada por **decisão de liquidação** (`LIQUIDADA` ou `REJEITADA`), gravada na
mesma transação que finaliza a transferência. Se a decisão não foi confirmada, a
entrada também não existe.

Não se audita o aceite do pedido: ele não trava contas, e a transferência
`PENDENTE` já é um registro do pedido. Também não existe "quem pediu", porque a
API não tem identidade (ADR 0005); a trilha prova **o que o sistema decidiu e
que isso não foi alterado depois**, não quem solicitou.

## Decisão 2 — Uma cadeia por conta, não uma global
Cada transferência gera **uma entrada na cadeia de cada conta envolvida**
(pagador e recebedor), como os lançamentos já fazem.

- **Por quê não global:** uma cadeia única exige que cada entrada conheça o MAC
  da anterior, e isso serializa todas as liquidações numa trava. A carga
  sustentada comprovada é de 1.000 pedidos/s; uma trava global é o tipo de
  mudança que a derrubaria sem aparecer em nenhum teste de correção.
- **Por quê por conta funciona:** o worker já trava as duas contas, em ordem
  crescente de id (ADR 0002), antes de decidir. A cabeça da cadeia de cada conta
  é lida e atualizada sob essa mesma trava. Não aparece trava nova, portanto não
  aparece ordem nova de trava nem risco novo de *deadlock*.
- **Custo aceito:** não há ordem total entre contas distintas. A trilha prova a
  ordem de cada conta; a ordem entre contas vem do `finalizada_em` da
  transferência, que entra no conteúdo protegido.

## Decisão 3 — Formato da entrada
Tabela `auditoria` (migração própria), só de inserção, com os mesmos gatilhos de
`lancamentos` contra `UPDATE`, `DELETE` e `TRUNCATE`:

| Coluna | Conteúdo |
|---|---|
| `conta_id`, `seq` | posição na cadeia da conta; `seq` começa em 1 e não tem buracos |
| `transferencia_id`, `status`, `motivo` | a decisão |
| `pagador_id`, `recebedor_id`, `valor_centavos` | o que foi decidido |
| `decidida_em` | instante da decisão, em microssegundos, vindo da aplicação |
| `chave_id` | qual chave do chaveiro assinou |
| `mac_anterior`, `mac` | elo e assinatura (32 bytes) |

`mac = HMAC-SHA256(chave[chave_id], codificação)`. A codificação é **canônica e
com tamanho prefixado em cada campo**, começando pelo nome e versão do domínio
(`"pix-auditoria/v1"`). Assina, nesta ordem: `conta_id`, `seq`,
`transferencia_id`, `status`, presença e valor de `motivo`, `pagador_id`,
`recebedor_id`, `valor_centavos`, `decidida_em`, `chave_id` e `mac_anterior`.
Inteiros usam complemento de dois em 8 bytes, *big-endian*; UUID e MAC usam os
bytes crus; texto usa UTF-8; instante usa microssegundos desde a época UTC; um
campo opcional começa por um byte de presença. Assim `conta_id` impede copiar
um elo válido para a outra cadeia da mesma transferência, `seq` liga o MAC à
posição e `chave_id` não pode ser trocado sem invalidá-lo. Concatenar texto
separado por delimitador permitiria que dois conteúdos diferentes gerassem os
mesmos bytes; o formato explícito fecha isso, e vetores de teste fixos impedem
que uma refatoração mude a representação em silêncio.

A primeira entrada de uma conta usa `mac_anterior` = 32 bytes zero. A cabeça de
cada cadeia (`seq` e `mac` da última entrada) fica em `contas`, atualizada sob a
trava da conta.

## Decisão 4 — Chave fora do banco, com rotação
O chaveiro vem do ambiente, nunca do Postgres:
`AUDITORIA_CHAVES="v1:<hex>,v2:<hex>"` e `AUDITORIA_CHAVE_ATUAL=v2`. Cada chave
tem 32 bytes. O código não tem chave padrão: sem a variável, o worker não sobe
(mesma regra que já vale para `DATABASE_URL`).

A rotação não quebra a cadeia: cada entrada registra o `chave_id` que a assinou
e o MAC da anterior entra no conteúdo, então uma chave nova passa a assinar
daqui para frente e as antigas continuam servindo para verificar o passado. Uma
chave aposentada só pode ser descartada depois que se aceita não verificar mais
as entradas que ela assinou.

## Decisão 5 — O que o verificador prova e o que não prova
`decidida_em` é produzido uma vez pela aplicação, truncado para microssegundos,
e o mesmo valor é passado ao `UPDATE transferencias ... finalizada_em` e às duas
entradas de auditoria. O verificador exige igualdade com `finalizada_em`; não
pode haver um relógio do banco e outro da aplicação representando a mesma
decisão.

`cmd/auditar` percorre as cadeias, recalcula cada MAC e confere o elo, a
sequência, a correspondência com a transferência e a cabeça gravada em
`contas`. Detecta:

| Adulteração | Como aparece |
|---|---|
| Alterar qualquer campo | MAC recalculado difere |
| Apagar uma entrada do meio | `seq` com buraco e elo que não fecha |
| Reordenar entradas | elo que não fecha |
| Inserir entrada falsa | sem a chave não há MAC válido |
| Recalcular tudo | exige a chave, que não está no banco |
| Decisão final sem entrada na cadeia | a transferência existe e a conta não tem entrada para ela |
| Entrada que não bate com a transferência | status, motivo, partes, valor ou instante diferentes |

**Limite que o projeto declara:** apagar as **últimas** entradas de uma conta
*e* voltar a cabeça em `contas` para o valor anterior deixa uma cadeia que se
verifica. A cadeia não tem como saber que havia mais. A verificação da
correspondência com `transferencias` estreita isso: para uma transferência
`LIQUIDADA`, os lançamentos são imutáveis e uma chave estrangeira impede apagar
a transferência, então a poda deixa uma "decisão sem entrada" e é acusada mesmo
sem âncora. O que sobra são as `REJEITADAS` (sem lançamentos), cujas entradas
somem junto com a transferência. A defesa é uma âncora fora
do banco: `cmd/auditar -ancora` imprime `(conta, seq, mac)` das cabeças. Ao usar
`-conferir-ancora`, o verificador exige não só `cabeça.seq >= âncora.seq`, mas
que o elo na posição exata `âncora.seq` ainda tenha o mesmo MAC. Comparar apenas
a sequência seria insuficiente: depois de uma poda, eventos legítimos poderiam
fazê-la alcançar novamente o número antigo. Em produção a âncora iria para
armazenamento autenticado de escrita única (WORM) ou para um carimbo de tempo de
terceiro. Aqui ela é um arquivo que o teste trata como confiável; quem puder
alterar banco **e** arquivo pode esconder a poda. O teste prova que a poda do
rabo é detectada mesmo depois de novas entradas quando existe âncora anterior,
e que não é detectada quando não existe.

## Decisão 6 — CPF cifrado, chave por conta, apagamento por descarte
- O modelo atual não tem entidade `titulares`: cada conta de cliente representa
  um titular cadastrado. Portanto a granularidade real da fase 7 é **uma chave
  por conta**. Se o produto passar a permitir várias contas para a mesma pessoa,
  será necessária uma entidade `titulares` compartilhada para que um único
  pedido apague todas elas; não se deve inferir identidade comparando nomes.
- `contas.cpf_cifrado` guarda `nonce(12) || texto cifrado || tag`, com AES-256-GCM.
- Cada conta tem sua chave de dados (32 bytes aleatórios) em tabela separada,
  `chaves_cpf`, **embrulhada** por uma chave-mestra do ambiente também com
  AES-GCM. A linha guarda `mestra_id`, nonce aleatório e chave embrulhada; o
  chaveiro `CPF_CHAVES_MESTRAS="v1:<hex>,v2:<hex>"` e
  `CPF_CHAVE_MESTRA_ATUAL=v2` permite rotação sem reusar nonce nem tornar chaves
  antigas indecifráveis. O embrulho usa como AAD o domínio
  `"pix-chave-cpf/v1"`, `conta_id` e `mestra_id`; trocar chaves embrulhadas entre
  contas ou trocar seu id de mestra falha na autenticação. Um vazamento só do
  banco entrega CPF cifrado e chaves embrulhadas, nenhuma legível.
- Nonce aleatório de 96 bits por cifragem. Com uma chave por conta e uma ou
  poucas cifragens por chave, a margem do limite de colisão de nonce no GCM
  fica enorme.
- O CPF usa como AAD o domínio `"pix-cpf/v1"` e o id da conta: um texto cifrado
  copiado para outra conta ou confundido com outro tipo de cifra não decifra.
- **Apagamento:** `DELETE` em `chaves_cpf` e `cpf_apagado_em` preenchido. O
  texto cifrado fica no banco e passa a ser indecifrável. A auditoria e os
  lançamentos só referenciam `conta_id`, nunca CPF, então continuam íntegros e
  verificáveis depois do apagamento. Há teste que apaga, verifica a cadeia e
  confere que a decifragem falha de forma distinguível ("apagado", não "erro").
- **Não existe busca por CPF.** Um índice cego (HMAC do CPF) permitiria achar o
  titular, mas seria dado pessoal derivado que sobrevive à destruição da chave.
  O custo é aceito: a conta se acha pela chave PIX.

## O que o crypto-shredding não resolve
- **Cópias da chave:** se um backup do banco contém `chaves_cpf`, o
  apagamento só é efetivo quando esse backup expirar ou for tratado à parte.
- **Chave-mestra:** descartar a chave do titular não ajuda se a chave-mestra e
  uma cópia antiga do banco caírem juntas. Num sistema real a mestra ficaria
  num KMS/HSM, com rotação e acesso auditado.
- **CPF em outros lugares:** logs, métricas e rastros já são proibidos de levar
  dado pessoal (revisão do ciclo 2); o teste de vazamento passa a incluir o CPF.

## Consequências
- Liquidar passa a gravar duas entradas a mais por transferência, na transação
  existente. Medido a 1.000 pedidos/s (uma rodada antes, uma depois): a vazão não
  mudou e a mediana subiu pouco (API p50 +0,2 ms; atraso até liquidar p50 +2,5 ms),
  mas a cauda piorou e **não foi possível atribuí-la à cadeia** com tão poucas
  rodadas (`docs/carga.md`). A decisão de não usar cadeia global não depende desse
  número; a de aceitar o custo, sim, e fica provisória até haver mais rodadas.
- `cmd/worker` ganha dependência do chaveiro; `cmd/semear` e `cmd/auditar` também.
- O `make demo` ganha dois passos: adulterar uma entrada com os gatilhos
  desligados e ver o verificador acusar; cadastrar e apagar um CPF e ver a
  trilha intacta.
- Um banco criado antes da fase 7 não tem trilha para o que já foi decidido, e o
  verificador o acusa de propósito (decisão sem entrada). Deve ser recriado.

## O que a implementação mudou no desenho
- **Decisão sem entrada** (acima): verificação que o texto original não tinha e
  que reduziu o limite declarado.
- **`decidida_em` nunca antes de `criada_em`:** o instante vem da aplicação; se o
  relógio dela estiver atrás do do banco, usa-se `criada_em`, para não violar a
  restrição `finalizacao_nao_antecede_criacao`.
- **Liquidar sem chaveiro falha** (`ErrSemChaveiro`), em vez de finalizar sem
  trilha. Só o worker e o `semear` recebem a chave no Compose; API, relay e
  conciliador não.
- **O nonce do embrulho tem coluna própria** (`chaves_cpf.nonce_embrulho`).
- **`cmd/titular` lê o CPF da entrada padrão** e só mostra o CPF mascarado;
  `semear` não cadastra CPF.
- **Recadastrar depois de apagar é recusado** (`ErrCPFJaCadastrado`). Um novo
  consentimento exigiria uma decisão de produto que não está no escopo.
- **`cmd/auditar` limita a 50 as linhas impressas** e resume por tipo.

## Alternativas descartadas
- **Cadeia global:** serializa a liquidação (ver Decisão 2).
- **Hash simples (SHA-256) sem chave:** quem altera uma linha recalcula toda a
  cadeia seguinte. Detecta erro, não adulteração.
- **Assinatura assimétrica (Ed25519):** permitiria verificar sem poder assinar,
  que é melhor para auditor externo. Fica como evolução; HMAC é o que a
  especificação pede e o verificador roda no mesmo ambiente de confiança.
- **Cifrar a trilha inteira:** esconde, não impede adulteração (já descartado na
  especificação). Cifra-se o dado pessoal, assina-se a trilha.
- **Uma única chave para todos os CPFs:** descartá-la apagaria todos os titulares
  ou nenhum; o apagamento individual exige chave individual.
