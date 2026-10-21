package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Pedido é o que o cliente envia: contas identificadas por chave PIX.
type Pedido struct {
	EndToEndID     string
	ChavePagador   string
	ChaveRecebedor string
	Valor          domain.Centavos
}

var (
	// ErrChaveDesconhecida: alguma das chaves PIX não pertence a conta nenhuma.
	ErrChaveDesconhecida = errors.New("chave PIX desconhecida")
	// ErrConflito: o end_to_end_id já foi usado com conteúdo diferente.
	ErrConflito = errors.New("end_to_end_id já usado com outro conteúdo")
)

// Receptor aceita pedidos de transferência para liquidação assíncrona.
type Receptor struct {
	Repo *repository.Repositorio
}

// Aceitar grava o pedido (transferência + outbox) e devolve a transferência
// PENDENTE. Não liquida: isso é trabalho do worker.
//
// Idempotente pelo end_to_end_id: repetir o mesmo pedido devolve a
// transferência já gravada com repetido=true; repetir a chave com outro
// conteúdo é ErrConflito. Pedidos simultâneos com a mesma chave também
// resolvem para uma transferência só, porque quem decide é a restrição
// UNIQUE do banco, não uma checagem prévia.
func (r Receptor) Aceitar(ctx context.Context, p Pedido) (t domain.Transferencia, repetido bool, err error) {
	pagador, err := r.idDaChave(ctx, p.ChavePagador)
	if err != nil {
		return t, false, err
	}
	recebedor, err := r.idDaChave(ctx, p.ChaveRecebedor)
	if err != nil {
		return t, false, err
	}
	novo := domain.Transferencia{EndToEndID: p.EndToEndID, PagadorID: pagador, RecebedorID: recebedor, Valor: p.Valor}
	if err := novo.Validar(); err != nil {
		return t, false, err
	}

	t, err = r.Repo.CriarComOutbox(ctx, novo)
	if !errors.Is(err, repository.ErrEndToEndRepete) {
		return t, false, err
	}

	existente, err := r.Repo.BuscarPorEndToEnd(ctx, p.EndToEndID)
	if err != nil {
		return t, false, err
	}
	if existente.PagadorID != novo.PagadorID || existente.RecebedorID != novo.RecebedorID || existente.Valor != novo.Valor {
		return t, false, ErrConflito
	}
	return existente, true, nil
}

func (r Receptor) idDaChave(ctx context.Context, chave string) (int64, error) {
	id, err := r.Repo.IDDeContaCliente(ctx, chave)
	if errors.Is(err, repository.ErrNaoEncontrada) {
		return 0, fmt.Errorf("%w: %q", ErrChaveDesconhecida, chave)
	}
	return id, err
}
