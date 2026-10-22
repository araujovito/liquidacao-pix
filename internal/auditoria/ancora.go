package auditoria

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Formato do arquivo de âncoras: uma linha por conta, "conta seq mac-em-hex".
// Linhas em branco e as que começam com # são ignoradas.

// FormatarAncora escreve a âncora de uma cabeça no formato do arquivo.
func FormatarAncora(contaID int64, c Cabeca) string {
	return fmt.Sprintf("%d %d %s", contaID, c.Seq, hex.EncodeToString(c.MAC[:]))
}

// LerAncoras lê um arquivo de âncoras.
func LerAncoras(r io.Reader) ([]Ancora, error) {
	var as []Ancora
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		linha := strings.TrimSpace(sc.Text())
		if linha == "" || strings.HasPrefix(linha, "#") {
			continue
		}
		campos := strings.Fields(linha)
		if len(campos) != 3 {
			return nil, fmt.Errorf("âncora, linha %d: esperado \"conta seq mac\"", n)
		}
		conta, err1 := strconv.ParseInt(campos[0], 10, 64)
		seq, err2 := strconv.ParseInt(campos[1], 10, 64)
		mac, err3 := hex.DecodeString(campos[2])
		if err1 != nil || err2 != nil || err3 != nil || conta <= 0 || seq <= 0 || len(mac) != TamanhoMAC {
			return nil, fmt.Errorf("âncora, linha %d: valores inválidos", n)
		}
		a := Ancora{ContaID: conta, Seq: seq}
		copy(a.MAC[:], mac)
		as = append(as, a)
	}
	return as, sc.Err()
}
