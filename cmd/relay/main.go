// Comando relay copia a outbox do Postgres para o Redis Stream. Pode rodar
// em mais de uma instância (FOR UPDATE SKIP LOCKED divide os lotes).
package main

import (
	"time"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/processo"
	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

func main() {
	log := processo.Log()
	ctx, parar := processo.ContextoComSinais()
	defer parar()

	pool, err := processo.Postgres(ctx)
	if err != nil {
		processo.Sair(log, "relay", err)
	}
	defer pool.Close()
	defer processo.Telemetria(ctx, log, "pix-relay")()
	rdb, err := processo.Redis(ctx)
	if err != nil {
		processo.Sair(log, "relay", err)
	}
	defer rdb.Close() //nolint:errcheck

	reg := processo.Metricas(ctx, log)
	r := usecase.Relay{
		Repo: repository.New(pool), Fila: queue.New(rdb, ""), Log: log,
		Metricas:         metricas.NovoRelay(reg),
		Lote:             processo.Inteiro("RELAY_LOTE", 200),
		Intervalo:        10 * time.Millisecond,
		IdadeOrfa:        30 * time.Second,
		MaxRepublicacoes: 5,
	}
	log.Info("relay iniciado", "lote", r.Lote)
	if err := r.Rodar(ctx); err != nil {
		processo.Sair(log, "relay", err)
	}
	log.Info("relay encerrado")
}
