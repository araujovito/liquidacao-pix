// Comando api recebe pedidos de transferência por HTTP e responde 202 depois
// de gravá-los com a outbox. Não liquida nada.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/processo"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
	"github.com/araujovito/liquidacao-pix/internal/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := rodar(log); err != nil {
		log.Error("api encerrada com erro", "erro", err)
		os.Exit(1)
	}
}

func rodar(log *slog.Logger) error {
	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()

	url, err := repository.URLDoAmbiente()
	if err != nil {
		return err
	}
	pool, err := repository.Conectar(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	defer processo.Telemetria(ctx, log, "pix-api")()

	repo := repository.New(pool)
	reg := processo.Metricas(ctx, log)
	srv := &http.Server{
		Addr: ":" + porta(),
		Handler: (&web.Servidor{
			Receptor: usecase.Receptor{Repo: repo}, Repo: repo, Log: log,
			Metricas: metricas.NovaAPI(reg),
		}).Rotas(),
		// Prazos explícitos: sem eles um cliente lento segura a conexão para sempre.
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	erros := make(chan error, 1)
	go func() {
		log.Info("api ouvindo", "endereco", srv.Addr)
		erros <- srv.ListenAndServe()
	}()

	select {
	case err := <-erros:
		return err
	case <-ctx.Done():
	}

	// Encerramento gracioso: para de aceitar conexões e espera os pedidos em
	// andamento terminarem o commit, para nenhum cliente ficar sem resposta.
	log.Info("encerrando: esperando pedidos em andamento")
	desligar, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelar()
	if err := srv.Shutdown(desligar); err != nil {
		return err
	}
	if err := <-erros; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func porta() string {
	if p := os.Getenv("PORTA"); p != "" {
		return p
	}
	return "8080"
}
