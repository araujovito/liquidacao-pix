package repository

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chaveTravaMigracao é a chave do advisory lock que impede dois processos de
// migrarem ao mesmo tempo (ex.: API e worker subindo juntos).
const chaveTravaMigracao = 7_202_610

// Migrar aplica, em ordem de nome, os arquivos .sql ainda não registrados em
// schema_migrations. Cada arquivo roda numa transação própria: ou entra
// inteiro, ou não entra.
//
// Um migrador próprio de ~60 linhas foi preferido a uma biblioteca porque o
// projeto só precisa avançar (sem "down") e assim os testes de integração
// usam exatamente o mesmo caminho que produção.
func Migrar(ctx context.Context, pool *pgxpool.Pool, arquivos fs.FS) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, chaveTravaMigracao); err != nil {
		return nil, fmt.Errorf("travar migração: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, chaveTravaMigracao) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		versao      text PRIMARY KEY,
		aplicada_em timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}

	nomes, err := fs.Glob(arquivos, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(nomes)

	var aplicadas []string
	for _, nome := range nomes {
		versao := strings.TrimSuffix(strings.TrimPrefix(nome, "migrations/"), ".sql")
		var existe bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE versao = $1)`, versao,
		).Scan(&existe); err != nil {
			return aplicadas, err
		}
		if existe {
			continue
		}
		sql, err := fs.ReadFile(arquivos, nome)
		if err != nil {
			return aplicadas, err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (versao) VALUES ($1)`, versao)
			return err
		})
		if err != nil {
			return aplicadas, fmt.Errorf("migração %s: %w", versao, err)
		}
		aplicadas = append(aplicadas, versao)
	}
	return aplicadas, nil
}
