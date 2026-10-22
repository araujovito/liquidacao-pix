package usecase_test

import (
	"log/slog"
	"testing"

	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

// A conciliação existe para detectar o que não deveria acontecer. Aqui o
// "impossível" é provocado de propósito: mexer à mão no saldo projetado de uma
// conta. Os lançamentos são imutáveis (gatilho), então a única forma de
// divergir é a projeção sair da soma dos lançamentos, e a conciliação precisa
// acusar.
func TestConciliadorUmaFechadaEDepoisDetectaSaldoAdulterado(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 300).ID); err != nil {
		t.Fatal(err)
	}
	conc := usecase.Conciliador{Repo: c.repo, Log: slog.New(slog.DiscardHandler)}

	n, err := conc.Uma(c.ctx)
	if err != nil || n != 0 {
		t.Fatalf("banco íntegro: divergências=%d, erro=%v", n, err)
	}

	if _, err := c.pool.Exec(c.ctx, `UPDATE contas SET saldo_centavos = saldo_centavos + 1 WHERE id = $1`, beto); err != nil {
		t.Fatal(err)
	}
	n, err = conc.Uma(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("saldo adulterado em 1 centavo não foi detectado pela conciliação")
	}
}
