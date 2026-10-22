// Comando conciliador roda a conciliação do livro-razão periodicamente e
// expõe em /metrics o estado operacional do sistema: backlog e idade da
// outbox, transferências pendentes, tamanho do stream e das mortas, e o
// resultado da última conciliação.
//
// Rode UMA instância: o estado é lido no momento da coleta, e várias
// instâncias duplicariam as séries.
package main

import (
	"flag"
	"os"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/processo"
	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

func main() {
	umaVez := flag.Bool("uma-vez", false, "concilia uma vez e sai (código 1 se houver divergência); não expõe métricas")
	flag.Parse()

	log := processo.Log()
	ctx, parar := processo.ContextoComSinais()
	defer parar()

	pool, err := processo.Postgres(ctx)
	if err != nil {
		processo.Sair(log, "conciliador", err)
	}
	defer pool.Close()

	if *umaVez {
		n, err := (usecase.Conciliador{Repo: repository.New(pool), Log: log}).Uma(ctx)
		if err != nil {
			processo.Sair(log, "conciliador", err)
		}
		if n > 0 {
			os.Exit(1)
		}
		return
	}
	rdb, err := processo.Redis(ctx)
	if err != nil {
		processo.Sair(log, "conciliador", err)
	}
	defer rdb.Close() //nolint:errcheck

	repo := repository.New(pool)
	reg := processo.Metricas(ctx, log)
	reg.MustRegister(metricas.NovoEstado(repo, queue.New(rdb, "")))

	c := usecase.Conciliador{
		Repo: repo, Log: log, Metricas: metricas.NovaConciliacao(reg),
		Intervalo: time.Duration(processo.Inteiro("CONCILIACAO_INTERVALO_SEGUNDOS", 120)) * time.Second,
	}
	log.Info("conciliador iniciado", "intervalo", c.Intervalo)
	if err := c.Rodar(ctx); err != nil {
		processo.Sair(log, "conciliador", err)
	}
	log.Info("conciliador encerrado")
}
