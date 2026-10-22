package queue

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

// URLDoAmbiente lê REDIS_URL (sem valor padrão no código, como DATABASE_URL).
func URLDoAmbiente() (string, error) {
	if u := os.Getenv("REDIS_URL"); u != "" {
		return u, nil
	}
	return "", errors.New("REDIS_URL não definida")
}

// Conectar abre o cliente e confirma que o Redis responde.
func Conectar(ctx context.Context, url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping no redis: %w", err)
	}
	return rdb, nil
}
