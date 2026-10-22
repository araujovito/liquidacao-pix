package metricas

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// API reúne as métricas do servidor HTTP. Todos os métodos aceitam receptor
// nulo, para que o código sem métricas (testes, por exemplo) não precise de
// condicionais.
type API struct {
	duracao *prometheus.HistogramVec
	aceitas *prometheus.CounterVec
}

// NovaAPI registra as métricas da API em reg.
func NovaAPI(reg prometheus.Registerer) *API {
	a := &API{
		duracao: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pix_api_requisicao_duracao_segundos",
			Help:    "Duração das requisições HTTP por rota e código de resposta.",
			Buckets: bucketsLatencia,
		}, []string{"rota", "codigo"}),
		aceitas: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pix_api_pedidos_aceitos_total",
			Help: "Pedidos de transferência respondidos com 202, por resultado (novo ou repetido).",
		}, []string{"resultado"}),
	}
	reg.MustRegister(a.duracao, a.aceitas)
	return a
}

// Aceito conta um pedido respondido com 202.
func (a *API) Aceito(repetido bool) {
	if a == nil {
		return
	}
	resultado := "novo"
	if repetido {
		resultado = "repetido"
	}
	a.aceitas.WithLabelValues(resultado).Inc()
}

type gravadorDeStatus struct {
	http.ResponseWriter
	codigo   int
	escreveu bool
}

func (g *gravadorDeStatus) WriteHeader(c int) {
	// Respostas informativas (1xx, exceto 101) podem ser enviadas várias vezes
	// ANTES do status final (ex.: 103 Early Hints). Elas passam adiante sem
	// travar o gravador; travar nelas engolia o status final, e o cliente
	// recebia 200 implícito no lugar do código que o handler escolheu.
	if c >= 100 && c <= 199 && c != http.StatusSwitchingProtocols {
		if !g.escreveu {
			g.ResponseWriter.WriteHeader(c)
		}
		return
	}
	// net/http considera apenas o primeiro WriteHeader final. Espelhar essa
	// regra é essencial para a métrica descrever a resposta que de fato foi
	// enviada.
	if g.escreveu {
		return
	}
	g.escreveu = true
	g.codigo = c
	g.ResponseWriter.WriteHeader(c)
}

// Unwrap expõe o ResponseWriter original ao http.ResponseController (Flush,
// prazos de escrita), que não enxerga métodos de um wrapper.
func (g *gravadorDeStatus) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gravadorDeStatus) Write(p []byte) (int, error) {
	if !g.escreveu {
		g.WriteHeader(http.StatusOK)
	}
	return g.ResponseWriter.Write(p)
}

// Middleware mede cada requisição. O rótulo "rota" é o padrão do roteador
// ("POST /transferencias", "GET /transferencias/{id}"), nunca o caminho
// literal: com o caminho, cada id de transferência criaria uma série nova.
// Rotas inexistentes (404 do roteador) caem em "nao_encontrada", para que
// varreduras de caminhos aleatórios não inflem a cardinalidade.
func (a *API) Middleware(proximo http.Handler) http.Handler {
	if a == nil {
		return proximo
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		g := &gravadorDeStatus{ResponseWriter: w, codigo: http.StatusOK}

		// A observação vai num defer: handler que dá pânico também aconteceu e
		// falhou, e sem isto sumiria das métricas, justamente o caso que o
		// painel de 5xx existe para mostrar. O pânico continua subindo para o
		// net/http tratá-lo (inclusive http.ErrAbortHandler).
		defer func() {
			panico := recover()
			if panico != nil && !g.escreveu {
				g.codigo = http.StatusInternalServerError
			}
			// r.Pattern é preenchido pelo roteador durante ServeHTTP, na mesma
			// requisição; por isso só pode ser lido depois.
			rota := r.Pattern
			if rota == "" {
				rota = "nao_encontrada"
			}
			a.duracao.WithLabelValues(rota, strconv.Itoa(g.codigo)).Observe(time.Since(inicio).Seconds())
			if panico != nil {
				panic(panico)
			}
		}()
		proximo.ServeHTTP(g, r)
	})
}
