package domain

import (
	"errors"
	"math"
	"testing"
)

// FuzzDecidirConservaDinheiro exercita a regra pura com valores variados e
// garante que nenhuma decisão produza dinheiro ou um livro-razão incompleto.
func FuzzDecidirConservaDinheiro(f *testing.F) {
	for _, caso := range []struct {
		valor, saldo int64
		tecnica      bool
	}{
		{1, 0, false},
		{1, 1, false},
		{100_000_000, 99_999_999, false},
		{100_000_000, -1, true},
	} {
		f.Add(caso.valor, caso.saldo, caso.tecnica)
	}

	f.Fuzz(func(t *testing.T, valor, saldo int64, tecnica bool) {
		// Decidir recebe uma transferência já validada.
		if valor <= 0 || valor > int64(ValorMaximo) {
			t.Skip()
		}
		tr := Transferencia{ID: "t", PagadorID: 1, RecebedorID: 2, Valor: Centavos(valor)}
		d := Decidir(tr, Centavos(saldo), tecnica)

		if d.Status == Rejeitada {
			if tecnica || saldo >= valor || len(d.Lancamentos) != 0 || d.Motivo != MotivoSaldoInsuficiente {
				t.Fatalf("rejeição inválida: %+v", d)
			}
			return
		}
		if d.Status != Liquidada || len(d.Lancamentos) != 2 {
			t.Fatalf("decisão inválida: %+v", d)
		}
		if d.Lancamentos[0].Valor+d.Lancamentos[1].Valor != 0 {
			t.Fatalf("lançamentos não conservam dinheiro: %+v", d.Lancamentos)
		}
		if d.Lancamentos[0].ContaID != tr.PagadorID || d.Lancamentos[1].ContaID != tr.RecebedorID {
			t.Fatalf("contas erradas: %+v", d.Lancamentos)
		}
	})
}

func FuzzValidarNuncaAceitaValorForaDoLimite(f *testing.F) {
	for _, valor := range []int64{math.MinInt64, -1, 0, 1, int64(ValorMaximo), int64(ValorMaximo) + 1, math.MaxInt64} {
		f.Add(valor)
	}
	f.Fuzz(func(t *testing.T, valor int64) {
		tr := Transferencia{EndToEndID: e2eValido, PagadorID: 1, RecebedorID: 2, Valor: Centavos(valor)}
		err := tr.Validar()
		fora := valor <= 0 || valor > int64(ValorMaximo)
		if errors.Is(err, ErrValorInvalido) != fora {
			t.Fatalf("valor %d, erro %v", valor, err)
		}
	})
}

func TestCentavosStringNoLimiteMinimo(t *testing.T) {
	const esperado = "-R$ 92233720368547758,08"
	if veio := Centavos(math.MinInt64).String(); veio != esperado {
		t.Fatalf("veio %q, esperava %q", veio, esperado)
	}
}
