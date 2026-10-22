package dadospessoais

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func mestras(t *testing.T) *Mestras {
	t.Helper()
	m, err := NovasMestras("m1", map[string][]byte{"m1": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNormalizarCPF(t *testing.T) {
	validos := map[string]string{
		"52998224725":    "52998224725",
		"529.982.247-25": "52998224725",
		"111.444.777-35": "11144477735",
	}
	for in, quer := range validos {
		if got, err := NormalizarCPF(in); err != nil || got != quer {
			t.Errorf("NormalizarCPF(%q) = %q, %v; esperado %q", in, got, err, quer)
		}
	}
	for _, in := range []string{
		"", "123", "529.982.247-26", "52998224724", "11111111111", "00000000000",
		"529982247250", "5299822472a", "529/982/247-25",
	} {
		if _, err := NormalizarCPF(in); !errors.Is(err, ErrCPFInvalido) {
			t.Errorf("NormalizarCPF(%q) aceitou: %v", in, err)
		}
	}
}

func TestMascararNaoMostraTudo(t *testing.T) {
	m := Mascarar("52998224725")
	if m != "***.982.247-**" {
		t.Fatalf("máscara = %q", m)
	}
	if Mascarar("123") != "***" {
		t.Fatal("entrada fora do formato deveria ser coberta por inteiro")
	}
}

func TestIdaEVolta(t *testing.T) {
	m := mestras(t)
	s, err := m.Cifrar(7, "52998224725")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CPFCifrado) != 12+11+16 || len(s.NonceEmbrulho) != 12 || len(s.ChaveEmbrulhada) != 32+16 {
		t.Fatalf("tamanhos inesperados: cpf=%d nonce=%d chave=%d", len(s.CPFCifrado), len(s.NonceEmbrulho), len(s.ChaveEmbrulhada))
	}
	for nome, b := range map[string][]byte{"cpf": s.CPFCifrado, "chave": s.ChaveEmbrulhada} {
		if bytes.Contains(b, []byte("52998224725")) {
			t.Errorf("%s cifrado contém o CPF em claro", nome)
		}
	}
	got, err := m.Decifrar(7, s)
	if err != nil || got != "52998224725" {
		t.Fatalf("Decifrar = %q, %v", got, err)
	}
}

func TestMesmoCPFCifraDiferenteAcadaVez(t *testing.T) {
	m := mestras(t)
	a, _ := m.Cifrar(7, "52998224725")
	b, _ := m.Cifrar(7, "52998224725")
	if bytes.Equal(a.CPFCifrado, b.CPFCifrado) || bytes.Equal(a.ChaveEmbrulhada, b.ChaveEmbrulhada) ||
		bytes.Equal(a.NonceEmbrulho, b.NonceEmbrulho) {
		t.Fatal("duas cifragens do mesmo CPF coincidiram: chave ou nonce repetidos")
	}
}

func TestAdulteracaoECopiaEntreContasFalham(t *testing.T) {
	m := mestras(t)
	s, _ := m.Cifrar(7, "52998224725")
	outra, _ := m.Cifrar(8, "11144477735")

	troca := func(f func(*Sigilo)) Sigilo {
		c := Sigilo{
			CPFCifrado:      bytes.Clone(s.CPFCifrado),
			MestraID:        s.MestraID,
			NonceEmbrulho:   bytes.Clone(s.NonceEmbrulho),
			ChaveEmbrulhada: bytes.Clone(s.ChaveEmbrulhada),
		}
		f(&c)
		return c
	}
	casos := map[string]struct {
		conta int64
		s     Sigilo
	}{
		"conta errada (CPF copiado para outra conta)": {8, s},
		"bit do CPF cifrado":                          {7, troca(func(c *Sigilo) { c.CPFCifrado[14] ^= 1 })},
		"nonce do CPF":                                {7, troca(func(c *Sigilo) { c.CPFCifrado[0] ^= 1 })},
		"tag do CPF":                                  {7, troca(func(c *Sigilo) { c.CPFCifrado[len(c.CPFCifrado)-1] ^= 1 })},
		"bit da chave embrulhada":                     {7, troca(func(c *Sigilo) { c.ChaveEmbrulhada[3] ^= 1 })},
		"nonce do embrulho":                           {7, troca(func(c *Sigilo) { c.NonceEmbrulho[0] ^= 1 })},
		"chave embrulhada de outra conta": {7, troca(func(c *Sigilo) {
			c.NonceEmbrulho, c.ChaveEmbrulhada = outra.NonceEmbrulho, outra.ChaveEmbrulhada
		})},
		"CPF cifrado de outra conta, mesma chave": {7, troca(func(c *Sigilo) { c.CPFCifrado = outra.CPFCifrado })},
		"truncado": {7, troca(func(c *Sigilo) { c.CPFCifrado = c.CPFCifrado[:20] })},
		"vazio":    {7, Sigilo{MestraID: "m1"}},
	}
	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			if got, err := m.Decifrar(c.conta, c.s); !errors.Is(err, ErrDecifragem) {
				t.Fatalf("Decifrar = %q, %v; esperado ErrDecifragem", got, err)
			}
		})
	}
}

