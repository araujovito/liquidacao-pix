package auditoria

import (
	"strings"
	"testing"
)

func TestAncoraIdaEVolta(t *testing.T) {
	var c Cabeca
	c.Seq = 12
	for i := range c.MAC {
		c.MAC[i] = byte(i)
	}
	linha := FormatarAncora(7, c)
	as, err := LerAncoras(strings.NewReader("# cabeças de 2026-10-22\n\n" + linha + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || as[0].ContaID != 7 || as[0].Seq != 12 || as[0].MAC != c.MAC {
		t.Fatalf("leitura difere do que foi formatado: %+v", as)
	}
}

func TestAncoraRecusaLinhaMalformada(t *testing.T) {
	ok := strings.Repeat("ab", TamanhoMAC)
	for nome, linha := range map[string]string{
		"campos a menos": "7 12",
		"campos a mais":  "7 12 " + ok + " x",
		"conta zero":     "0 12 " + ok,
		"seq zero":       "7 0 " + ok,
		"seq negativa":   "7 -1 " + ok,
		"mac curto":      "7 12 abcd",
		"mac não hex":    "7 12 " + strings.Repeat("zz", TamanhoMAC),
	} {
		t.Run(nome, func(t *testing.T) {
			if _, err := LerAncoras(strings.NewReader(linha)); err == nil {
				t.Fatalf("aceitou %q", linha)
			}
		})
	}
}
