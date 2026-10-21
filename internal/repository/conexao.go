// Package repository concentra o acesso ao Postgres. O SQL é escrito à mão
// (pgx, sem ORM) para que travas, isolamento e índices fiquem explícitos.
package repository

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// URLPadrao aponta para o Postgres do docker-compose de desenvolvimento.
const URLPadrao = "postgres://pix:pix@localhost:5432/pix?sslmode=disable"

// URLDoAmbiente lê DATABASE_URL e cai no padrão de desenvolvimento.
func URLDoAmbiente() string {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u
	}
	return URLPadrao
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
