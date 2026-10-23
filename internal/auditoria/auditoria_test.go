package auditoria

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func chaveDeTeste() []byte {
	k := make([]byte, TamanhoChave)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func entradaBase(t *testing.T) Entrada {
	t.Helper()
	var ant [TamanhoMAC]byte
	for i := range ant {
		ant[i] = 0xAA
	}
	return Entrada{
		ContaID: 7, Seq: 3,
		TransferenciaID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Status:          StatusLiquidada,
		PagadorID:       10, RecebedorID: 20, ValorCentavos: 2500,
		DecididaEm:  time.Date(2026, 10, 22, 12, 0, 0, 123456000, time.UTC),
		ChaveID:     "v1",
		MACAnterior: ant,
	}
}

func chaveiro(t *testing.T) *Chaveiro {
	t.Helper()
	c, err := NovoChaveiro("v1", map[string][]byte{"v1": chaveDeTeste()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Vetores calculados à parte, em Python (struct + hmac), a partir da
// especificação do ADR 0006 e sem usar este código. Se uma refatoração mudar a
// representação dos bytes assinados, estes testes quebram.
func TestVetoresFixos(t *testing.T) {
	motivo := "saldo insuficiente"
	casos := []struct {
		nome       string
		mutar      func(*Entrada)
		bytes, mac string
	}{
		{
			"liquidada", func(*Entrada) {},
			"000000107069782d61756469746f7269612f76310000000000000007000000000000000311111111222233334444555555555555000000094c495155494441444100000000000000000a000000000000001400000000000009c400065e6c9a661240000000027631aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"22035dc20e6f8468150302315fd173bf362f9ebd06d79ee9b3e50e0ed2e16a18",
		},
		{
			"rejeitada com motivo", func(e *Entrada) { e.Status = StatusRejeitada; e.Motivo = &motivo },
			"000000107069782d61756469746f7269612f763100000000000000070000000000000003111111112222333344445555555555550000000952454a454954414441010000001273616c646f20696e737566696369656e7465000000000000000a000000000000001400000000000009c400065e6c9a661240000000027631aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"388b4c69a7bd4a844e411dc34c2d18a30614fdac874f2eb2b82850a9d340c720",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			e := entradaBase(t)
			c.mutar(&e)
			if got := hex.EncodeToString(e.Codificar()); got != c.bytes {
				t.Fatalf("bytes assinados mudaram:\n got %s\nwant %s", got, c.bytes)
			}
			if err := chaveiro(t).Assinar(&e); err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(e.MAC[:]); got != c.mac {
				t.Fatalf("MAC = %s, esperado %s", got, c.mac)
			}
		})
	}
}

// Cada campo assinado tem de alterar o MAC: se um campo ficasse de fora, dava
// para alterá-lo no banco sem a verificação perceber.
func TestTodoCampoAlteraOMAC(t *testing.T) {
	c := chaveiro(t)
	base := entradaBase(t)
	if err := c.Assinar(&base); err != nil {
		t.Fatal(err)
	}
	outroMotivo := "x"
	mutacoes := map[string]func(*Entrada){
		"conta":         func(e *Entrada) { e.ContaID++ },
		"seq":           func(e *Entrada) { e.Seq++ },
		"transferencia": func(e *Entrada) { e.TransferenciaID[15] ^= 1 },
		"pagador":       func(e *Entrada) { e.PagadorID++ },
		"recebedor":     func(e *Entrada) { e.RecebedorID++ },
		"valor":         func(e *Entrada) { e.ValorCentavos++ },
		"decidida_em":   func(e *Entrada) { e.DecididaEm = e.DecididaEm.Add(time.Microsecond) },
		"mac_anterior":  func(e *Entrada) { e.MACAnterior[0] ^= 1 },
		"status e motivo": func(e *Entrada) {
			e.Status = StatusRejeitada
			e.Motivo = &outroMotivo
		},
	}
	for nome, mutar := range mutacoes {
		t.Run(nome, func(t *testing.T) {
			e := base
			mutar(&e)
			if err := c.Conferir(e); !errors.Is(err, ErrMACInvalido) {
				t.Fatalf("Conferir = %v, esperado ErrMACInvalido", err)
			}
		})
	}
	t.Run("chave_id", func(t *testing.T) {
		// Trocar o identificador para outro que existe no chaveiro, com a
		// mesma chave, ainda invalida o MAC: o id faz parte do conteúdo.
		c2, _ := NovoChaveiro("v1", map[string][]byte{"v1": chaveDeTeste(), "v2": chaveDeTeste()})
		e := base
		e.ChaveID = "v2"
		if err := c2.Conferir(e); !errors.Is(err, ErrMACInvalido) {
			t.Fatalf("Conferir = %v, esperado ErrMACInvalido", err)
		}
	})
	if err := c.Conferir(base); err != nil {
		t.Fatalf("entrada intacta não confere: %v", err)
	}
}

// Sem delimitadores, motivo ausente e motivo vazio, ou textos que "se
// deslocam" entre campos vizinhos, não podem gerar os mesmos bytes.
func TestCodificacaoNaoEAmbigua(t *testing.T) {
	vazio := ""
	a, b := entradaBase(t), entradaBase(t)
	a.Status, a.Motivo = StatusRejeitada, nil
	b.Status, b.Motivo = StatusRejeitada, &vazio
	if bytes.Equal(a.Codificar(), b.Codificar()) {
		t.Fatal("motivo ausente e motivo vazio geram os mesmos bytes")
	}
	x, y := entradaBase(t), entradaBase(t)
	m1, m2 := "ab", "b"
	x.Status, x.Motivo, x.ChaveID = StatusRejeitada, &m1, "c"
	y.Status, y.Motivo, y.ChaveID = StatusRejeitada, &m2, "ac"
	if bytes.Equal(x.Codificar(), y.Codificar()) {
		t.Fatal("texto deslocado entre motivo e chave_id gera os mesmos bytes")
	}
}

func TestValidarRecusaDecisaoImpossivel(t *testing.T) {
	m := "x"
	casos := map[string]func(*Entrada){
		"conta zero":                func(e *Entrada) { e.ContaID = 0 },
		"seq zero":                  func(e *Entrada) { e.Seq = 0 },
		"status pendente":           func(e *Entrada) { e.Status = "PENDENTE" },
		"liquidada com motivo":      func(e *Entrada) { e.Motivo = &m },
		"rejeitada sem motivo":      func(e *Entrada) { e.Status = StatusRejeitada },
		"instante em nanossegundos": func(e *Entrada) { e.DecididaEm = e.DecididaEm.Add(time.Nanosecond) },
	}
	for nome, mutar := range casos {
		t.Run(nome, func(t *testing.T) {
			e := entradaBase(t)
			mutar(&e)
			if err := chaveiro(t).Assinar(&e); err == nil {
				t.Fatal("Assinar aceitou uma entrada inválida")
			}
		})
	}
}

func TestRotacaoNaoQuebraOPassado(t *testing.T) {
	k2 := bytes.Repeat([]byte{0x42}, TamanhoChave)
	antigo := chaveiro(t)
	e := entradaBase(t)
	if err := antigo.Assinar(&e); err != nil {
		t.Fatal(err)
	}
	novo, err := NovoChaveiro("v2", map[string][]byte{"v1": chaveDeTeste(), "v2": k2})
	if err != nil {
		t.Fatal(err)
	}
	if err := novo.Conferir(e); err != nil {
		t.Fatalf("entrada assinada por v1 não confere depois da rotação: %v", err)
	}
	e2 := entradaBase(t)
	e2.Seq = 4
	if err := novo.Assinar(&e2); err != nil {
		t.Fatal(err)
	}
	if e2.ChaveID != "v2" {
		t.Fatalf("assinou com %q, esperado a chave atual v2", e2.ChaveID)
	}
	// Quem só tem a chave antiga não confere a nova.
	if err := antigo.Conferir(e2); !errors.Is(err, ErrChaveDesconhecida) {
		t.Fatalf("Conferir = %v, esperado ErrChaveDesconhecida", err)
	}
}

func TestChaveiroDoAmbiente(t *testing.T) {
	h := hex.EncodeToString(chaveDeTeste())
	curta := hex.EncodeToString(chaveDeTeste()[:16])
	casos := []struct {
		nome, chaves, atual string
		ok                  bool
	}{
		{"uma chave", "v1:" + h, "v1", true},
		{"duas chaves, a segunda atual", "v1:" + h + ",v2:" + h, "v2", true},
		{"sem variáveis", "", "", false},
		{"sem atual", "v1:" + h, "", false},
		{"atual inexistente", "v1:" + h, "v9", false},
		{"sem dois-pontos", h, "v1", false},
		{"não é hexadecimal", "v1:zz" + h[2:], "v1", false},
		{"chave curta", "v1:" + curta, "v1", false},
		{"id repetido", "v1:" + h + ",v1:" + h, "v1", false},
		{"id inválido", "v 1:" + h, "v 1", false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Setenv("AUDITORIA_CHAVES", c.chaves)
			t.Setenv("AUDITORIA_CHAVE_ATUAL", c.atual)
			got, err := ChaveiroDoAmbiente()
			if (err == nil) != c.ok {
				t.Fatalf("erro = %v, ok esperado = %v", err, c.ok)
			}
			if err != nil && strings.Contains(err.Error(), h) {
				t.Fatalf("a mensagem de erro vaza a chave: %v", err)
			}
			if err == nil && got.Atual() != c.atual {
				t.Fatalf("Atual = %q, esperado %q", got.Atual(), c.atual)
			}
		})
	}
}

func TestDefeitoNoMACAnteriorPreservaMACAtual(t *testing.T) {
	ch, err := NovoChaveiro("v1", map[string][]byte{"v1": chaveDeTeste()})
	if err != nil {
		t.Fatal(err)
	}
	motivo := "SALDO_INSUFICIENTE"
	instante := time.Date(2026, 10, 22, 20, 0, 0, 123000, time.UTC)
	e := Entrada{
		ContaID: 7, Seq: 1, TransferenciaID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Status: StatusRejeitada, Motivo: &motivo, PagadorID: 7, RecebedorID: 8,
		ValorCentavos: 100, DecididaEm: instante,
	}
	if err := ch.Assinar(&e); err != nil {
		t.Fatal(err)
	}
	decisao := Decisao{Existe: true, Status: e.Status, Motivo: e.Motivo, PagadorID: e.PagadorID,
		RecebedorID: e.RecebedorID, Valor: e.ValorCentavos, FinalizadaEm: &instante}
	conta := ContaLida{
		ID: e.ContaID, Cabeca: Cabeca{Seq: e.Seq, MAC: e.MAC},
		Entradas: []EntradaLida{{Entrada: e, Transferencia: decisao,
			Defeito: "mac_anterior com tamanho inválido", MACLido: true}},
	}
	ancora := Ancora{ContaID: e.ContaID, Seq: e.Seq, MAC: e.MAC}
	ps := ch.VerificarConta(conta, []Ancora{ancora})
	if len(ps) != 1 || ps[0].Tipo != ProblemaMAC {
		t.Fatalf("problemas = %v, esperado apenas o formato inválido do MAC anterior", ps)
	}
}
