// Comando worker liquida as transferências do Redis Stream. WORKERS define
// quantos consumidores rodam no processo; cada um tem nome próprio no grupo.
package main

import (
	"fmt"
	"os"
	"sync"
	"time"

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
		processo.Sair(log, "worker", err)
	}
	defer pool.Close()
	rdb, err := processo.Redis(ctx)
	if err != nil {
		processo.Sair(log, "worker", err)
	}
	defer rdb.Close() //nolint:errcheck

	host, _ := os.Hostname()
	fila := queue.New(rdb, "")
	liq := usecase.Liquidador{Repo: repository.New(pool)}
	n := processo.Inteiro("WORKERS", 8)

	var wg sync.WaitGroup
	for i := range n {
		w := usecase.Worker{
			Liquidador: liq, Fila: fila, Log: log,
			Consumidor:  fmt.Sprintf("%s-%d-%d", host, os.Getpid(), i),
			Lote:        20,
			EsperaBase:  5 * time.Second,
			MaxEntregas: 6, // 5 + 10 + 20 + 40 + 80 s: ~2,5 min tentando antes de desistir
		}
		wg.Go(func() {
			if err := w.Rodar(ctx); err != nil {
				log.Error("consumidor parou", "consumidor", w.Consumidor, "erro", err)
			}
		})
	}
	log.Info("worker iniciado", "consumidores", n)
	wg.Wait()
	log.Info("worker encerrado")
}
