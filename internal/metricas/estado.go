package metricas

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// Estado coleta, no momento do scrape, o que está esperando no Postgres e no
// Redis. É um Collector (e não gauges atualizados por um laço) para que o
// valor nunca fique velho: o Prometheus vê o estado do instante da coleta.
//
// Rode UMA instância (o conciliador); com várias réplicas coletando, o mesmo
// backlog apareceria duplicado.
type Estado struct {
	repo *repository.Repositorio
	fila *queue.Fila

	outboxPendentes, outboxIdade, pendentes, pendenteIdade *prometheus.Desc
	streamTamanho, streamPendentes, mortas, sucesso        *prometheus.Desc
}

// NovoEstado cria o coletor; registre-o com reg.MustRegister.
func NovoEstado(repo *repository.Repositorio, fila *queue.Fila) *Estado {
	d := func(nome, ajuda string, rotulos ...string) *prometheus.Desc {
		return prometheus.NewDesc(nome, ajuda, rotulos, nil)
	}
	return &Estado{
		repo: repo, fila: fila,
		outboxPendentes: d("pix_outbox_pendentes", "Linhas da outbox ainda não publicadas no stream."),
		outboxIdade:     d("pix_outbox_mais_antiga_segundos", "Idade da linha mais antiga da outbox ainda não publicada (0 se vazia)."),
		pendentes:       d("pix_transferencias_pendentes", "Transferências aceitas e ainda sem status final."),
		pendenteIdade:   d("pix_transferencia_pendente_mais_antiga_segundos", "Idade da transferência PENDENTE mais antiga (0 se não há)."),
		streamTamanho:   d("pix_stream_tamanho", "Entradas no stream principal (esperando ou em processamento)."),
		streamPendentes: d("pix_stream_pendentes_confirmacao", "Mensagens entregues a um worker e ainda sem confirmação."),
		mortas:          d("pix_stream_mortas_tamanho", "Entradas no stream de mensagens mortas."),
		sucesso:         d("pix_estado_coleta_sucesso", "1 se a fonte respondeu nesta coleta, 0 se falhou.", "fonte"),
	}
}

func (e *Estado) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{e.outboxPendentes, e.outboxIdade, e.pendentes, e.pendenteIdade,
		e.streamTamanho, e.streamPendentes, e.mortas, e.sucesso} {
		ch <- d
	}
}

func (e *Estado) Collect(ch chan<- prometheus.Metric) {
	ctx, cancelar := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelar()
	gauge := func(d *prometheus.Desc, v float64, rotulos ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, rotulos...)
	}

	// Fonte fora do ar: não emite os valores dela (um zero seria mentira) e
	// sinaliza a falha, que o alerta de "coleta falhando" usa.
	if pg, err := e.repo.EstadoOperacional(ctx); err == nil {
		gauge(e.outboxPendentes, float64(pg.OutboxPendentes))
		gauge(e.outboxIdade, pg.OutboxMaisAntigaSeg)
		gauge(e.pendentes, float64(pg.TransferenciasPendente))
		gauge(e.pendenteIdade, pg.PendenteMaisAntigaSeg)
		gauge(e.sucesso, 1, "postgres")
	} else {
		gauge(e.sucesso, 0, "postgres")
	}
	if rd, err := e.fila.Estado(ctx); err == nil {
		gauge(e.streamTamanho, float64(rd.Tamanho))
		gauge(e.streamPendentes, float64(rd.PendentesDeConfirmacao))
		gauge(e.mortas, float64(rd.Mortas))
		gauge(e.sucesso, 1, "redis")
	} else {
		gauge(e.sucesso, 0, "redis")
	}
}

// Conciliacao reúne as métricas da conciliação periódica.
type Conciliacao struct {
	divergencias prometheus.Gauge
	ultima       prometheus.Gauge
	duracao      prometheus.Gauge
	execucoes    *prometheus.CounterVec
}

// NovaConciliacao registra as métricas em reg.
func NovaConciliacao(reg prometheus.Registerer) *Conciliacao {
	c := &Conciliacao{
		divergencias: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pix_conciliacao_divergencias",
			Help: "Divergências encontradas na última conciliação concluída (0 = fechada).",
		}),
		ultima: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pix_conciliacao_ultima_execucao_timestamp_segundos",
			Help: "Momento (Unix) da última conciliação concluída, com ou sem divergência.",
		}),
		duracao: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pix_conciliacao_duracao_segundos",
			Help: "Duração da última conciliação concluída.",
		}),
		execucoes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pix_conciliacao_execucoes_total",
			Help: "Conciliações por resultado: fechada, divergente ou erro (não conseguiu rodar).",
		}, []string{"resultado"}),
	}
	reg.MustRegister(c.divergencias, c.ultima, c.duracao, c.execucoes)
	return c
}

// Concluida registra uma conciliação que rodou até o fim.
func (c *Conciliacao) Concluida(divergencias int, duracao time.Duration) {
	if c == nil {
		return
	}
	c.divergencias.Set(float64(divergencias))
	c.ultima.SetToCurrentTime()
	c.duracao.Set(duracao.Seconds())
	resultado := "fechada"
	if divergencias > 0 {
		resultado = "divergente"
	}
	c.execucoes.WithLabelValues(resultado).Inc()
}

// Falhou registra uma conciliação que não conseguiu rodar (banco fora do ar).
// Não mexe nas divergências: a última resposta conhecida segue valendo, e o
// alerta de idade da última execução cobre o caso de ela ficar velha.
func (c *Conciliacao) Falhou() {
	if c != nil {
		c.execucoes.WithLabelValues("erro").Inc()
	}
}
