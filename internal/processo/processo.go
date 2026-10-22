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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
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
