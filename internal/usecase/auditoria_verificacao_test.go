package usecase_test

import (
	"slices"
	"testing"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
)

// verificar roda o mesmo caminho do cmd/auditar: lê o banco e confere com o
// chaveiro dado. Devolve os achados de todas as contas.
func (c *cenario) verificar(ch *auditoria.Chaveiro, ancoras ...auditoria.Ancora) []auditoria.Problema {
	c.t.Helper()
	var ps []auditoria.Problema
	err := c.repo.LerAuditoria(c.ctx, func(l auditoria.ContaLida) error {
		ps = append(ps, ch.VerificarConta(l, ancoras)...)
		return nil
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return ps
}

func (c *cenario) ancorar(conta int64) auditoria.Ancora {
	c.t.Helper()
	seq, mac := c.cabeca(conta)
	a := auditoria.Ancora{ContaID: conta, Seq: seq}
	copy(a.MAC[:], mac)
	return a
}

// adulterar roda SQL com os gatilhos de imutabilidade desligados, como faria
// quem tem acesso total ao banco. O HMAC é a defesa contra isso.
func (c *cenario) adulterar(sql string, args ...any) {
	c.t.Helper()
	for _, cmd := range []string{
		`ALTER TABLE auditoria DISABLE TRIGGER USER`,
		`ALTER TABLE lancamentos DISABLE TRIGGER USER`,
	} {
		if _, err := c.pool.Exec(c.ctx, cmd); err != nil {
			c.t.Fatal(err)
		}
	}
	if _, err := c.pool.Exec(c.ctx, sql, args...); err != nil {
		c.t.Fatalf("adulteração %q: %v", sql, err)
	}
	for _, cmd := range []string{
		`ALTER TABLE auditoria ENABLE TRIGGER USER`,
		`ALTER TABLE lancamentos ENABLE TRIGGER USER`,
	} {
		if _, err := c.pool.Exec(c.ctx, cmd); err != nil {
			c.t.Fatal(err)
		}
	}
}

func tipos(ps []auditoria.Problema) []string {
	var ts []string
	for _, p := range ps {
		if !slices.Contains(ts, p.Tipo) {
			ts = append(ts, p.Tipo)
		}
	}
	slices.Sort(ts)
	return ts
}

// cadeiaDeTres monta ana→beto três vezes: a cadeia de beto fica com 3 entradas.
func cadeiaDeTres(t *testing.T) (c *cenario, ana, beto int64, ch *auditoria.Chaveiro) {
	c = novoCenario(t)
	ch = c.liq.Auditoria
	ana, beto = c.conta("ana", 10_000), c.conta("beto", 0)
	for _, v := range []domain.Centavos{100, 200, 300} {
		if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, v).ID); err != nil {
			t.Fatal(err)
		}
	}
	if ps := c.verificar(ch); len(ps) != 0 {
		t.Fatalf("a cadeia íntegra foi acusada: %v", ps)
	}
	return c, ana, beto, ch
}

func esperarSo(t *testing.T, ps []auditoria.Problema, quer ...string) {
	t.Helper()
	slices.Sort(quer)
	if got := tipos(ps); !slices.Equal(got, quer) {
		t.Fatalf("tipos de problema = %v, esperado %v\n%v", got, quer, ps)
	}
}

func TestAdulteracaoDeUmCampoApontaOndeFoi(t *testing.T) {
	c, _, beto, ch := cadeiaDeTres(t)
	// Muda o valor da entrada 2 de beto e acerta a transferência para o
	// valor novo: só o HMAC denuncia, e denuncia exatamente a entrada 2.
	c.adulterar(`UPDATE auditoria SET valor_centavos = 999 WHERE conta_id = $1 AND seq = 2`, beto)
	ps := c.verificar(ch)
	if len(ps) == 0 || ps[0].ContaID != beto {
		t.Fatalf("adulteração não detectada na conta %d: %v", beto, ps)
	}
	for _, p := range ps {
		if p.ContaID == beto && p.Seq != 2 && p.Tipo != auditoria.ProblemaTransferencia {
			t.Errorf("achado fora da entrada adulterada: %v", p)
		}
	}
	esperarSo(t, ps, auditoria.ProblemaMAC, auditoria.ProblemaTransferencia)
}

