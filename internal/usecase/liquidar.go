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
}

// Liquidar leva uma transferência PENDENTE ao status final.
//
// É idempotente: chamar de novo com uma transferência já finalizada devolve o
// resultado gravado sem tocar em nada. Isso é o que permite à fila entregar a
// mesma mensagem mais de uma vez.
func (l Liquidador) Liquidar(ctx context.Context, id string) (domain.Transferencia, error) {
	var final domain.Transferencia
	err := l.Repo.NaTransacao(ctx, func(tx repository.TxLiquidacao) error {
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
		return err
	})
	return final, err
}
