// Package metricas define as métricas Prometheus dos programas.
//
// Regras de cardinalidade: todo rótulo tem um conjunto pequeno e fixo de
// valores (rota do roteador, código HTTP, status final, motivo de rejeição
// do domínio). Nenhum rótulo recebe id de transferência, chave PIX, CPF,
// end_to_end_id ou texto de erro: o que é por-pedido vai para logs e traces,
// nunca para métricas.
package metricas

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NovoRegistro devolve um registro com as métricas do runtime Go e do
// processo (memória, goroutines, descritores abertos).
func NovoRegistro() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Servir expõe /metrics em METRICAS_ENDERECO (padrão :9100) até ctx ser
// cancelado. É uma porta separada da API, para que as métricas não fiquem no
// mesmo endereço público dos pedidos; no compose ela só existe dentro da rede
// dos contêineres.
func Servir(ctx context.Context, log *slog.Logger, reg *prometheus.Registry) {
	endereco := os.Getenv("METRICAS_ENDERECO")
	if endereco == "" {
		endereco = ":9100"
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: endereco, Handler: mux, ReadHeaderTimeout: 2 * time.Second}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("servidor de métricas parou", "erro", err)
		}
	}()
	go func() { //nolint:gosec // ctx já foi cancelado aqui; o Shutdown precisa de um prazo próprio
		<-ctx.Done()
		fim, cancelar := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelar()
		_ = srv.Shutdown(fim)
	}()
	log.Info("métricas expostas", "endereco", endereco)
}

// Buckets de latência em segundos: de 1 ms a 10 s, mais finos onde a API vive.
var bucketsLatencia = []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// Buckets de atraso aceite→liquidação: de 10 ms a 5 min. O atraso normal é de
// dezenas de ms; a cauda longa mostra fila acumulada.
var bucketsAtraso = []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}
