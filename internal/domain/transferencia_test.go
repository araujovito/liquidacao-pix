package domain

import (
	"errors"
	"testing"
)

const e2eValido = "E1234567820261021143000000000001"

func TestValidar(t *testing.T) {
	base := Transferencia{EndToEndID: e2eValido, PagadorID: 1, RecebedorID: 2, Valor: 100}
	casos := []struct {
		nome   string
		mudar  func(*Transferencia)
		espera error
	}{
		{"válida", func(*Transferencia) {}, nil},
		{"e2e curto", func(t *Transferencia) { t.EndToEndID = "E123" }, ErrEndToEndInvalido},
		{"e2e sem E", func(t *Transferencia) { t.EndToEndID = "X" + e2eValido[1:] }, ErrEndToEndInvalido},
		{"valor zero", func(t *Transferencia) { t.Valor = 0 }, ErrValorInvalido},
		{"valor negativo", func(t *Transferencia) { t.Valor = -1 }, ErrValorInvalido},
		{"acima do teto", func(t *Transferencia) { t.Valor = ValorMaximo + 1 }, ErrValorInvalido},
		{"no teto", func(t *Transferencia) { t.Valor = ValorMaximo }, nil},
		{"mesma conta", func(t *Transferencia) { t.RecebedorID = 1 }, ErrMesmaConta},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			tr := base
			c.mudar(&tr)
			err := tr.Validar()
			if c.espera == nil && err != nil {
				t.Fatalf("esperava válida, veio %v", err)
			}
			if c.espera != nil && !errors.Is(err, c.espera) {
				t.Fatalf("esperava %v, veio %v", c.espera, err)
			}
		})
	}
}

func TestValidarJuntaTodosOsErros(t *testing.T) {
	err := Transferencia{EndToEndID: "x", PagadorID: 1, RecebedorID: 1}.Validar()
	for _, e := range []error{ErrEndToEndInvalido, ErrValorInvalido, ErrMesmaConta} {
		if !errors.Is(err, e) {
			t.Errorf("faltou %v em %v", e, err)
		}
	}
}

func TestDecidir(t *testing.T) {
	tr := Transferencia{ID: "t1", PagadorID: 1, RecebedorID: 2, Valor: 500}

	t.Run("saldo exato liquida", func(t *testing.T) {
		d := Decidir(tr, 500, false)
		if d.Status != Liquidada {
			t.Fatalf("status %s", d.Status)
		}
		var soma Centavos
		for _, l := range d.Lancamentos {
			soma += l.Valor
		}
		if soma != 0 || len(d.Lancamentos) != 2 {
			t.Fatalf("partidas dobradas violadas: %+v", d.Lancamentos)
		}
	})

	t.Run("um centavo a menos rejeita", func(t *testing.T) {
		d := Decidir(tr, 499, false)
		if d.Status != Rejeitada || d.Motivo != MotivoSaldoInsuficiente || len(d.Lancamentos) != 0 {
			t.Fatalf("decisão inesperada: %+v", d)
		}
	})

	t.Run("conta técnica pode ficar negativa", func(t *testing.T) {
		if d := Decidir(tr, -1_000, true); d.Status != Liquidada {
			t.Fatalf("status %s", d.Status)
		}
	})
}

func TestCentavosString(t *testing.T) {
	for v, s := range map[Centavos]string{0: "R$ 0,00", 5: "R$ 0,05", 123456: "R$ 1234,56", -250: "-R$ 2,50"} {
		if v.String() != s {
			t.Errorf("%d: %q, esperava %q", int64(v), v.String(), s)
		}
	}
}
