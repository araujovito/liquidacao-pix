# ADR 0006 — Auditoria em cadeia de HMAC por conta e CPF com chave por titular

**Status:** proposta (2026-10-22). Risco **crítico** (HMAC, AES-GCM e gestão de
chaves): a implementação só se considera concluída depois da revisão e da
auditoria previstas em `docs/colaboracao-claude-codex.md`.

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
com tamanho prefixado em cada campo**, começando por uma versão do formato e
pelo nome do domínio (`"pix-auditoria/v1"`). Concatenar texto separado por
delimitador permite que dois conteúdos diferentes gerem os mesmos bytes; o
prefixo de tamanho fecha isso, e há teste para esse caso.

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
`cmd/auditar` percorre as cadeias, recalcula cada MAC e confere o elo, a
sequência e a cabeça gravada em `contas`. Detecta:

| Adulteração | Como aparece |
|---|---|
| Alterar qualquer campo | MAC recalculado difere |
| Apagar uma entrada do meio | `seq` com buraco e elo que não fecha |
| Reordenar entradas | elo que não fecha |
| Inserir entrada falsa | sem a chave não há MAC válido |
| Recalcular tudo | exige a chave, que não está no banco |

**Limite que o projeto declara:** apagar as **últimas** entradas de uma conta
*e* voltar a cabeça em `contas` para o valor anterior deixa uma cadeia que se
verifica. A cadeia não tem como saber que havia mais. A defesa é uma âncora fora
do banco: `cmd/auditar -ancora` imprime `(conta, seq, mac)` das cabeças, e a
execução seguinte, com `-conferir-ancora`, falha se alguma cabeça recuou. Em
produção essa âncora iria para armazenamento de escrita única (WORM) ou para um
carimbo de tempo de terceiro; aqui ela é um arquivo, e o teste prova que a
poda do rabo é detectada **quando existe âncora anterior** e que não é
detectada quando não existe.

## Decisão 6 — CPF cifrado, chave por titular, apagamento por descarte
- `contas.cpf_cifrado` guarda `nonce(12) || texto cifrado || tag`, com AES-256-GCM.
- Cada titular tem sua chave de dados (32 bytes aleatórios) em tabela separada,
  `chaves_titular`, **embrulhada** por uma chave-mestra do ambiente
  (`CPF_CHAVE_MESTRA`) também com AES-GCM. Um vazamento só do banco entrega CPF
  cifrado e chaves embrulhadas, nenhuma legível.
- Nonce aleatório de 96 bits por cifragem. Com uma chave por titular e uma ou
  poucas cifragens por chave, a margem do limite de colisão de nonce no GCM
  fica enorme.
- Dado autenticado adicional (AAD) = id da conta: um CPF cifrado copiado para
  outra conta não decifra.
- **Apagamento:** `DELETE` em `chaves_titular` e `cpf_apagado_em` preenchido. O
  texto cifrado fica no banco e passa a ser indecifrável. A auditoria e os
  lançamentos só referenciam `conta_id`, nunca CPF, então continuam íntegros e
  verificáveis depois do apagamento. Há teste que apaga, verifica a cadeia e
  confere que a decifragem falha de forma distinguível ("apagado", não "erro").
- **Não existe busca por CPF.** Um índice cego (HMAC do CPF) permitiria achar o
  titular, mas seria dado pessoal derivado que sobrevive à destruição da chave.
  O custo é aceito: a conta se acha pela chave PIX.

## O que o crypto-shredding não resolve
- **Cópias da chave:** se um backup do banco contém `chaves_titular`, o
  apagamento só é efetivo quando esse backup expirar ou for tratado à parte.
- **Chave-mestra:** descartar a chave do titular não ajuda se a chave-mestra e
  uma cópia antiga do banco caírem juntas. Num sistema real a mestra ficaria
  num KMS/HSM, com rotação e acesso auditado.
- **CPF em outros lugares:** logs, métricas e rastros já são proibidos de levar
  dado pessoal (revisão do ciclo 2); o teste de vazamento passa a incluir o CPF.

## Consequências
- Liquidar passa a gravar duas entradas a mais por transferência, na transação
  existente. O custo precisa ser **medido** a 1.000 pedidos/s e registrado, não
  suposto; se for relevante, o ADR é revisto.
- `cmd/worker` ganha dependência do chaveiro; `cmd/semear` e `cmd/auditar` também.
- O `make demo` ganha um passo: adulterar uma entrada com o gatilho desligado e
  ver o verificador acusar.

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
