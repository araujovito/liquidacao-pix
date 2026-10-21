package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/araujovito/liquidacao-pix/internal/domain"
)

// ChaveLiquidez identifica a conta técnica criada pela migração 0001.
const ChaveLiquidez = "liquidez@sistema"

var (
	ErrNaoEncontrada  = errors.New("não encontrada")
	ErrEndToEndRepete = errors.New("end_to_end_id já existe")
)

// Repositorio é o acesso ao Postgres usado pelos casos de uso.
type Repositorio struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repositorio { return &Repositorio{pool: pool} }

// CriarConta cadastra uma conta de cliente com saldo zero.
func (r *Repositorio) CriarConta(ctx context.Context, chavePix, titular string) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO contas (chave_pix, titular) VALUES ($1, $2) RETURNING id`,
		chavePix, titular,
	).Scan(&id)
	return id, err
}

// IDDaChave devolve o id da conta dona da chave PIX.
func (r *Repositorio) IDDaChave(ctx context.Context, chavePix string) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `SELECT id FROM contas WHERE chave_pix = $1`, chavePix).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNaoEncontrada
	}
	return id, err
}

// SaldoDaConta lê o saldo projetado (sem trava).
func (r *Repositorio) SaldoDaConta(ctx context.Context, id int64) (domain.Centavos, error) {
	var s int64
	err := r.pool.QueryRow(ctx, `SELECT saldo_centavos FROM contas WHERE id = $1`, id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNaoEncontrada
	}
	return domain.Centavos(s), err
}

// CriarTransferencia grava o pedido como PENDENTE.
func (r *Repositorio) CriarTransferencia(ctx context.Context, t domain.Transferencia) (domain.Transferencia, error) {
	return inserirTransferencia(ctx, r.pool, t)
}

type executor interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func inserirTransferencia(ctx context.Context, db executor, t domain.Transferencia) (domain.Transferencia, error) {
	row := db.QueryRow(ctx, `
		INSERT INTO transferencias (end_to_end_id, pagador_id, recebedor_id, valor_centavos)
		VALUES ($1, $2, $3, $4)
		RETURNING `+colunasTransferencia,
		t.EndToEndID, t.PagadorID, t.RecebedorID, int64(t.Valor))
	criada, err := lerTransferencia(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "transferencias_end_to_end_id_key" {
		return domain.Transferencia{}, ErrEndToEndRepete
	}
	return criada, err
}

// BuscarTransferencia lê uma transferência pelo id.
func (r *Repositorio) BuscarTransferencia(ctx context.Context, id string) (domain.Transferencia, error) {
	return lerTransferencia(r.pool.QueryRow(ctx,
		`SELECT `+colunasTransferencia+` FROM transferencias WHERE id = $1`, id))
}

// BuscarPorEndToEnd lê uma transferência pela chave de idempotência.
func (r *Repositorio) BuscarPorEndToEnd(ctx context.Context, e2e string) (domain.Transferencia, error) {
	return lerTransferencia(r.pool.QueryRow(ctx,
		`SELECT `+colunasTransferencia+` FROM transferencias WHERE end_to_end_id = $1`, e2e))
}

const colunasTransferencia = `id, end_to_end_id, pagador_id, recebedor_id, valor_centavos,
	status, coalesce(motivo_rejeicao, ''), criada_em, finalizada_em`

func lerTransferencia(row pgx.Row) (domain.Transferencia, error) {
	var t domain.Transferencia
	var valor int64
	var status string
	err := row.Scan(&t.ID, &t.EndToEndID, &t.PagadorID, &t.RecebedorID, &valor,
		&status, &t.MotivoRejeicao, &t.CriadaEm, &t.FinalizadaEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNaoEncontrada
	}
	t.Valor, t.Status = domain.Centavos(valor), domain.Status(status)
	return t, err
}

// ContaTravada é o estado de uma conta lido sob FOR UPDATE.
type ContaTravada struct {
	ID      int64
	Saldo   domain.Centavos
	Tecnica bool
}

// TxLiquidacao expõe as operações de liquidação dentro de uma transação.
type TxLiquidacao struct {
	tx pgx.Tx
}

// NaTransacao roda fn numa transação READ COMMITTED. As travas de linha
// (FOR UPDATE) já dão o isolamento necessário; SERIALIZABLE obrigaria a
// repetir transações abortadas por conflito sem ganho de correção aqui.
func (r *Repositorio) NaTransacao(ctx context.Context, fn func(TxLiquidacao) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error { return fn(TxLiquidacao{tx: tx}) })
}

// TravarTransferencia lê a transferência e a trava até o fim da transação.
// Duas entregas da mesma mensagem esperam uma pela outra aqui; a segunda já
// encontra o status final.
func (t TxLiquidacao) TravarTransferencia(ctx context.Context, id string) (domain.Transferencia, error) {
	return lerTransferencia(t.tx.QueryRow(ctx,
		`SELECT `+colunasTransferencia+` FROM transferencias WHERE id = $1 FOR UPDATE`, id))
}

// TravarContas trava as contas uma a uma em ordem crescente de id.
//
// A ordem fixa é o que evita deadlock: se A→B travasse A e depois B enquanto
// B→A trava B e depois A, cada uma esperaria a outra para sempre. Com a
// ordem fixa, as duas tentam A primeiro e uma simplesmente espera.
func (t TxLiquidacao) TravarContas(ctx context.Context, ids ...int64) (map[int64]ContaTravada, error) {
	ordenados := slices.Clone(ids)
	slices.Sort(ordenados)
	ordenados = slices.Compact(ordenados)

	contas := make(map[int64]ContaTravada, len(ordenados))
	for _, id := range ordenados {
		var c ContaTravada
		var saldo int64
		err := t.tx.QueryRow(ctx,
			`SELECT id, saldo_centavos, tecnica FROM contas WHERE id = $1 FOR UPDATE`, id,
		).Scan(&c.ID, &saldo, &c.Tecnica)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("conta %d: %w", id, ErrNaoEncontrada)
		}
		if err != nil {
			return nil, err
		}
		c.Saldo = domain.Centavos(saldo)
		contas[id] = c
	}
	return contas, nil
}

// GravarLancamentos insere os lançamentos e atualiza a projeção de saldo,
// num único envio ao banco.
func (t TxLiquidacao) GravarLancamentos(ctx context.Context, ls []domain.Lancamento) error {
	if len(ls) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, l := range ls {
		b.Queue(`INSERT INTO lancamentos (transferencia_id, conta_id, valor_centavos) VALUES ($1, $2, $3)`,
			l.TransferenciaID, l.ContaID, int64(l.Valor))
		b.Queue(`UPDATE contas SET saldo_centavos = saldo_centavos + $2 WHERE id = $1`,
			l.ContaID, int64(l.Valor))
	}
	return t.tx.SendBatch(ctx, b).Close()
}

// Finalizar grava o status final. A condição status = 'PENDENTE' é redundante
// com a trava, mas protege contra uso fora de ordem.
func (t TxLiquidacao) Finalizar(ctx context.Context, id string, s domain.Status, motivo string) (domain.Transferencia, error) {
	return lerTransferencia(t.tx.QueryRow(ctx, `
		UPDATE transferencias
		   SET status = $2, motivo_rejeicao = nullif($3, ''), finalizada_em = now()
		 WHERE id = $1 AND status = 'PENDENTE'
		RETURNING `+colunasTransferencia, id, string(s), motivo))
}

// ContarPorStatus conta as transferências em cada status.
func (r *Repositorio) ContarPorStatus(ctx context.Context) (map[domain.Status]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT status, count(*) FROM transferencias GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	contagem := map[domain.Status]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		contagem[domain.Status(s)] = n
	}
	return contagem, rows.Err()
}
