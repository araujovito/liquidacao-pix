package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Relay copia a outbox do Postgres para o Redis Stream.
type Relay struct {
	Repo *repository.Repositorio
	Fila *queue.Fila
	Log  *slog.Logger

	Lote      int           // linhas por transação
	Intervalo time.Duration // espera quando a outbox está vazia

	// Varredura de órfãs: PENDENTE publicada há mais de IdadeOrfa volta para
	// a outbox, no máximo MaxRepublicacoes vezes.
	IdadeOrfa        time.Duration
	MaxRepublicacoes int
}

// Rodar publica até ctx ser cancelado. Erros de Redis ou Postgres não
// derrubam o relay: ele espera (cada vez mais, até 5 s) e tenta de novo, e a
// outbox guarda tudo enquanto isso.
func (r Relay) Rodar(ctx context.Context) error {
	espera := r.Intervalo
	proximaVarredura := time.Now().Add(r.IdadeOrfa / 2)
	for {
		n, err := r.Repo.PublicarPendentes(ctx, r.Lote, r.Fila.Publicar)
		if ctx.Err() != nil {
			return nil
		}
		switch {
		case err != nil:
			r.Log.WarnContext(ctx, "relay: publicação falhou; outbox mantém os pedidos", "erro", err, "nova_tentativa_em", espera)
			espera = min(espera*2, 5*time.Second)
		case n == r.Lote:
			espera = r.Intervalo
			continue // outbox cheia: emenda o próximo lote sem dormir
		default:
			espera = r.Intervalo
		}

		if time.Now().After(proximaVarredura) {
			proximaVarredura = time.Now().Add(r.IdadeOrfa / 2)
			if k, err := r.Repo.RepublicarOrfas(ctx, r.IdadeOrfa, r.MaxRepublicacoes); err != nil {
				r.Log.WarnContext(ctx, "relay: varredura de órfãs falhou", "erro", err)
			} else if k > 0 {
				r.Log.WarnContext(ctx, "relay: transferências pendentes republicadas", "quantidade", k)
				continue
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(espera):
		}
	}
}
