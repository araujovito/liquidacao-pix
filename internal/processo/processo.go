// Package processo junta o que os comandos têm em comum: log em JSON,
// sinais de encerramento e conexões a partir do ambiente.
package processo

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/telemetria"
)

// Log devolve o logger JSON. LOG_NIVEL=debug mostra cada liquidação.
func Log() *slog.Logger {
	nivel := slog.LevelInfo
	if os.Getenv("LOG_NIVEL") == "debug" {
		nivel = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: nivel}))
}

// ContextoComSinais é cancelado no SIGINT ou SIGTERM.
func ContextoComSinais() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Postgres conecta em DATABASE_URL.
func Postgres(ctx context.Context) (*pgxpool.Pool, error) {
	url, err := repository.URLDoAmbiente()
	if err != nil {
		return nil, err
	}
	return repository.Conectar(ctx, url)
}

// Redis conecta em REDIS_URL.
func Redis(ctx context.Context) (*redis.Client, error) {
	url, err := queue.URLDoAmbiente()
	if err != nil {
		return nil, err
	}
	return queue.Conectar(ctx, url)
}

// Metricas cria o registro do processo e começa a expô-lo em /metrics.
func Metricas(ctx context.Context, log *slog.Logger) *prometheus.Registry {
	reg := metricas.NovoRegistro()
	metricas.Servir(ctx, log, reg)
	return reg
}

// Telemetria liga o rastreamento (se OTEL_EXPORTER_OTLP_ENDPOINT existir) e
// devolve a função que esvazia os spans pendentes; use com defer. O
// encerramento usa um contexto novo e curto: o do processo já foi cancelado.
func Telemetria(ctx context.Context, log *slog.Logger, servico string) func() {
	encerrar, err := telemetria.Iniciar(ctx, log, servico)
	if err != nil {
		// Rastreamento é diagnóstico: falha ao ligá-lo não derruba o serviço.
		log.Warn("rastreamento desligado", "erro", err)
		return func() {}
	}
	return func() {
		fim, cancelar := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelar()
		if err := encerrar(fim); err != nil {
			log.Warn("esvaziar spans", "erro", err)
		}
	}
}

// Inteiro lê uma variável de ambiente numérica, com padrão.
func Inteiro(nome string, padrao int) int {
	if v, err := strconv.Atoi(os.Getenv(nome)); err == nil && v > 0 {
		return v
	}
	return padrao
}

// Sair registra o erro e encerra com código 1.
func Sair(log *slog.Logger, programa string, err error) {
	log.Error(programa+" encerrado com erro", "erro", err)
	os.Exit(1)
}
