package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

func e2e(n int) string { return fmt.Sprintf("E12345678202610211430%011d", n) }

type cenario struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	repo *repository.Repositorio
	liq  usecase.Liquidador
	seq  int
}

func novoCenario(t *testing.T) *cenario {
	pool := testinfra.Postgres(t)
	repo := repository.New(pool)
	return &cenario{t: t, ctx: context.Background(), pool: pool, repo: repo, liq: usecase.Liquidador{Repo: repo, Auditoria: testinfra.Chaveiro(t)}}
}

func (c *cenario) proximoE2E() string { c.seq++; return e2e(c.seq) }

func (c *cenario) conta(nome string, saldo domain.Centavos) int64 {
	c.t.Helper()
	id, err := c.repo.CriarConta(c.ctx, nome+"@teste", nome)
	if err != nil {
		c.t.Fatal(err)
	}
	if saldo > 0 {
		if _, err := c.liq.Aportar(c.ctx, id, saldo, c.proximoE2E()); err != nil {
			c.t.Fatal(err)
		}
	}
	return id
}

func (c *cenario) pedir(de, para int64, v domain.Centavos) domain.Transferencia {
	c.t.Helper()
	tr, err := c.repo.CriarTransferencia(c.ctx, domain.Transferencia{
		EndToEndID: c.proximoE2E(), PagadorID: de, RecebedorID: para, Valor: v})
	if err != nil {
		c.t.Fatal(err)
	}
	return tr
}

func (c *cenario) saldo(id int64) domain.Centavos {
	c.t.Helper()
	s, err := c.repo.SaldoDaConta(c.ctx, id)
	if err != nil {
		c.t.Fatal(err)
	}
	return s
}

func (c *cenario) conciliado() {
	c.t.Helper()
	divs, err := c.repo.Conciliar(c.ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, d := range divs {
		c.t.Errorf("divergência %s: %s", d.Tipo, d.Detalhe)
	}
}

func TestLiquidaEMoveSaldo(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 10_000), c.conta("beto", 0)

	tr, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 2_550).ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != domain.Liquidada || tr.FinalizadaEm == nil {
		t.Fatalf("esperava LIQUIDADA com data, veio %+v", tr)
	}
	if c.saldo(ana) != 7_450 || c.saldo(beto) != 2_550 {
		t.Fatalf("saldos ana=%s beto=%s", c.saldo(ana), c.saldo(beto))
	}
	c.conciliado()
}

func TestRejeitaSemSaldoSemLancamento(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 100), c.conta("beto", 0)

	tr, err := c.liq.Liquidar(c.ctx, c.pedir(ana, beto, 101).ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != domain.Rejeitada || tr.MotivoRejeicao != domain.MotivoSaldoInsuficiente {
		t.Fatalf("esperava REJEITADA por saldo, veio %+v", tr)
	}
	if c.saldo(ana) != 100 || c.saldo(beto) != 0 {
		t.Fatal("rejeição mexeu em saldo")
	}
	c.conciliado()
}

func TestLiquidarDeNovoNaoMexeEmNada(t *testing.T) {
	c := novoCenario(t)
	ana, beto := c.conta("ana", 1_000), c.conta("beto", 0)
	tr := c.pedir(ana, beto, 400)

	primeira, err := c.liq.Liquidar(c.ctx, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		deNovo, err := c.liq.Liquidar(c.ctx, tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		if deNovo.Status != primeira.Status || !deNovo.FinalizadaEm.Equal(*primeira.FinalizadaEm) {
			t.Fatalf("segunda liquidação mudou o resultado: %+v", deNovo)
		}
	}
	if c.saldo(ana) != 600 {
		t.Fatalf("debitou mais de uma vez: %s", c.saldo(ana))
	}
	c.conciliado()
}

func TestLancamentosSaoImutaveis(t *testing.T) {
	c := novoCenario(t)
	pool := c.pool
	c.conta("ana", 500)

	for _, sql := range []string{
		`UPDATE lancamentos SET valor_centavos = 1`,
		`DELETE FROM lancamentos`,
		`TRUNCATE lancamentos CASCADE`,
	} {
		if _, err := pool.Exec(c.ctx, sql); err == nil {
			t.Errorf("banco aceitou %q", sql)
		}
	}
}

// TestConcorrenciaConservaDinheiro é o teste central da fase 1: milhares de
// transferências cruzadas entre poucas contas (muita disputa pela mesma
// linha), cada uma liquidada DUAS vezes em paralelo para simular a fila
// entregando a mesma mensagem repetida.
//
// Ao final: nenhum erro (nem deadlock), nenhuma pendente, nenhum saldo
// negativo, o total dos clientes igual ao aportado e a conciliação fechada.
func TestConcorrenciaConservaDinheiro(t *testing.T) {
	const (
		nContas        = 12
		saldoInicial   = 100_000 // R$ 1.000,00
		nTransferencia = 4_000
		trabalhadores  = 32
	)
	c := novoCenario(t)
	rnd := rand.New(rand.NewPCG(42, 42)) //nolint:gosec // semente fixa de propósito: o teste precisa ser reproduzível

	contas := make([]int64, nContas)
	for i := range contas {
		contas[i] = c.conta(fmt.Sprintf("c%02d", i), saldoInicial)
	}

	// Cada id aparece duas vezes, em posições embaralhadas.
	ids := make([]string, 0, 2*nTransferencia)
	for range nTransferencia {
		de := rnd.IntN(nContas)
		para := (de + 1 + rnd.IntN(nContas-1)) % nContas
		tr := c.pedir(contas[de], contas[para], domain.Centavos(1+rnd.IntN(30_000)))
		ids = append(ids, tr.ID, tr.ID)
	}
	rnd.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })

	// Prazo curto: sem ele, uma regressão que cause deadlocks deixa o teste
	// lento (cada deadlock custa 1 s até o Postgres detectar) em vez de falhar.
	ctx, cancelar := context.WithTimeout(c.ctx, 60*time.Second)
	defer cancelar()

	fila := make(chan string)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for range trabalhadores {
		wg.Go(func() {
			for id := range fila {
				if _, err := c.liq.Liquidar(ctx, id); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		})
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		fila <- id
	}
	close(fila)
	wg.Wait()

	if len(errs) > 0 {
		var deadlocks int
		for _, err := range errs {
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "40P01" {
				deadlocks++
			}
		}
		t.Fatalf("%d erros durante a liquidação (%d deadlocks); primeiro: %v", len(errs), deadlocks, errs[0])
	}

	var total domain.Centavos
	for _, id := range contas {
		s := c.saldo(id)
		if s < 0 {
			t.Errorf("conta %d negativa: %s", id, s)
		}
		total += s
	}
	if total != nContas*saldoInicial {
		t.Errorf("dinheiro dos clientes mudou: %s, esperava %s", total, domain.Centavos(nContas*saldoInicial))
	}

	contagem, err := c.repo.ContarPorStatus(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	liquidadas, rejeitadas, pendentes := contagem[domain.Liquidada], contagem[domain.Rejeitada], contagem[domain.Pendente]
	if pendentes != 0 {
		t.Errorf("%d transferências ficaram pendentes", pendentes)
	}
	if rejeitadas == 0 {
		t.Error("nenhuma rejeição: o teste não exercitou o caminho de saldo insuficiente")
	}
	t.Logf("%d liquidadas, %d rejeitadas (fora %d aportes)", liquidadas-nContas, rejeitadas, nContas)
	c.conciliado()
}
