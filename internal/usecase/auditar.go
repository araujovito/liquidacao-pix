package usecase

import (
	"context"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// ResumoAuditoria é o resultado de verificar todas as cadeias.
type ResumoAuditoria struct {
	Contas, Entradas int
	Problemas        []auditoria.Problema
}

// VerificarAuditoria lê todas as cadeias e as confere com o chaveiro, sem
// âncoras. Quem precisa de âncoras ou das cabeças (cmd/auditar) faz o
// percurso por conta própria.
func VerificarAuditoria(ctx context.Context, repo *repository.Repositorio, ch *auditoria.Chaveiro) (ResumoAuditoria, error) {
	var r ResumoAuditoria
	err := repo.LerAuditoria(ctx, func(c auditoria.ContaLida) error {
		r.Contas++
		r.Entradas += len(c.Entradas)
		r.Problemas = append(r.Problemas, ch.VerificarConta(c, nil)...)
		return nil
	})
	return r, err
}
