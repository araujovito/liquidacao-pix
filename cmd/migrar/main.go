// Comando migrar aplica as migrações pendentes em DATABASE_URL.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/araujovito/liquidacao-pix/db"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

func main() {
	ctx := context.Background()
	url, err := repository.URLDoAmbiente()
	if err != nil {
		slog.Error("configuração", "erro", err)
		os.Exit(1)
	}
	pool, err := repository.Conectar(ctx, url)
	if err != nil {
		slog.Error("conectar", "erro", err)
		os.Exit(1)
	}
	defer pool.Close()

	aplicadas, err := repository.Migrar(ctx, pool, db.Migracoes)
	if err != nil {
		slog.Error("migrar", "erro", err)
		os.Exit(1)
	}
	slog.Info("migrações aplicadas", "novas", aplicadas)
}
