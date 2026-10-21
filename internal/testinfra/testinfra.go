// Package testinfra sobe Postgres e Redis reais em contêineres para os testes
// de integração (Testcontainers). Um contêiner por pacote de teste; cada
// teste recebe um banco novo, já migrado, para não haver interferência.
package testinfra

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/araujovito/liquidacao-pix/db"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

var (
	pgUma      sync.Once
	pgBase     string
	pgErr      error
	contadorDB atomic.Int64
)

// Postgres devolve um pool para um banco novo e migrado. Pula o teste com
// -short, para que `go test -short` rode sem Docker.
func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("teste de integração (precisa de Docker); rodando com -short")
	}
	ctx := context.Background()
	pgUma.Do(func() {
		var c *tcpostgres.PostgresContainer
		c, pgErr = tcpostgres.Run(ctx, "postgres:17-alpine",
			tcpostgres.WithDatabase("pix"),
			tcpostgres.WithUsername("pix"),
			tcpostgres.WithPassword("pix"),
			tcpostgres.BasicWaitStrategies(),
			// Mais conexões que o padrão (100): o teste de concorrência abre muitas.
			testcontainers.WithCmd("postgres", "-c", "max_connections=300", "-c", "fsync=off"),
		)
		if pgErr == nil {
			pgBase, pgErr = c.ConnectionString(ctx, "sslmode=disable")
		}
	})
	if pgErr != nil {
		t.Fatalf("subir postgres: %v", pgErr)
	}

	admin, err := repository.Conectar(ctx, pgBase)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	nome := fmt.Sprintf("teste_%d", contadorDB.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+nome); err != nil {
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(pgBase)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = nome
	cfg.MaxConns = 40
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := repository.Migrar(ctx, pool, db.Migracoes); err != nil {
		t.Fatal(err)
	}
	return pool
}
