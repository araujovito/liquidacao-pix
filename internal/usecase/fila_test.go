package usecase_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

// Tempos curtos para os testes não esperarem os valores de produção.
const (
	esperaBaseTeste = 200 * time.Millisecond
	maxEntregas     = 3
)

type pipeline struct {
	*cenario
	rdb  *redis.Client
	fila *queue.Fila
	log  *slog.Logger
}

func novoPipeline(t *testing.T) *pipeline {
	c := novoCenario(t)
	rdb, prefixo := testinfra.Redis(t)
	log := slog.New(slog.DiscardHandler)
	if os.Getenv("LOG_TESTE") != "" { // LOG_TESTE=1 go test -v … mostra os logs do relay e do worker
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &pipeline{cenario: c, rdb: rdb, fila: queue.New(rdb, prefixo), log: log}
}

func (p *pipeline) relay() usecase.Relay {
	return usecase.Relay{Repo: p.repo, Fila: p.fila, Log: p.log, Lote: 50,
		Intervalo: 5 * time.Millisecond, IdadeOrfa: time.Second, MaxRepublicacoes: 3}
}

func (p *pipeline) worker(nome string) usecase.Worker {
	return usecase.Worker{Liquidador: p.liq, Fila: p.fila, Log: p.log, Consumidor: nome,
		Lote: 10, EsperaBase: esperaBaseTeste, MaxEntregas: maxEntregas}
}

// rodar sobe os laços em goroutines e devolve a função que os para e espera.
func rodar(t *testing.T, lacos ...func(context.Context) error) (parar func()) {
	ctx, cancelar := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, l := range lacos {
		wg.Go(func() {
			if err := l(ctx); err != nil {
				t.Errorf("laço terminou com erro: %v", err)
			}
		})
	}
	return func() { cancelar(); wg.Wait() }
}

// esperarTodasFinais espera até nenhuma transferência estar PENDENTE.
func (p *pipeline) esperarTodasFinais(prazo time.Duration) map[domain.Status]int {
	p.t.Helper()
	limite := time.Now().Add(prazo)
	for {
		cont, err := p.repo.ContarPorStatus(p.ctx)
		if err != nil {
			p.t.Fatal(err)
		}
		if cont[domain.Pendente] == 0 {
			return cont
		}
		if time.Now().After(limite) {
			p.t.Fatalf("prazo esgotado com %d pendentes: %v", cont[domain.Pendente], cont)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *pipeline) aceitar(n int, contas []int64) {
	p.t.Helper()
	for i := range n {
		de, para := contas[i%len(contas)], contas[(i+1)%len(contas)]
		if _, err := p.repo.CriarComOutbox(p.ctx, domain.Transferencia{
			EndToEndID: p.proximoE2E(), PagadorID: de, RecebedorID: para, Valor: domain.Centavos(1 + i%5_000),
		}); err != nil {
			p.t.Fatal(err)
		}
	}
}

func TestFluxoCompletoOutboxStreamWorker(t *testing.T) {
	p := novoPipeline(t)
	contas := []int64{p.conta("a", 50_000), p.conta("b", 50_000), p.conta("c", 50_000)}
	p.aceitar(300, contas)

	parar := rodar(t, p.relay().Rodar, p.worker("w1").Rodar, p.worker("w2").Rodar, p.worker("w3").Rodar)
	defer parar()

	cont := p.esperarTodasFinais(20 * time.Second)
	t.Logf("status finais: %v", cont)
	p.conciliado()

	if n, _ := p.repo.PendentesNaOutbox(p.ctx); n != 0 {
		t.Errorf("%d linhas não publicadas na outbox", n)
	}
	// Confirmar apaga do stream: sem MAXLEN, ele não cresce sem limite.
	if n, _ := p.fila.Tamanho(p.ctx); n != 0 {
		t.Errorf("stream ficou com %d entradas", n)
	}
}

// A mesma transferência publicada várias vezes (relay que caiu entre XADD e o
// commit) é liquidada uma única vez.
func TestMensagemRepetidaLiquidaUmaVez(t *testing.T) {
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	tr := p.pedir(ana, beto, 600)
	if _, err := p.fila.Publicar(p.ctx, []string{tr.ID, tr.ID, tr.ID, tr.ID}); err != nil {
		t.Fatal(err)
	}

	parar := rodar(t, p.worker("w1").Rodar, p.worker("w2").Rodar)
	defer parar()
	p.esperarTodasFinais(10 * time.Second)
	esperarStreamVazio(t, p)

	if p.saldo(ana) != 400 || p.saldo(beto) != 600 {
		t.Fatalf("saldos ana=%s beto=%s", p.saldo(ana), p.saldo(beto))
	}
	p.conciliado()
}

func esperarStreamVazio(t *testing.T, p *pipeline) {
	t.Helper()
	for range 200 {
		if n, _ := p.fila.Tamanho(p.ctx); n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stream não esvaziou")
}

// Um worker lê a mensagem e morre sem confirmar. Outro reivindica depois da
// espera base e liquida.
func TestWorkerMortoTemMensagemReivindicada(t *testing.T) {
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	tr := p.pedir(ana, beto, 100)
	if err := p.fila.GarantirGrupo(p.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.fila.Publicar(p.ctx, []string{tr.ID}); err != nil {
		t.Fatal(err)
	}

	// "Worker que morre": lê e nunca confirma.
	lidas, err := p.fila.Ler(p.ctx, "morto", 10, time.Second)
	if err != nil || len(lidas) != 1 {
		t.Fatalf("leitura: %v %v", lidas, err)
	}

	parar := rodar(t, p.worker("vivo").Rodar)
	defer parar()
	cont := p.esperarTodasFinais(5 * time.Second)
	if cont[domain.Liquidada] != 2 { // aporte + a transferência
		t.Fatalf("status: %v", cont)
	}
}

// Falha permanente (transferência que não existe): depois de maxEntregas
// tentativas com espera crescente, a mensagem vai para as mortas com o erro.
func TestFalhaPersistenteVaiParaMortas(t *testing.T) {
	p := novoPipeline(t)
	fantasma := uuid.NewString()
	if _, err := p.fila.Publicar(p.ctx, []string{fantasma}); err != nil {
		t.Fatal(err)
	}

	inicio := time.Now()
	parar := rodar(t, p.worker("w1").Rodar)
	var mortas []queue.Morta
	for time.Since(inicio) < 10*time.Second && len(mortas) == 0 {
		time.Sleep(50 * time.Millisecond)
		var err error
		if mortas, err = p.fila.Mortas(p.ctx); err != nil {
			t.Fatal(err)
		}
	}
	parar()
	decorrido := time.Since(inicio)

	if len(mortas) != 1 || mortas[0].TransferenciaID != fantasma || mortas[0].Entregas != fmt.Sprint(maxEntregas) {
		t.Fatalf("mortas: %+v", mortas)
	}
	if mortas[0].UltimoErro == "" {
		t.Error("morta sem o último erro anotado")
	}
	// Esperas de 1× e 2× a base antes da 2ª e 3ª entregas, e 4× antes de
	// desistir: pelo menos 7× a base. Prova que há espera, não laço apertado.
	if minimo := 7 * esperaBaseTeste; decorrido < minimo {
		t.Errorf("desistiu em %s, antes do mínimo de %s", decorrido, minimo)
	}
	if n, _ := p.fila.Tamanho(p.ctx); n != 0 {
		t.Errorf("morta continua no stream principal (%d)", n)
	}
}

// Redis fora do ar: o relay falha, a outbox segura os pedidos; quando o Redis
// "volta" (aqui, trocando a fila quebrada pela boa), tudo é publicado e
// liquidado.
func TestRedisForaDoArNaoPerdePedido(t *testing.T) {
	p := novoPipeline(t)
	contas := []int64{p.conta("a", 10_000), p.conta("b", 10_000)}
	p.aceitar(40, contas)

	quebrado := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer func() { _ = quebrado.Close() }()
	relayQuebrado := p.relay()
	relayQuebrado.Fila = queue.New(quebrado, "x:")

	parar := rodar(t, relayQuebrado.Rodar)
	time.Sleep(300 * time.Millisecond)
	parar()
	if n, _ := p.repo.PendentesNaOutbox(p.ctx); n != 40 {
		t.Fatalf("com Redis fora, esperava 40 na outbox, há %d", n)
	}

	parar = rodar(t, p.relay().Rodar, p.worker("w1").Rodar)
	defer parar()
	p.esperarTodasFinais(10 * time.Second)
	p.conciliado()
}

// Mensagem perdida no Redis (publicada, mas sumiu antes de ser consumida —
// queda com AOF everysec, por exemplo). A varredura de órfãs recoloca na
// outbox e a transferência é liquidada.
func TestMensagemPerdidaNoRedisERecuperada(t *testing.T) {
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	if _, err := p.repo.CriarComOutbox(p.ctx, domain.Transferencia{
		EndToEndID: p.proximoE2E(), PagadorID: ana, RecebedorID: beto, Valor: 250}); err != nil {
		t.Fatal(err)
	}
	// Publica e "perde": apaga o stream inteiro depois do relay marcar.
	if _, err := p.repo.PublicarPendentes(p.ctx, 10, p.fila.Publicar); err != nil {
		t.Fatal(err)
	}
	if err := p.fila.Descartar(p.ctx); err != nil {
		t.Fatal(err)
	}

	parar := rodar(t, p.relay().Rodar, p.worker("w1").Rodar)
	defer parar()
	p.esperarTodasFinais(10 * time.Second)
	if p.saldo(beto) != 250 {
		t.Fatalf("beto=%s", p.saldo(beto))
	}
	p.conciliado()
}

// Apagar o stream com os workers rodando também apaga o grupo de consumo. O
// relay recria o stream no XADD seguinte, sem grupo; os workers precisam
// recriá-lo. Achado do teste de caos (workers presos em NOGROUP).
func TestStreamApagadoComWorkersRodando(t *testing.T) {
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)

	parar := rodar(t, p.relay().Rodar, p.worker("w1").Rodar)
	defer parar()
	time.Sleep(200 * time.Millisecond) // worker já criou o grupo e está lendo
	if err := p.fila.Descartar(p.ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := p.repo.CriarComOutbox(p.ctx, domain.Transferencia{
		EndToEndID: p.proximoE2E(), PagadorID: ana, RecebedorID: beto, Valor: 250}); err != nil {
		t.Fatal(err)
	}
	p.esperarTodasFinais(10 * time.Second)
	if p.saldo(beto) != 250 {
		t.Fatalf("beto=%s", p.saldo(beto))
	}
	p.conciliado()
}

// Mensagem que só está esperando na fila (sem worker rodando) há mais tempo
// que IdadeOrfa NÃO é republicada. Regressão do teste de caos: a primeira
// versão da varredura republicava tudo que estava PENDENTE havia mais de 30 s,
// e com a fila atrasada isso gerou 14 mil duplicatas numa rodada.
func TestMensagemEsperandoNaFilaNaoERepublicada(t *testing.T) {
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	if _, err := p.repo.CriarComOutbox(p.ctx, domain.Transferencia{
		EndToEndID: p.proximoE2E(), PagadorID: ana, RecebedorID: beto, Valor: 10}); err != nil {
		t.Fatal(err)
	}

	parar := rodar(t, p.relay().Rodar) // só o relay: ninguém consome
	time.Sleep(2500 * time.Millisecond) // > 2× IdadeOrfa do teste
	parar()

	var republicacoes int
	if err := p.pool.QueryRow(p.ctx, `SELECT republicacoes FROM outbox`).Scan(&republicacoes); err != nil {
		t.Fatal(err)
	}
	if republicacoes != 0 {
		t.Fatalf("mensagem na fila foi republicada %d vezes", republicacoes)
	}
	if n, _ := p.fila.Tamanho(p.ctx); n != 1 {
		t.Fatalf("stream com %d entradas, esperava 1", n)
	}
}
