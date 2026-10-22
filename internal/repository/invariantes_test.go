package repository_test

import (
	"context"
	"testing"

	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
)

func TestConstraintsDeFinalizacao(t *testing.T) {
	pool := testinfra.Postgres(t)
	ctx := context.Background()

	var pagador, recebedor int64
	if err := pool.QueryRow(ctx, `INSERT INTO contas (chave_pix, titular) VALUES ('p@teste', 'P') RETURNING id`).Scan(&pagador); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO contas (chave_pix, titular) VALUES ('r@teste', 'R') RETURNING id`).Scan(&recebedor); err != nil {
		t.Fatal(err)
	}

	casos := []struct {
		nome, status, motivo, finalizada string
		valor                            int64
	}{
		{"pendente com data", "PENDENTE", "", "now()", 100},
		{"liquidada sem data", "LIQUIDADA", "", "NULL", 100},
		{"rejeitada sem data", "REJEITADA", "SALDO_INSUFICIENTE", "NULL", 100},
		{"acima do limite", "PENDENTE", "", "NULL", 100_000_001},
	}
	for i, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			_, err := pool.Exec(ctx, `INSERT INTO transferencias
				(end_to_end_id, pagador_id, recebedor_id, valor_centavos, status, motivo_rejeicao, finalizada_em)
				VALUES ('invariante-' || $1, $2, $3, $4, $5, nullif($6, ''), `+caso.finalizada+`)`,
				i, pagador, recebedor, caso.valor, caso.status, caso.motivo)
			if err == nil {
				t.Fatal("banco aceitou estado inválido")
			}
		})
	}
}

func TestConciliacaoDetectaCreditoNaContaErrada(t *testing.T) {
	pool := testinfra.Postgres(t)
	ctx := context.Background()

	var liquidez, recebedor, terceiro int64
	if err := pool.QueryRow(ctx, `SELECT id FROM contas WHERE chave_pix = $1`, repository.ChaveLiquidez).Scan(&liquidez); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO contas (chave_pix, titular) VALUES ('destino@teste', 'Destino') RETURNING id`).Scan(&recebedor); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO contas (chave_pix, titular) VALUES ('errada@teste', 'Errada') RETURNING id`).Scan(&terceiro); err != nil {
		t.Fatal(err)
	}

	var transferencia string
	if err := pool.QueryRow(ctx, `INSERT INTO transferencias
		(end_to_end_id, pagador_id, recebedor_id, valor_centavos, status, finalizada_em)
		VALUES ('conciliacao-conta-errada', $1, $2, 100, 'LIQUIDADA', now()) RETURNING id`,
		liquidez, recebedor).Scan(&transferencia); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lancamentos (transferencia_id, conta_id, valor_centavos)
		VALUES ($1, $2, -100), ($1, $3, 100)`, transferencia, liquidez, terceiro); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE contas SET saldo_centavos = -100 WHERE id = $1`, liquidez); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE contas SET saldo_centavos = 100 WHERE id = $1`, terceiro); err != nil {
		t.Fatal(err)
	}

	divs, err := repository.New(pool).Conciliar(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range divs {
		if d.Tipo == "TRANSFERENCIA_INCONSISTENTE" {
			return
		}
	}
	t.Fatalf("conciliação não detectou crédito na conta errada: %+v", divs)
}
