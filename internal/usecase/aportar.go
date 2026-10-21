package usecase

import (
	"context"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Aportar coloca dinheiro numa conta de cliente a partir da conta técnica de
// liquidez. É uma transferência comum, liquidada na hora, para que até a
// entrada de dinheiro passe pelo livro-razão e pela conciliação.
func (l Liquidador) Aportar(ctx context.Context, contaID int64, valor domain.Centavos, e2e string) (domain.Transferencia, error) {
	liquidez, err := l.Repo.IDDaChave(ctx, repository.ChaveLiquidez)
	if err != nil {
		return domain.Transferencia{}, err
	}
	t := domain.Transferencia{EndToEndID: e2e, PagadorID: liquidez, RecebedorID: contaID, Valor: valor}
	if err := t.Validar(); err != nil {
		return domain.Transferencia{}, err
	}
	t, err = l.Repo.CriarTransferencia(ctx, t)
	if err != nil {
		return domain.Transferencia{}, err
	}
	return l.Liquidar(ctx, t.ID)
}
