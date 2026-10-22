package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/araujovito/liquidacao-pix/internal/domain"
)

// Divergencia descreve uma quebra de invariante encontrada na conciliação.
type Divergencia struct {
	Tipo    string
	Detalhe string
}

// Conciliar confere as invariantes do livro-razão numa fotografia única do
// banco (REPEATABLE READ, só leitura), para não acusar divergência falsa por
// causa de uma liquidação que acontece no meio da conferência.
func (r *Repositorio) Conciliar(ctx context.Context) ([]Divergencia, error) {
	var divs []Divergencia
	err := pgx.BeginTxFunc(ctx, r.pool,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			// 1. Conservação: todo débito tem um crédito.
			var soma int64
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(valor_centavos), 0) FROM lancamentos`).Scan(&soma); err != nil {
				return err
			}
			if soma != 0 {
				divs = append(divs, Divergencia{"SOMA_GLOBAL", domain.Centavos(soma).String()})
			}

			// 2. A projeção de saldo bate com os lançamentos.
			rows, err := tx.Query(ctx, `
				SELECT c.id::text || ': saldo ' || c.saldo_centavos || ', lançamentos ' || coalesce(sum(l.valor_centavos), 0)
				  FROM contas c LEFT JOIN lancamentos l ON l.conta_id = c.id
				 GROUP BY c.id
				HAVING c.saldo_centavos <> coalesce(sum(l.valor_centavos), 0)`)
			if err != nil {
				return err
			}
			if err := coletar(rows, "SALDO_DIVERGENTE", &divs); err != nil {
				return err
			}

			// 3. Cada transferência liquidada tem exatamente débito + crédito do
			// valor certo; as outras não têm lançamento nenhum.
			rows, err = tx.Query(ctx, `
				SELECT t.id::text || ' (' || t.status || '): ' || count(l.id) || ' lançamentos'
				  FROM transferencias t LEFT JOIN lancamentos l ON l.transferencia_id = t.id
				 GROUP BY t.id
				HAVING (t.status = 'LIQUIDADA' AND (
				           count(l.id) <> 2
				        OR coalesce(sum(l.valor_centavos), 0) <> 0
				        OR count(*) FILTER (WHERE l.conta_id = t.pagador_id AND l.valor_centavos = -t.valor_centavos) <> 1
				        OR count(*) FILTER (WHERE l.conta_id = t.recebedor_id AND l.valor_centavos = t.valor_centavos) <> 1))
				    OR (t.status <> 'LIQUIDADA' AND count(l.id) <> 0)`)
			if err != nil {
				return err
			}
			return coletar(rows, "TRANSFERENCIA_INCONSISTENTE", &divs)
		})
	return divs, err
}

func coletar(rows pgx.Rows, tipo string, divs *[]Divergencia) error {
	detalhes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, d := range detalhes {
		*divs = append(*divs, Divergencia{tipo, d})
	}
	return err
}
