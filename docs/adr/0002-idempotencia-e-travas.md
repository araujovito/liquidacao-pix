# ADR 0002 — Idempotência no consumo e travas em ordem fixa

**Status:** aceita (2026-10-21)

## Contexto
Filas entregam "pelo menos uma vez": a mesma transferência pode chegar ao
worker duas vezes, às vezes ao mesmo tempo em dois workers. E transferências
cruzadas (A→B e B→A) disputam as mesmas linhas.

## Decisão
1. A liquidação trava a transferência com `FOR UPDATE` antes de tudo. A segunda
   entrega espera a primeira terminar e encontra o status final; não faz nada.
2. As contas são travadas uma a uma, **em ordem crescente de id**. Assim duas
   transações nunca esperam uma pela outra em ciclo (*deadlock*).
3. Isolamento `READ COMMITTED`: as travas de linha bastam. `SERIALIZABLE`
   abortaria transações em conflito e exigiria repetir, sem ganho aqui.
4. Duas redes de segurança no banco, para o caso de o código errar:
   `UNIQUE (transferencia_id, conta_id)` em `lancamentos` e
   `CHECK (saldo >= 0)` nas contas de cliente.

## Como foi verificado
`TestConcorrenciaConservaDinheiro`: 4.000 transferências entre 12 contas, cada
uma liquidada duas vezes em paralelo por 32 goroutines. Passa sem erro e com a
conciliação fechada. Teste de mutação (ver histórico da memória técnica): sem a
ordenação das travas, o teste falha com deadlocks; sem a checagem de status,
falha na restrição única.
