package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Conciliador roda a conciliação do livro-razão em intervalos fixos e publica
// o resultado como métrica.
//
// A conciliação varre as tabelas inteiras; em produção o intervalo deve ser
// escolhido pelo tamanho do banco (o tempo da última execução aparece em
// pix_conciliacao_duracao_segundos).
type Conciliador struct {
	Repo      *repository.Repositorio
	Log       *slog.Logger
	Metricas  *metricas.Conciliacao // opcional
	Intervalo time.Duration
}

// Rodar concilia imediatamente e depois a cada Intervalo, até ctx ser cancelado.
func (c Conciliador) Rodar(ctx context.Context) error {
	for {
		c.uma(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(c.Intervalo):
		}
	}
}

func (c Conciliador) uma(ctx context.Context) {
	if _, err := c.Uma(ctx); err != nil && ctx.Err() == nil {
		c.Metricas.Falhou()
		c.Log.WarnContext(ctx, "conciliação não conseguiu rodar", "erro", err)
	}
}

// Uma roda a conciliação uma vez, registra o resultado (métricas e log) e
// devolve quantas divergências encontrou. O erro é de execução (banco fora do
// ar), não de divergência.
func (c Conciliador) Uma(ctx context.Context) (int, error) {
	inicio := time.Now()
	divs, err := c.Repo.Conciliar(ctx)
	if err != nil {
		return 0, err
	}
	c.Metricas.Concluida(len(divs), time.Since(inicio))
	if len(divs) == 0 {
		c.Log.InfoContext(ctx, "conciliação fechada", "duracao", time.Since(inicio).Round(time.Millisecond))
		return 0, nil
	}
	// Os detalhes têm ids internos de conta e de transferência, nunca dados
	// pessoais; podem ir para o log.
	for _, d := range divs {
		c.Log.ErrorContext(ctx, "conciliação divergente", "tipo", d.Tipo, "detalhe", d.Detalhe)
	}
	return len(divs), nil
}
