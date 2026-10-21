// Package repository concentra o acesso ao Postgres. O SQL é escrito à mão
// (pgx, sem ORM) para que travas, isolamento e índices fiquem explícitos.
package repository

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// URLDoAmbiente lê DATABASE_URL. Não há valor padrão no código, para que
// nenhuma credencial fique no binário; o Makefile define a de desenvolvimento.
func URLDoAmbiente() (string, error) {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u, nil
	}
	return "", errors.New("DATABASE_URL não definida")
}

// Conectar abre o pool e confirma que o banco responde.
func Conectar(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("abrir pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping no postgres: %w", err)
	}
	return pool, nil
}
