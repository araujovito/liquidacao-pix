package usecase_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

type linhaAuditoria struct {
	seq             int64
	status          string
	mac, anterior   []byte
	igualAoInstante bool
}

func (c *cenario) cadeia(conta int64) []linhaAuditoria {
	c.t.Helper()
	rows, err := c.pool.Query(c.ctx, `
		SELECT a.seq, a.status, a.mac, a.mac_anterior, a.decidida_em = t.finalizada_em
		  FROM auditoria a JOIN transferencias t ON t.id = a.transferencia_id
		 WHERE a.conta_id = $1 ORDER BY a.seq`, conta)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var ls []linhaAuditoria
	for rows.Next() {
		var l linhaAuditoria
		if err := rows.Scan(&l.seq, &l.status, &l.mac, &l.anterior, &l.igualAoInstante); err != nil {
			c.t.Fatal(err)
		}
		ls = append(ls, l)
	}
	if err := rows.Err(); err != nil {
		c.t.Fatal(err)
	}
	return ls
}

func (c *cenario) cabeca(conta int64) (int64, []byte) {
	c.t.Helper()
	var seq int64
	var mac []byte
	if err := c.pool.QueryRow(c.ctx,
		`SELECT auditoria_seq, auditoria_mac FROM contas WHERE id = $1`, conta).Scan(&seq, &mac); err != nil {
		c.t.Fatal(err)
	}
	return seq, mac
}

func TestLiquidacaoAssinaADecisaoNasDuasCadeias(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)

	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 400).ID); err != nil {
		t.Fatal(err)
	}
	rej, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 5_000).ID)
	if err != nil || rej.Status != domain.Rejeitada {
		t.Fatalf("pré-condição: esperava uma rejeição, veio %+v, erro=%v", rej, err)
	}

	for _, conta := range []int64{ana, beto} {
		ls := c.cadeia(conta)
		if len(ls) == 0 {
			t.Fatalf("conta %d sem entradas", conta)
		}
		ant := make([]byte, auditoria.TamanhoMAC) // a primeira entrada parte de zeros
		for i, l := range ls {
			if l.seq != int64(i+1) {
				t.Errorf("conta %d: seq %d na posição %d", conta, l.seq, i)
			}
			if !bytes.Equal(l.anterior, ant) {
				t.Errorf("conta %d seq %d: elo anterior não é o MAC da entrada anterior", conta, l.seq)
			}
			if !l.igualAoInstante {
				t.Errorf("conta %d seq %d: decidida_em difere de finalizada_em", conta, l.seq)
			}
			ant = l.mac
		}
		seq, mac := c.cabeca(conta)
		if seq != int64(len(ls)) || !bytes.Equal(mac, ant) {
			t.Errorf("conta %d: cabeça (%d) não aponta para a última entrada (%d)", conta, seq, len(ls))
		}
	}
	if n := len(c.cadeia(ana)); n != 3 {
		t.Errorf("ana tem %d entradas, esperado 3 (aporte, liquidada, rejeitada)", n)
	}
	if n := len(c.cadeia(beto)); n != 2 {
		t.Errorf("beto tem %d entradas, esperado 2 (liquidada, rejeitada)", n)
	}
}

func TestLiquidarDeNovoNaoRepeteEntrada(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)
	tr := c.pedir(ana, beto, 400)
	for range 3 {
		if _, err := c.liq.Liquidar(c.ctx, tr.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(c.cadeia(beto)); n != 1 {
		t.Fatalf("beto tem %d entradas depois de 3 liquidações da mesma transferência, esperado 1", n)
	}
}

func TestSemChaveiroNaoLiquida(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)
	tr := c.pedir(ana, beto, 400)

	semChave := usecase.Liquidador{Repo: c.repo}
	if _, err := semChave.Liquidar(c.ctx, tr.ID); !errors.Is(err, auditoria.ErrSemChaveiro) {
		t.Fatalf("erro = %v, esperado ErrSemChaveiro", err)
	}
	g, err := c.repo.BuscarTransferencia(c.ctx, tr.ID)
	if err != nil || g.Status != domain.Pendente {
		t.Fatalf("a transferência saiu de PENDENTE sem trilha: %+v, erro=%v", g, err)
	}
}

// Se gravar a auditoria falha, a liquidação inteira desfaz: saldo, lançamentos
// e status. A falha é provocada ocupando a posição que a próxima entrada de
// beto usaria, com uma linha que a cabeça da conta ainda não conhece.
func TestFalhaNaAuditoriaDesfazALiquidacao(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 100).ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.pool.Exec(c.ctx, `
		INSERT INTO auditoria (conta_id, seq, transferencia_id, status, pagador_id, recebedor_id,
		                       valor_centavos, decidida_em, chave_id, mac_anterior, mac)
		SELECT $1, 2, transferencia_id, status, pagador_id, recebedor_id,
		       valor_centavos, decidida_em, chave_id, mac_anterior, mac
		  FROM auditoria WHERE conta_id = $2 AND seq = 1`, beto, ana); err != nil {
		t.Fatal(err)
	}
	var lancamentos int
	if err := c.pool.QueryRow(c.ctx, `SELECT count(*) FROM lancamentos`).Scan(&lancamentos); err != nil {
		t.Fatal(err)
	}
	saldoAna, saldoBeto := c.saldo(ana), c.saldo(beto)

	tr := c.pedir(ana, beto, 200)
	if _, err := c.liq.Liquidar(c.ctx, tr.ID); err == nil {
		t.Fatal("Liquidar não falhou com a posição da cadeia ocupada")
	}

	g, err := c.repo.BuscarTransferencia(c.ctx, tr.ID)
	if err != nil || g.Status != domain.Pendente {
		t.Fatalf("a transferência saiu de PENDENTE apesar da falha: %+v, erro=%v", g, err)
	}
	var depois int
	if err := c.pool.QueryRow(c.ctx, `SELECT count(*) FROM lancamentos`).Scan(&depois); err != nil {
		t.Fatal(err)
	}
	if depois != lancamentos || c.saldo(ana) != saldoAna || c.saldo(beto) != saldoBeto {
		t.Fatal("a falha na auditoria deixou lançamento ou saldo para trás")
	}
}
