package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
)

func TestPublicacaoIncompletaNaoMarcaOutbox(t *testing.T) {
	pool := testinfra.Postgres(t)
	ctx := context.Background()
	repo := repository.New(pool)

	pagador, err := repo.CriarConta(ctx, "outbox-pagador@teste", "Pagador")
	if err != nil {
		t.Fatal(err)
	}
	recebedor, err := repo.CriarConta(ctx, "outbox-recebedor@teste", "Recebedor")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CriarComOutbox(ctx, domain.Transferencia{
		EndToEndID:  "E1234567820261021143000000000101",
		PagadorID:   pagador,
		RecebedorID: recebedor,
		Valor:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	n, err := repo.PublicarPendentes(ctx, 10, func(context.Context, []string) ([]string, error) {
		return nil, nil // simula publicador que perdeu parte da resposta
	})
	if err == nil {
		t.Fatal("publicação com quantidade errada foi aceita")
	}
	if n != 0 {
		t.Fatalf("informou %d publicações apesar do rollback", n)
	}
	pendentes, err := repo.PendentesNaOutbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pendentes != 1 {
		t.Fatalf("outbox ficou com %d pendentes, esperava 1", pendentes)
	}
}

func TestCancelamentoDepoisDosLancamentosDesfazTudo(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo := repository.New(pool)
	base := context.Background()

	liquidez, err := repo.IDDaChave(base, repository.ChaveLiquidez)
	if err != nil {
		t.Fatal(err)
	}
	recebedor, err := repo.CriarConta(base, "rollback@teste", "Rollback")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := repo.CriarTransferencia(base, domain.Transferencia{
		EndToEndID:  "E1234567820261021143000000000102",
		PagadorID:   liquidez,
		RecebedorID: recebedor,
		Valor:       500,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancelar := context.WithCancel(base)
	chegouAposGravacao := false
	err = repo.NaTransacao(ctx, func(tx repository.TxLiquidacao) error {
		travada, err := tx.TravarTransferencia(ctx, tr.ID)
		if err != nil {
			return err
		}
		contas, err := tx.TravarContas(ctx, travada.PagadorID, travada.RecebedorID)
		if err != nil {
			return err
		}
		decisao := domain.Decidir(travada, contas[travada.PagadorID].Saldo, contas[travada.PagadorID].Tecnica)
		if decisao.Status != domain.Liquidada || len(decisao.Lancamentos) != 2 {
			t.Fatalf("pré-condição do teste não produziu dois lançamentos: %+v", decisao)
		}
		if err := tx.GravarLancamentos(ctx, decisao.Lancamentos); err != nil {
			return err
		}
		chegouAposGravacao = true

		// Cancela depois dos INSERTs e UPDATEs, mas antes do status final e do
		// commit. A transação inteira precisa desaparecer.
		cancelar()
		_, err = tx.Finalizar(ctx, travada.ID, decisao.Status, decisao.Motivo)
		return err
	})
	if !chegouAposGravacao {
		t.Fatal("teste não chegou ao ponto de cancelamento depois da gravação")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("esperava context.Canceled depois da gravação, veio %v", err)
	}

	gravada, err := repo.BuscarTransferencia(base, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gravada.Status != domain.Pendente || gravada.FinalizadaEm != nil {
		t.Fatalf("transferência mudou apesar do rollback: %+v", gravada)
	}
	if saldo, err := repo.SaldoDaConta(base, recebedor); err != nil || saldo != 0 {
		t.Fatalf("saldo do recebedor depois do rollback: %s, erro=%v", saldo, err)
	}
	var lancamentos int
	if err := pool.QueryRow(base, `SELECT count(*) FROM lancamentos WHERE transferencia_id = $1`, tr.ID).Scan(&lancamentos); err != nil {
		t.Fatal(err)
	}
	if lancamentos != 0 {
		t.Fatalf("rollback deixou %d lançamentos", lancamentos)
	}
}
