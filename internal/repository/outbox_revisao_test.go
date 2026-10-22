package repository_test

import (
	"context"
	"testing"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
)

// outboxComUmPedido cria duas contas e uma transferência com linha na outbox.
func outboxComUmPedido(t *testing.T) (*repository.Repositorio, context.Context) {
	t.Helper()
	ctx := context.Background()
	repo := repository.New(testinfra.Postgres(t))
	de, err := repo.CriarConta(ctx, "revisao-de@teste", "De")
	if err != nil {
		t.Fatal(err)
	}
	para, err := repo.CriarConta(ctx, "revisao-para@teste", "Para")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CriarComOutbox(ctx, domain.Transferencia{
		EndToEndID: "E1234567820261021143000000000201", PagadorID: de, RecebedorID: para, Valor: 100,
	}); err != nil {
		t.Fatal(err)
	}
	return repo, ctx
}

// Revisão de 1599e4f. A correção passou a rejeitar contagem errada de ids,
// mas PublicarPendentes ainda devolvia n > 0 junto com um erro quando o UPDATE
// (ou o commit) falhava depois da publicação, porque n era atribuído antes de
// o resultado ser conhecido. O contrato é: erro => nada foi publicado.
func TestPublicarPendentesComErroNaoInformaPublicacoes(t *testing.T) {
	repo, base := outboxComUmPedido(t)
	ctx, cancelar := context.WithCancel(base)
	defer cancelar()

	n, err := repo.PublicarPendentes(ctx, 10, func(context.Context, []string) ([]string, error) {
		// A mensagem "saiu", mas o contexto cai antes do UPDATE da outbox.
		cancelar()
		return []string{"1-0"}, nil
	})
	if err == nil {
		t.Fatal("esperava erro: o contexto foi cancelado antes do UPDATE")
	}
	if n != 0 {
		t.Fatalf("erro e n=%d: informou publicações que o rollback desfez", n)
	}
	if pendentes, err := repo.PendentesNaOutbox(base); err != nil || pendentes != 1 {
		t.Fatalf("a linha deveria continuar pendente: %d, erro=%v", pendentes, err)
	}
}

// Um id vazio tem a contagem certa, mas não identifica mensagem nenhuma: a
// varredura de órfãs trata "" como "não existe no Redis" e republicaria a
// transferência a cada ciclo até o limite.
func TestPublicacaoComIdVazioNaoMarcaOutbox(t *testing.T) {
	repo, ctx := outboxComUmPedido(t)

	n, err := repo.PublicarPendentes(ctx, 10, func(context.Context, []string) ([]string, error) {
		return []string{""}, nil
	})
	if err == nil || n != 0 {
		t.Fatalf("id vazio foi aceito: n=%d, erro=%v", n, err)
	}
	if pendentes, err := repo.PendentesNaOutbox(ctx); err != nil || pendentes != 1 {
		t.Fatalf("a linha deveria continuar pendente: %d, erro=%v", pendentes, err)
	}
}
