package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// PublicarPendentes pega até limite linhas não publicadas da outbox, entrega
// os ids de transferência a publicar e, se publicar der certo, marca as
// linhas como publicadas — tudo na mesma transação.
//
// FOR UPDATE SKIP LOCKED deixa vários relays rodarem juntos: cada um pega um
// lote diferente em vez de esperar o do outro. Se publicar falhar, a transação
// desfaz e as linhas continuam pendentes. Se o commit falhar depois de
// publicar, a mensagem sai de novo no próximo lote: duplicata aceita de
// propósito, porque a liquidação é idempotente.
func (r *Repositorio) PublicarPendentes(ctx context.Context, limite int, publicar func(ctx context.Context, ids []string) error) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, transferencia_id::text FROM outbox
			 WHERE publicada_em IS NULL
			 ORDER BY id
			 LIMIT $1
			   FOR UPDATE SKIP LOCKED`, limite)
		if err != nil {
			return err
		}
		var linhas []int64
		var ids []string
		for rows.Next() {
			var linha int64
			var id string
			if err := rows.Scan(&linha, &id); err != nil {
				return err
			}
			linhas, ids = append(linhas, linha), append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := publicar(ctx, ids); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE outbox SET publicada_em = now() WHERE id = ANY($1)`, linhas)
		n = len(ids)
		return err
	})
	return n, err
}

// RepublicarOrfas devolve à fila de publicação as transferências que seguem
// PENDENTE muito depois de publicadas. Cobre a mensagem perdida no Redis
// (queda com AOF everysec perde até ~1 s; sem AOF, tudo): sem isto, a
// transferência ficaria pendente para sempre.
func (r *Repositorio) RepublicarOrfas(ctx context.Context, idade time.Duration, maxRepublicacoes int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbox o
		   SET publicada_em = NULL, republicacoes = o.republicacoes + 1
		  FROM transferencias t
		 WHERE t.id = o.transferencia_id
		   AND t.status = 'PENDENTE'
		   AND o.publicada_em < now() - make_interval(secs => $1)
		   AND o.republicacoes < $2`, idade.Seconds(), maxRepublicacoes)
	return tag.RowsAffected(), err
}

// PendentesNaOutbox conta as linhas ainda não publicadas.
func (r *Repositorio) PendentesNaOutbox(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE publicada_em IS NULL`).Scan(&n)
	return n, err
}