func TestApagarEntradaDoMeioEDetectado(t *testing.T) {
	c, _, beto, ch := cadeiaDeTres(t)
	c.adulterar(`DELETE FROM auditoria WHERE conta_id = $1 AND seq = 2`, beto)
	ps := c.verificar(ch)
	esperarSo(t, ps, auditoria.ProblemaSeq, auditoria.ProblemaElo, auditoria.ProblemaSemEntrada)
}

func TestReordenarEntradasEDetectado(t *testing.T) {
	c, _, beto, ch := cadeiaDeTres(t)
	// A chave primária não deixa trocar dois seq de uma vez: sai da frente.
	c.adulterar(`UPDATE auditoria SET seq = seq + 10 WHERE conta_id = $1 AND seq IN (1, 2)`, beto)
	c.adulterar(`UPDATE auditoria SET seq = CASE seq WHEN 11 THEN 2 ELSE 1 END WHERE conta_id = $1 AND seq IN (11, 12)`, beto)
	ps := c.verificar(ch)
	if len(ps) == 0 {
		t.Fatal("reordenação não detectada")
	}
	if !slices.Contains(tipos(ps), auditoria.ProblemaMAC) {
		t.Errorf("o seq faz parte do conteúdo assinado; esperava MAC inválido: %v", tipos(ps))
	}
}

func TestEntradaForjadaSemAChaveEDetectada(t *testing.T) {
	c, ana, beto, ch := cadeiaDeTres(t)
	// Acrescenta uma entrada seq 4 com MAC inventado e avança a cabeça, o que
	// é tudo que quem não tem a chave consegue fazer.
	c.adulterar(`
		WITH nova AS (
		  INSERT INTO auditoria (conta_id, seq, transferencia_id, status, pagador_id, recebedor_id,
		                         valor_centavos, decidida_em, chave_id, mac_anterior, mac)
		  SELECT conta_id, 4,
		         (SELECT transferencia_id FROM auditoria WHERE conta_id = $2 AND seq = 1),
		         status, pagador_id, recebedor_id,
		         valor_centavos, decidida_em, chave_id, mac, decode(repeat('ab', 32), 'hex')
		    FROM auditoria WHERE conta_id = $1 AND seq = 3
		  RETURNING conta_id, seq, mac)
		UPDATE contas SET auditoria_seq = nova.seq, auditoria_mac = nova.mac
		  FROM nova WHERE contas.id = nova.conta_id`, beto, ana)
	ps := c.verificar(ch)
	if !slices.Contains(tipos(ps), auditoria.ProblemaMAC) {
		t.Fatalf("entrada forjada sem a chave não foi acusada: %v", ps)
	}
}

func TestChaveDesconhecidaEAcusada(t *testing.T) {
	c, _, _, _ := cadeiaDeTres(t)
	outra, err := auditoria.NovoChaveiro("outra", map[string][]byte{"outra": make([]byte, auditoria.TamanhoChave)})
	if err != nil {
		t.Fatal(err)
	}
	esperarSo(t, c.verificar(outra), auditoria.ProblemaChave)
}

// Apagar as entradas do rabo de uma liquidada deixa a transferência (e os
// lançamentos, imutáveis) para trás: a decisão sem entrada denuncia, mesmo
// sem âncora.
func TestPodarORaboDeLiquidadaEDetectadoSemAncora(t *testing.T) {
	c, ana, beto, ch := cadeiaDeTres(t)
	c.adulterar(`DELETE FROM auditoria WHERE transferencia_id =
	               (SELECT transferencia_id FROM auditoria WHERE conta_id = $1 AND seq = 3)`, beto)
	c.adulterar(`UPDATE contas SET
	               auditoria_seq = (SELECT max(seq) FROM auditoria WHERE conta_id = contas.id),
	               auditoria_mac = (SELECT mac FROM auditoria WHERE conta_id = contas.id ORDER BY seq DESC LIMIT 1)
	             WHERE id IN ($1, $2)`, ana, beto)
	esperarSo(t, c.verificar(ch), auditoria.ProblemaSemEntrada)
}