// O identificador da mestra faz parte do dado autenticado: apontar a chave
// embrulhada para outra mestra (mesmo com os mesmos bytes) não decifra.
func TestTrocarOIdDaMestraFalha(t *testing.T) {
	k := bytes.Repeat([]byte{1}, 32)
	m, _ := NovasMestras("m1", map[string][]byte{"m1": k, "m2": k})
	s, _ := m.Cifrar(7, "52998224725")
	s.MestraID = "m2"
	if _, err := m.Decifrar(7, s); !errors.Is(err, ErrDecifragem) {
		t.Fatalf("erro = %v, esperado ErrDecifragem", err)
	}
}

func TestRotacaoDaMestra(t *testing.T) {
	velha := mestras(t)
	s, _ := velha.Cifrar(7, "52998224725")
	nova, err := NovasMestras("m2", map[string][]byte{
		"m1": bytes.Repeat([]byte{1}, 32), "m2": bytes.Repeat([]byte{2}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := nova.Decifrar(7, s); err != nil || got != "52998224725" {
		t.Fatalf("CPF antigo não decifra depois da rotação: %q, %v", got, err)
	}
	s2, _ := nova.Cifrar(8, "11144477735")
	if s2.MestraID != "m2" {
		t.Fatalf("embrulhou com %q, esperado a atual m2", s2.MestraID)
	}
	if _, err := velha.Decifrar(8, s2); !errors.Is(err, ErrMestraDesconhecida) {
		t.Fatalf("erro = %v, esperado ErrMestraDesconhecida", err)
	}
}

type fonteQueFalha struct{}

func (fonteQueFalha) Read([]byte) (int, error) { return 0, errors.New("sem entropia") }

func TestSemEntropiaNaoProduzNada(t *testing.T) {
	m := mestras(t).ComFonteAleatoria(fonteQueFalha{})
	if s, err := m.Cifrar(7, "52998224725"); err == nil || s.CPFCifrado != nil {
		t.Fatalf("Cifrar com fonte quebrada devolveu %+v, %v", s, err)
	}
}

func TestCifrarRecusaCPFNaoNormalizado(t *testing.T) {
	if _, err := mestras(t).Cifrar(7, "529.982.247-25"); err == nil {
		t.Fatal("aceitou CPF com pontuação")
	}
}

func TestMestrasDoAmbiente(t *testing.T) {
	h := strings.Repeat("ab", 32)
	t.Setenv("CPF_CHAVES_MESTRAS", "m1:"+h)
	t.Setenv("CPF_CHAVE_MESTRA_ATUAL", "m1")
	if _, err := MestrasDoAmbiente(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPF_CHAVE_MESTRA_ATUAL", "")
	if _, err := MestrasDoAmbiente(); err == nil {
		t.Fatal("aceitou sem a chave atual")
	}
	t.Setenv("CPF_CHAVES_MESTRAS", "m1:zz"+h[2:])
	t.Setenv("CPF_CHAVE_MESTRA_ATUAL", "m1")
	if _, err := MestrasDoAmbiente(); err == nil || strings.Contains(err.Error(), h[2:]) {
		t.Fatalf("erro = %v: deveria falhar sem repetir a chave", err)
	}
}
