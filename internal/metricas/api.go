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
	codigo int
}

func (g *gravadorDeStatus) WriteHeader(c int) {
	g.codigo = c
	g.ResponseWriter.WriteHeader(c)
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
		proximo.ServeHTTP(g, r)

		// r.Pattern é preenchido pelo roteador durante ServeHTTP, na mesma
		// requisição; por isso só pode ser lido depois.
		rota := r.Pattern
		if rota == "" {
			rota = "nao_encontrada"
		}
		a.duracao.WithLabelValues(rota, strconv.Itoa(g.codigo)).Observe(time.Since(inicio).Seconds())
	})
}