// O limite que o ADR declara: o rabo de uma REJEITADA, apagado junto com a
// transferência (que não tem lançamentos), não deixa rastro na cadeia. Só uma
// âncora anterior denuncia.
func TestPodarORaboDeRejeitadaSoOAncoraDenuncia(t *testing.T) {
	c := novoCenario(t)
	ch := c.liq.Auditoria
	ana, beto := c.conta("ana", 100), c.conta("beto", 0)
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 10).ID); err != nil {
		t.Fatal(err)
	}
	rej, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 5_000).ID)
	if err != nil || rej.Status != domain.Rejeitada {
		t.Fatalf("pré-condição: %+v, %v", rej, err)
	}
	ancoraAna, ancoraBeto := c.ancorar(ana), c.ancorar(beto)

	c.adulterar(`DELETE FROM auditoria WHERE transferencia_id = $1`, rej.ID)
	c.adulterar(`DELETE FROM transferencias WHERE id = $1`, rej.ID)
	c.adulterar(`UPDATE contas SET auditoria_seq = auditoria_seq - 1, auditoria_mac =
	               (SELECT mac FROM auditoria WHERE conta_id = contas.id AND seq = contas.auditoria_seq - 1)
	             WHERE id IN ($1, $2)`, ana, beto)

	if ps := c.verificar(ch); len(ps) != 0 {
		t.Fatalf("sem âncora, a poda de uma rejeitada deveria passar despercebida (limite do ADR): %v", ps)
	}
	esperarSo(t, c.verificar(ch, ancoraAna, ancoraBeto), auditoria.ProblemaAncora)

	// Depois da poda, novas liquidações fazem a cadeia alcançar de novo o
	// número de posição ancorado, com outra entrada: comparar só o seq não
	// bastaria, o MAC da posição ancorada tem de ser o mesmo.
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 20).ID); err != nil {
		t.Fatal(err)
	}
	if ps := c.verificar(ch, ancoraAna, ancoraBeto); !slices.Contains(tipos(ps), auditoria.ProblemaAncora) {
		t.Fatalf("a âncora não detectou a poda depois de a cadeia crescer de novo: %v", ps)
	}
}

func TestAncoraDeCadeiaIntactaPassaEmCrescimento(t *testing.T) {
	c, ana, beto, ch := cadeiaDeTres(t)
	a, b := c.ancorar(ana), c.ancorar(beto)
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 50).ID); err != nil {
		t.Fatal(err)
	}
	if ps := c.verificar(ch, a, b); len(ps) != 0 {
		t.Fatalf("âncora de cadeia que só cresceu foi acusada: %v", ps)
	}
}

func TestRotacaoDeChaveNaoQuebraACadeia(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 10).ID); err != nil {
		t.Fatal(err)
	}
	nova := make([]byte, auditoria.TamanhoChave)
	for i := range nova {
		nova[i] = 0x55
	}
	rotacionado, err := auditoria.NovoChaveiro("v2", map[string][]byte{"v2": nova, "teste": testinfra.ChaveDeAuditoria()})
	if err != nil {
		t.Fatal(err)
	}
	c.liq.Auditoria = rotacionado
	if _, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 20).ID); err != nil {
		t.Fatal(err)
	}
	if ps := c.verificar(rotacionado); len(ps) != 0 {
		t.Fatalf("cadeia com duas chaves foi acusada: %v", ps)
	}
	var usadas int
	if err := c.pool.QueryRow(c.ctx, `SELECT count(DISTINCT chave_id) FROM auditoria WHERE conta_id = $1`, beto).Scan(&usadas); err != nil || usadas != 2 {
		t.Fatalf("esperava entradas de duas chaves na conta de beto, vieram %d (erro %v)", usadas, err)
	}
	// Quem só tem a chave nova não verifica o passado.
	soNova, _ := auditoria.NovoChaveiro("v2", map[string][]byte{"v2": nova})
	if !slices.Contains(tipos(c.verificar(soNova)), auditoria.ProblemaChave) {
		t.Fatal("verificar sem a chave antiga deveria acusar chave desconhecida")
	}
}

// A cabeça em contas é o que o worker usa para a próxima entrada; se ela não
// aponta para a última entrada, a próxima liquidação encadearia no lugar errado.
func TestCabecaQueNaoApontaParaAUltimaEntradaEAcusada(t *testing.T) {
	c, _, beto, ch := cadeiaDeTres(t)
	c.adulterar(`UPDATE contas SET auditoria_seq = 2,
	               auditoria_mac = (SELECT mac FROM auditoria WHERE conta_id = $1 AND seq = 2)
	             WHERE id = $1`, beto)
	esperarSo(t, c.verificar(ch), auditoria.ProblemaCabeca)
}
