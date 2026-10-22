package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
)

// LerAuditoria entrega, conta por conta e em ordem de id, a cadeia de
// auditoria como está no banco, junto com as decisões finais que ficaram sem
// entrada. Tudo numa única fotografia REPEATABLE READ somente leitura, para
// que a verificação não veja uma liquidação pela metade.
//
// Mantém na memória uma conta por vez. O banco inteiro de uma vez seria
// proibitivo; uma conta com milhões de entradas ainda é um limite, aceito aqui.
func (r *Repositorio) LerAuditoria(ctx context.Context, fn func(auditoria.ContaLida) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // só leitura

	semEntrada, err := decisoesSemEntrada(ctx, tx)
	if err != nil {
		return err
	}

	contas, err := tx.Query(ctx, `SELECT id, auditoria_seq, auditoria_mac FROM contas ORDER BY id`)
	if err != nil {
		return err
	}
	var cabecas []auditoria.ContaLida
	for contas.Next() {
		var c auditoria.ContaLida
		var mac []byte
		if err := contas.Scan(&c.ID, &c.Cabeca.Seq, &mac); err != nil {
			contas.Close()
			return err
		}
		if len(mac) != auditoria.TamanhoMAC {
			contas.Close()
			return fmt.Errorf("conta %d: cabeça da auditoria com %d bytes", c.ID, len(mac))
		}
		copy(c.Cabeca.MAC[:], mac)
		c.SemEntrada = semEntrada[c.ID]
		cabecas = append(cabecas, c)
	}
	contas.Close()
	if err := contas.Err(); err != nil {
		return err
	}

	// LEFT JOIN: uma entrada cuja transferência sumiu (chave estrangeira
	// desligada por quem tem acesso total) aparece como achado, não como erro.
	linhas, err := tx.Query(ctx, `
		SELECT a.conta_id, a.seq, a.transferencia_id, a.status, a.motivo,
		       a.pagador_id, a.recebedor_id, a.valor_centavos, a.decidida_em,
		       a.chave_id, a.mac_anterior, a.mac,
		       t.id IS NOT NULL, coalesce(t.status, ''), t.motivo_rejeicao,
		       coalesce(t.pagador_id, 0), coalesce(t.recebedor_id, 0),
		       coalesce(t.valor_centavos, 0), t.finalizada_em
		  FROM auditoria a LEFT JOIN transferencias t ON t.id = a.transferencia_id
		 ORDER BY a.conta_id, a.seq`)
	if err != nil {
		return err
	}
	defer linhas.Close()

	i := 0 // índice em cabecas
	var corrente *auditoria.ContaLida
	fechar := func() error {
		if corrente == nil {
			return nil
		}
		err := fn(*corrente)
		corrente = nil
		return err
	}
	// Avança até a conta da linha, entregando as que não têm nenhuma entrada.
	avancar := func(id int64) error {
		for i < len(cabecas) && cabecas[i].ID < id {
			if err := fechar(); err != nil {
				return err
			}
			c := cabecas[i]
			corrente = &c
			i++
			if err := fechar(); err != nil {
				return err
			}
		}
		return nil
	}
	for linhas.Next() {
		var l auditoria.EntradaLida
		var tid uuid.UUID
		var anterior, mac []byte
		var tMotivo *string
		if err := linhas.Scan(&l.ContaID, &l.Seq, &tid, &l.Status, &l.Motivo,
			&l.PagadorID, &l.RecebedorID, &l.ValorCentavos, &l.DecididaEm,
			&l.ChaveID, &anterior, &mac,
			&l.Transferencia.Existe, &l.Transferencia.Status, &tMotivo,
			&l.Transferencia.PagadorID, &l.Transferencia.RecebedorID,
			&l.Transferencia.Valor, &l.Transferencia.FinalizadaEm); err != nil {
			return err
		}
		l.TransferenciaID = tid
		l.Transferencia.Motivo = tMotivo
		copy(l.MACAnterior[:], anterior)
		copy(l.MAC[:], mac)

		if corrente == nil || corrente.ID != l.ContaID {
			if err := fechar(); err != nil {
				return err
			}
			if err := avancar(l.ContaID); err != nil {
				return err
			}
			if i >= len(cabecas) || cabecas[i].ID != l.ContaID {
				return fmt.Errorf("auditoria: entrada da conta %d, que não existe", l.ContaID)
			}
			c := cabecas[i]
			corrente = &c
			i++
		}
		corrente.Entradas = append(corrente.Entradas, l)
	}
	if err := linhas.Err(); err != nil {
		return err
	}
	if err := fechar(); err != nil {
		return err
	}
	return avancar(1<<62 - 1)
}

// decisoesSemEntrada acha, por conta, as transferências finais em que ela é
// parte e que não têm entrada na cadeia dela. Pega o apagamento de entradas
// recentes enquanto a transferência continua lá (lançamentos impedem apagar
// uma transferência liquidada).
func decisoesSemEntrada(ctx context.Context, tx pgx.Tx) (map[int64][]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.conta_id, t.id
		  FROM transferencias t
		 CROSS JOIN LATERAL (VALUES (t.pagador_id), (t.recebedor_id)) AS p(conta_id)
		 WHERE t.status IN ('LIQUIDADA', 'REJEITADA')
		   AND NOT EXISTS (SELECT 1 FROM auditoria a
		                    WHERE a.conta_id = p.conta_id AND a.transferencia_id = t.id)
		 ORDER BY p.conta_id, t.finalizada_em, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64][]uuid.UUID{}
	for rows.Next() {
		var conta int64
		var id uuid.UUID
		if err := rows.Scan(&conta, &id); err != nil {
			return nil, err
		}
		m[conta] = append(m[conta], id)
	}
	return m, rows.Err()
}
