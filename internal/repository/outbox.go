package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pendente é uma linha da outbox à espera de publicação.
type Pendente struct {
	TransferenciaID string
	Traceparent     string // contexto do rastro do pedido; vazio se o rastreamento estava desligado
}

// PublicarPendentes pega até limite linhas não publicadas da outbox, entrega
// os ids de transferência a publicar e, se publicar der certo, marca as
// linhas como publicadas — tudo na mesma transação.
//
// É a forma simples de PublicarPendentesDoRastro, para quem só precisa dos ids.
func (r *Repositorio) PublicarPendentes(ctx context.Context, limite int, publicar func(ctx context.Context, ids []string) ([]string, error)) (int, error) {
	return r.PublicarPendentesDoRastro(ctx, limite, func(ctx context.Context, ps []Pendente) ([]string, error) {
		ids := make([]string, len(ps))
		for i, p := range ps {
			ids[i] = p.TransferenciaID
		}
		return publicar(ctx, ids)
	})
}

// PublicarPendentesDoRastro faz o trabalho de PublicarPendentes entregando, além
// do id, o traceparent de cada linha, para o relay continuar o rastro do pedido.
//
// FOR UPDATE SKIP LOCKED deixa vários relays rodarem juntos: cada um pega um
// lote diferente em vez de esperar o do outro. Se publicar falhar, a transação
// desfaz e as linhas continuam pendentes. Se o commit falhar depois de
// publicar, a mensagem sai de novo no próximo lote: duplicata aceita de
// propósito, porque a liquidação é idempotente.
//
// publicar devolve o id da mensagem criada para cada transferência; ele fica
// gravado na outbox para a varredura de órfãs.
func (r *Repositorio) PublicarPendentesDoRastro(ctx context.Context, limite int, publicar func(ctx context.Context, pendentes []Pendente) ([]string, error)) (int, error) {
	var publicadas int
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, transferencia_id::text, coalesce(traceparent, '') FROM outbox
			 WHERE publicada_em IS NULL
			 ORDER BY id
			 LIMIT $1
			   FOR UPDATE SKIP LOCKED`, limite)
		if err != nil {
			return err
		}
		var linhas []int64
		var pendentes []Pendente
		for rows.Next() {
			var linha int64
			var p Pendente
			if err := rows.Scan(&linha, &p.TransferenciaID, &p.Traceparent); err != nil {
				return err
			}
			linhas, pendentes = append(linhas, linha), append(pendentes, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(pendentes) == 0 {
			return nil
		}
		mensagens, err := publicar(ctx, pendentes)
		if err != nil {
			return err
		}
		// O unnest de vários arrays preenche o menor com NULL até o tamanho do
		// maior. Sem esta validação, um publicador defeituoso poderia devolver
		// menos ids, marcar linhas como publicadas sem mensagem_id e ainda fazer
		// o método informar que publicou tudo.
		if len(mensagens) != len(pendentes) {
			return fmt.Errorf("publicar outbox: %d transferências, %d mensagens", len(pendentes), len(mensagens))
		}
		// Um id vazio tem a contagem certa mas não identifica mensagem: a
		// varredura de órfãs o trata como "sumiu do Redis" e republicaria.
		if slices.Contains(mensagens, "") {
			return errors.New("publicar outbox: id de mensagem vazio")
		}
		_, err = tx.Exec(ctx, `
			UPDATE outbox o SET publicada_em = now(), mensagem_id = p.mensagem_id
			  FROM unnest($1::bigint[], $2::text[]) AS p (id, mensagem_id)
			 WHERE o.id = p.id`, linhas, mensagens)
		publicadas = len(pendentes)
		return err
	})
	// Só informa publicações depois do commit: se o UPDATE ou o COMMIT
	// falharem, o rollback desfaz tudo e o chamador não pode ler n > 0.
	if err != nil {
		return 0, err
	}
	return publicadas, nil
}

// Orfa é uma linha da outbox cuja transferência segue PENDENTE muito depois
// de publicada.
type Orfa struct {
	Linha      int64
	MensagemID string
}

// CandidatasOrfas lista linhas publicadas há mais de idade cuja transferência
// ainda está PENDENTE, até maxRepublicacoes vezes cada. São só candidatas: a
// maioria costuma estar apenas esperando na fila, e quem decide é o relay,
// perguntando ao Redis se a mensagem ainda existe.
//
// Paginada por id: depoisDe é a última linha da página anterior (0 na
// primeira).
func (r *Repositorio) CandidatasOrfas(ctx context.Context, idade time.Duration, maxRepublicacoes, limite int, depoisDe int64) ([]Orfa, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, coalesce(o.mensagem_id, '')
		  FROM outbox o JOIN transferencias t ON t.id = o.transferencia_id
		 WHERE t.status = 'PENDENTE'
		   AND o.publicada_em < now() - make_interval(secs => $1)
		   AND o.republicacoes < $2
		   AND o.id > $4
		 ORDER BY o.id
		 LIMIT $3`, idade.Seconds(), maxRepublicacoes, limite, depoisDe)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Orfa])
}

// Republicar devolve as linhas à fila de publicação. A condição sobre
// publicada_em evita contar duas vezes uma linha que outro relay já
// devolveu.
func (r *Repositorio) Republicar(ctx context.Context, linhas []int64) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbox SET publicada_em = NULL, republicacoes = republicacoes + 1
		 WHERE id = ANY($1) AND publicada_em IS NOT NULL`, linhas)
	return tag.RowsAffected(), err
}

// PendentesNaOutbox conta as linhas ainda não publicadas.
func (r *Repositorio) PendentesNaOutbox(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE publicada_em IS NULL`).Scan(&n)
	return n, err
}

// Estado é uma fotografia barata do que está esperando no Postgres.
type Estado struct {
	OutboxPendentes        int64
	OutboxMaisAntigaSeg    float64 // idade da linha não publicada mais antiga; 0 se não há
	TransferenciasPendente int64
	PendenteMaisAntigaSeg  float64 // idade da transferência PENDENTE mais antiga; 0 se não há
}

// EstadoOperacional lê o estado usando só os índices parciais (outbox
// pendente e transferência pendente): o custo cresce com o backlog, não com o
// histórico.
func (r *Repositorio) EstadoOperacional(ctx context.Context) (Estado, error) {
	var e Estado
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(extract(epoch FROM now() - min(criada_em)), 0)
		  FROM outbox WHERE publicada_em IS NULL`).Scan(&e.OutboxPendentes, &e.OutboxMaisAntigaSeg); err != nil {
		return e, err
	}
	err := r.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(extract(epoch FROM now() - min(criada_em)), 0)
		  FROM transferencias WHERE status = 'PENDENTE'`).Scan(&e.TransferenciasPendente, &e.PendenteMaisAntigaSeg)
	return e, err
}
