// Package usecase orquestra domínio e repositório. As regras de decisão
// ficam no domain; aqui ficam a ordem das operações e as garantias.
package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Liquidador liquida transferências pendentes.
type Liquidador struct {
	Repo *repository.Repositorio

	// Auditoria assina a decisão de cada liquidação na cadeia de HMAC das
	// contas (ADR 0006). É obrigatória: sem ela, Liquidar falha em vez de
	// finalizar uma transferência sem trilha.
	Auditoria *auditoria.Chaveiro

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
	if l.Auditoria == nil {
		return domain.Transferencia{}, auditoria.ErrSemChaveiro
	}
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
		final, err = tx.Finalizar(ctx, t.ID, d.Status, d.Motivo, instanteDaDecisao(t))
		if err != nil {
			return err
		}
		if err := tx.GravarAuditoria(ctx, l.Auditoria, final, contas); err != nil {
			return fmt.Errorf("gravar auditoria: %w", err)
		}
		nova = true
		return nil
	})
	if err == nil && nova && l.AoFinalizar != nil {
		l.AoFinalizar(final)
	}
	return final, err
}

// instanteDaDecisao é a hora da decisão, em microssegundos (a precisão do
// banco). Nunca antes da criação da transferência: o relógio da aplicação pode
// estar um pouco atrás do relógio do banco que carimbou criada_em, e a
// restrição finalizacao_nao_antecede_criacao recusaria a linha.
func instanteDaDecisao(t domain.Transferencia) time.Time {
	agora := time.Now().UTC().Truncate(time.Microsecond)
	if agora.Before(t.CriadaEm) {
		return t.CriadaEm.UTC().Truncate(time.Microsecond)
	}
	return agora
}
