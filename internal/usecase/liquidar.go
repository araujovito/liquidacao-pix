// Package usecase orquestra domínio e repositório. As regras de decisão
// ficam no domain; aqui ficam a ordem das operações e as garantias.
package usecase

import (
	"context"
	"fmt"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Liquidador liquida transferências pendentes.
type Liquidador struct {
	Repo *repository.Repositorio

	// AoFinalizar, se definido, é chamado depois do commit, uma vez por
	// transferência que ACABOU de chegar ao status final. Entregas repetidas
	// de uma transferência já final não o chamam, então contadores e o atraso
	// aceite→liquidação não contam em dobro.
	AoFinalizar func(domain.Transferencia)
}

// Liquidar leva uma transferência PENDENTE ao status final.
//
// É idempotente: chamar de novo com uma transferência já finalizada devolve o
// resultado gravado sem tocar em nada. Isso é o que permite à fila entregar a
// mesma mensagem mais de uma vez.
func (l Liquidador) Liquidar(ctx context.Context, id string) (domain.Transferencia, error) {
	var final domain.Transferencia
	var nova bool
	err := l.Repo.NaTransacao(ctx, func(tx repository.TxLiquidacao) error {
		nova = false // a função pode rodar mais de uma vez se a transação for repetida
		t, err := tx.TravarTransferencia(ctx, id)
		if err != nil {
			return fmt.Errorf("transferência %s: %w", id, err)
		}
		if t.Status.Final() {
			final = t
			return nil
		}

		contas, err := tx.TravarContas(ctx, t.PagadorID, t.RecebedorID)
		if err != nil {
			return err
		}
		pagador := contas[t.PagadorID]

		d := domain.Decidir(t, pagador.Saldo, pagador.Tecnica)
		if err := tx.GravarLancamentos(ctx, d.Lancamentos); err != nil {
			return fmt.Errorf("gravar lançamentos: %w", err)
		}
		final, err = tx.Finalizar(ctx, t.ID, d.Status, d.Motivo)
		nova = err == nil
		return err
	})
	if err == nil && nova && l.AoFinalizar != nil {
		l.AoFinalizar(final)
	}
	return final, err
}
