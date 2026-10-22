// Comando worker liquida as transferências do Redis Stream. WORKERS define
// quantos consumidores rodam no processo; cada um tem nome próprio no grupo.
package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
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
		processo.Sair(log, "worker", err)
	}
	defer pool.Close()
	defer processo.Telemetria(ctx, log, "pix-worker")()
	rdb, err := processo.Redis(ctx)
	if err != nil {
		processo.Sair(log, "worker", err)
	}
	defer rdb.Close() //nolint:errcheck

	host, _ := os.Hostname()
	fila := queue.New(rdb, "")
	reg := processo.Metricas(ctx, log)
	wm := metricas.NovoWorker(reg)
	chaveiro, err := auditoria.ChaveiroDoAmbiente()
	if err != nil {
		processo.Sair(log, "worker", err)
	}
	liq := usecase.Liquidador{Repo: repository.New(pool), Auditoria: chaveiro, AoFinalizar: wm.Finalizada}
	// 32: com 8, o worker liquidava ~1.400/s e não acompanhava 1.500/s de
	// entrada (docs/carga.md). Cada liquidação passa a maior parte do tempo
	// esperando o banco, então mais consumidores rendem mais até o limite do
	// pool de conexões.
	n := processo.Inteiro("WORKERS", 32)

	var wg sync.WaitGroup
	for i := range n {
		w := usecase.Worker{
			Liquidador: liq, Fila: fila, Log: log, Metricas: wm,
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
