package metricas

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/araujovito/liquidacao-pix/internal/domain"
)

// Worker reúne as métricas da liquidação. Receptor nulo é aceito.
type Worker struct {
	liquidacoes *prometheus.CounterVec
	rejeicoes   *prometheus.CounterVec
	duracao     prometheus.Histogram
	atraso      prometheus.Histogram
	falhas      *prometheus.CounterVec
	mortas      prometheus.Counter
	grupo       prometheus.Counter
}

// NovoWorker registra as métricas do worker em reg.
func NovoWorker(reg prometheus.Registerer) *Worker {
	w := &Worker{
		liquidacoes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pix_liquidacoes_total",
			Help: "Transferências levadas a um status final por este worker, por status.",
		}, []string{"status"}),
		rejeicoes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pix_rejeicoes_total",
			Help: "Transferências rejeitadas, por motivo do domínio.",
		}, []string{"motivo"}),
		duracao: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pix_liquidacao_duracao_segundos",
			Help:    "Tempo de uma tentativa de liquidação (transação inteira), com sucesso ou não.",
			Buckets: bucketsLatencia,
		}),
		atraso: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pix_atraso_aceite_liquidacao_segundos",
			Help:    "Tempo entre aceitar o pedido (202) e a transferência ficar em status final.",
			Buckets: bucketsAtraso,
		}),
		falhas: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pix_worker_falhas_total",
			Help: "Falhas do worker por etapa: liquidar, confirmar, ler.",
		}, []string{"etapa"}),
		mortas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pix_mensagens_mortas_total",
			Help: "Mensagens movidas para o stream de mortas depois de esgotar as entregas.",
		}),
		grupo: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pix_grupo_consumo_recriado_total",
			Help: "Vezes em que o grupo de consumo sumiu do Redis e foi recriado.",
		}),
	}
	reg.MustRegister(w.liquidacoes, w.rejeicoes, w.duracao, w.atraso, w.falhas, w.mortas, w.grupo)
	return w
}

// Finalizada conta uma transferência que acabou de chegar ao status final.
// O atraso só entra aqui, na primeira finalização; uma entrega repetida de
// transferência já final não passa por este método.
func (w *Worker) Finalizada(t domain.Transferencia) {
	if w == nil {
		return
	}
	w.liquidacoes.WithLabelValues(string(t.Status)).Inc()
	if t.Status == domain.Rejeitada {
		w.rejeicoes.WithLabelValues(t.MotivoRejeicao).Inc()
	}
	if t.FinalizadaEm != nil {
		w.atraso.Observe(t.FinalizadaEm.Sub(t.CriadaEm).Seconds())
	}
}

// Tentativa registra a duração de uma tentativa de liquidação.
func (w *Worker) Tentativa(segundos float64) {
	if w != nil {
		w.duracao.Observe(segundos)
	}
}

// Falha conta uma falha do worker numa etapa ("liquidar", "confirmar", "ler").
func (w *Worker) Falha(etapa string) {
	if w != nil {
		w.falhas.WithLabelValues(etapa).Inc()
	}
}

// Mortas conta mensagens movidas para o stream de mortas.
func (w *Worker) Mortas(n int) {
	if w != nil {
		w.mortas.Add(float64(n))
	}
}

// GrupoRecriado conta uma recriação do grupo de consumo.
func (w *Worker) GrupoRecriado() {
	if w != nil {
		w.grupo.Inc()
	}
}

// Relay reúne as métricas da publicação da outbox. Receptor nulo é aceito.
type Relay struct {
	publicadas   prometheus.Counter
	falhas       prometheus.Counter
	republicadas prometheus.Counter
}

// NovoRelay registra as métricas do relay em reg.
func NovoRelay(reg prometheus.Registerer) *Relay {
	r := &Relay{
		publicadas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pix_relay_publicadas_total",
			Help: "Mensagens publicadas no stream a partir da outbox (inclui republicações).",
		}),
		falhas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pix_relay_falhas_total",
			Help: "Lotes que o relay não conseguiu publicar (Redis ou Postgres indisponível).",
		}),
		republicadas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pix_relay_republicadas_total",
			Help: "Transferências devolvidas à outbox pela varredura de órfãs (mensagem sumiu do Redis).",
		}),
	}
	reg.MustRegister(r.publicadas, r.falhas, r.republicadas)
	return r
}

// Publicadas conta mensagens publicadas.
func (r *Relay) Publicadas(n int) {
	if r != nil {
		r.publicadas.Add(float64(n))
	}
}

// Falha conta um lote que não foi publicado.
func (r *Relay) Falha() {
	if r != nil {
		r.falhas.Inc()
	}
}

// Republicadas conta transferências devolvidas à outbox.
func (r *Relay) Republicadas(n int64) {
	if r != nil {
		r.republicadas.Add(float64(n))
	}
}
