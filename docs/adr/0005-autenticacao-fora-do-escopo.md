# ADR 0005 — Autenticação e autorização ficam fora do escopo, de propósito

**Status:** aceita (2026-10-22)

## Contexto
O projeto é um simulador para estudar consistência, idempotência e resiliência
de um motor de liquidação. A API não tem autenticação: qualquer chamador pode
enviar um pedido, e **qualquer chamador pode usar como pagador qualquer conta de
cliente**. O contrato declara isso (`security: []`), e o linter de OpenAPI o
acusava como erro enquanto não estava declarado.

## Decisão
Não implementar autenticação nem autorização neste projeto, e deixar isso
explícito: no contrato (`security: []`), no README ("o que não garante") e aqui.

O motivo é de escopo, não de descuido. As garantias que o projeto quer provar
(nenhum pedido aceito se perde, nenhum é liquidado duas vezes, o dinheiro é
conservado) não dependem de quem chama, e uma autenticação feita pela metade
(uma chave estática num cabeçalho, por exemplo) daria falsa sensação de
segurança sem provar nada.

## O que um sistema real exigiria
- **Autenticação forte entre participantes:** mTLS entre as instituições e OAuth 2.0
  com credenciais de cliente (*client credentials*) para o acesso à API.
- **Autorização por conta:** o participante só pode debitar contas que são dele. É a
  lacuna mais séria hoje: o `pagador` vem no corpo e ninguém confere.
- **Idempotência por cliente:** o `end_to_end_id` deixa de ser único no sistema e
  passa a ser único **por participante** (`UNIQUE (participante_id, end_to_end_id)`).
  Hoje um participante poderia, por acidente ou de propósito, colidir com a chave
  de outro e receber a transferência dele de volta no `202`.
- **Limite de taxa** por participante, para um cliente com defeito não consumir a
  capacidade dos demais (a carga sustentada comprovada é de 1.000 pedidos/s).
- **Trilha de auditoria** de quem pediu o quê (fase 7 do roteiro).

## O que mudaria no código
Um *middleware* extrai o participante do certificado ou do token e o coloca no
contexto; `Receptor.Aceitar` confere que a conta pagadora pertence a ele; uma
migração acrescenta `participante_id` às contas e à transferência e troca o
`UNIQUE` do `end_to_end_id`. O `GET /transferencias/{id}` passa a devolver 404
para transferência de outro participante, para não revelar que ela existe.

## Consequências
- O contrato e o README dizem a verdade: quem lê sabe que a API é aberta.
- Nenhum dado real deve passar por este sistema.
- O teste de contrato continua válido: ele valida formato e status, não identidade.

## Alternativas descartadas
- **Chave de API estática no cabeçalho:** parece segurança e não é; sem rotação,
  escopo por conta nem auditoria, só esconde a lacuna.
- **Implementar mTLS e OAuth no projeto:** é um projeto à parte e desviaria o foco
  do que se quer demonstrar.
