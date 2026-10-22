package usecase_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/queue"
)

// valor lê o valor de uma série pelo nome da métrica e pelos rótulos.
func valor(t *testing.T, reg *prometheus.Registry, nome string, rotulos map[string]string) (float64, bool) {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != nome {
			continue
		}
	serie:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := rotulos[l.GetName()]; ok && want != l.GetValue() {
					continue serie
				}
			}
			switch {
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount()), true
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue(), true
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// Entregar a mesma mensagem várias vezes não pode inflar contadores nem o
// histograma de atraso: o hook AoFinalizar só dispara na primeira finalização.
func TestMetricasDoWorkerNaoContamEmDobro(t *testing.T) {
	p := novoPipeline(t)
	reg := metricas.NovoRegistro()
	wm := metricas.NovoWorker(reg)

	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	p.liq.AoFinalizar = wm.Finalizada // definido depois: o aporte inicial não conta
	ok1 := p.pedir(ana, beto, 300)
	ok2 := p.pedir(ana, beto, 400)
	sem := p.pedir(ana, beto, 900) // 1000-300-400 = 300 < 900: rejeitada por saldo

	var ids []string
	for _, tr := range []string{ok1.ID, ok2.ID, sem.ID} {
		ids = append(ids, tr, tr, tr) // cada uma entregue 3 vezes
	}
	if _, err := p.fila.Publicar(p.ctx, ids); err != nil {
		t.Fatal(err)
	}

	w := p.worker("w1")
	w.Liquidador = p.liq
	w.Metricas = wm
	parar := rodar(t, w.Rodar)
	defer parar()
	p.esperarTodasFinais(10 * time.Second)
	esperarStreamVazio(t, p)

	checar := func(nome string, rotulos map[string]string, quer float64) {
		t.Helper()
		if got, ok := valor(t, reg, nome, rotulos); !ok || got != quer {
			t.Errorf("%s%v = %v (existe=%v), esperava %v", nome, rotulos, got, ok, quer)
		}
	}
	checar("pix_liquidacoes_total", map[string]string{"status": "LIQUIDADA"}, 2)
	checar("pix_liquidacoes_total", map[string]string{"status": "REJEITADA"}, 1)
	checar("pix_rejeicoes_total", map[string]string{"motivo": domain.MotivoSaldoInsuficiente}, 1)
	checar("pix_atraso_aceite_liquidacao_segundos", nil, 3) // 3 finalizações, não 9 entregas
	// Tentativas contam todas as 9 entregas: é o trabalho real do worker.
	if got, _ := valor(t, reg, "pix_liquidacao_duracao_segundos", nil); got < 9 {
		t.Errorf("duração observada %v vezes, esperava ao menos 9 entregas", got)
	}
}

func TestEstadoOperacionalReflecteBacklogEIdade(t *testing.T) {
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	for range 3 {
		if _, err := p.repo.CriarComOutbox(p.ctx, domain.Transferencia{
			EndToEndID: p.proximoE2E(), PagadorID: ana, RecebedorID: beto, Valor: 10}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1100 * time.Millisecond) // para a idade ser visivelmente > 1 s

	reg := prometheus.NewRegistry()
	reg.MustRegister(metricas.NovoEstado(p.repo, p.fila))
	checar := func(nome string, quer float64, minimo bool) {
		t.Helper()
		got, ok := valor(t, reg, nome, nil)
		if !ok || (!minimo && got != quer) || (minimo && got < quer) {
			t.Errorf("%s = %v (existe=%v), esperava %v (mínimo=%v)", nome, got, ok, quer, minimo)
		}
	}
	checar("pix_outbox_pendentes", 3, false)
	checar("pix_transferencias_pendentes", 3, false)
	checar("pix_outbox_mais_antiga_segundos", 1, true)
	checar("pix_transferencia_pendente_mais_antiga_segundos", 1, true)
	checar("pix_stream_tamanho", 0, false)

	// Depois de publicar, a outbox esvazia, o stream enche, e as transferências
	// seguem pendentes (ninguém consumiu).
	if _, err := p.repo.PublicarPendentes(p.ctx, 10, p.fila.Publicar); err != nil {
		t.Fatal(err)
	}
	checar("pix_outbox_pendentes", 0, false)
	checar("pix_outbox_mais_antiga_segundos", 0, false)
	checar("pix_stream_tamanho", 3, false)
	checar("pix_transferencias_pendentes", 3, false)
	if n := testutil.CollectAndCount(metricas.NovoEstado(p.repo, p.fila)); n != 9 {
		t.Errorf("esperava 9 séries (7 de estado + 2 de sucesso) com as duas fontes no ar, vieram %d", n)
	}
}

// Com o Redis fora do ar, o coletor não inventa zeros para o stream: omite
// essas séries e sinaliza a falha da fonte. O Postgres continua reportado.
func TestEstadoComRedisForaDoArSinalizaEOmite(t *testing.T) {
	p := novoPipeline(t)
	quebrado := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer func() { _ = quebrado.Close() }()

	reg := prometheus.NewRegistry()
	reg.MustRegister(metricas.NovoEstado(p.repo, queue.New(quebrado, "x:")))

	if v, ok := valor(t, reg, "pix_estado_coleta_sucesso", map[string]string{"fonte": "redis"}); !ok || v != 0 {
		t.Errorf("sucesso{redis} = %v (existe=%v), esperava 0", v, ok)
	}
	if v, ok := valor(t, reg, "pix_estado_coleta_sucesso", map[string]string{"fonte": "postgres"}); !ok || v != 1 {
		t.Errorf("sucesso{postgres} = %v (existe=%v), esperava 1", v, ok)
	}
	for _, nome := range []string{"pix_stream_tamanho", "pix_stream_pendentes_confirmacao", "pix_stream_mortas_tamanho"} {
		if _, ok := valor(t, reg, nome, nil); ok {
			t.Errorf("%s apareceu com o Redis fora do ar; devia ser omitida", nome)
		}
	}
	if _, ok := valor(t, reg, "pix_outbox_pendentes", nil); !ok {
		t.Error("pix_outbox_pendentes sumiu apesar de o Postgres estar no ar")
	}
}

func TestConciliacaoMetricas(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := metricas.NovaConciliacao(reg)

	c.Concluida(0, 1500*time.Millisecond)
	if v, _ := valor(t, reg, "pix_conciliacao_divergencias", nil); v != 0 {
		t.Errorf("divergências = %v", v)
	}
	antes, _ := valor(t, reg, "pix_conciliacao_ultima_execucao_timestamp_segundos", nil)
	if time.Since(time.Unix(int64(antes), 0)) > 5*time.Second {
		t.Errorf("timestamp da última execução velho: %v", antes)
	}

	c.Concluida(2, time.Second)
	c.Falhou()
	for resultado, quer := range map[string]float64{"fechada": 1, "divergente": 1, "erro": 1} {
		if v, ok := valor(t, reg, "pix_conciliacao_execucoes_total", map[string]string{"resultado": resultado}); !ok || v != quer {
			t.Errorf("execucoes{%s} = %v (existe=%v)", resultado, v, ok)
		}
	}
	// Falha não zera a última resposta conhecida.
	if v, _ := valor(t, reg, "pix_conciliacao_divergencias", nil); v != 2 {
		t.Errorf("divergências depois da falha = %v, esperava 2", v)
	}
}
