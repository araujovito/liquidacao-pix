package metricas_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
)

// codigosObservados devolve "rota|codigo" -> quantidade de observações do
// histograma de duração da API.
func codigosObservados(t *testing.T, reg *prometheus.Registry) map[string]uint64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uint64{}
	for _, f := range fams {
		if f.GetName() != "pix_api_requisicao_duracao_segundos" {
			continue
		}
		for _, m := range f.GetMetric() {
			var rota, codigo string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "rota":
					rota = l.GetValue()
				case "codigo":
					codigo = l.GetValue()
				}
			}
			out[rota+"|"+codigo] = m.GetHistogram().GetSampleCount()
		}
	}
	return out
}

// Revisão de 478bce1 e 6cd1642. Travar no primeiro WriteHeader espelha o
// net/http, mas o net/http trata 1xx de forma diferente: respostas
// informativas (103 Early Hints, por exemplo) podem ser enviadas várias vezes
// ANTES do status final. A trava engolia o status final e o handler acabava
// respondendo 200 implícito, enquanto a métrica registrava 103.
func TestRespostaInformativaNaoEngoleOStatusFinal(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /x", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(a.Middleware(mux))
	defer srv.Close()

	var informativas []int
	req, _ := http.NewRequest("POST", srv.URL+"/x", nil)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
			informativas = append(informativas, code)
			return nil
		},
	}))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("o cliente recebeu %d, esperava 202 (informativas vistas: %v)", res.StatusCode, informativas)
	}
	got := codigosObservados(t, reg)
	if got["POST /x|202"] != 1 || len(got) != 1 {
		t.Fatalf("série = %v, esperava só POST /x|202", got)
	}
}

// Handler que dá pânico: o net/http derruba a conexão, mas a requisição
// aconteceu e falhou. Sem isto ela some das métricas, justamente o caso que
// o painel de 5xx existe para mostrar.
func TestPanicDoHandlerApareceComo500(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /quebra", func(http.ResponseWriter, *http.Request) { panic("falha inesperada") })
	h := a.Middleware(mux)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("o pânico precisa continuar subindo para o net/http tratá-lo")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/quebra", nil))
	}()

	got := codigosObservados(t, reg)
	if got["GET /quebra|500"] != 1 {
		t.Fatalf("série = %v, esperava GET /quebra|500", got)
	}
}

// Se o handler já tinha escrito o status antes do pânico, o código registrado
// é o que o cliente recebeu, não 500.
func TestPanicDepoisDeEscreverMantemOCodigoEnviado(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /meio", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("depois de responder")
	})
	func() {
		defer func() { _ = recover() }()
		a.Middleware(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/meio", nil))
	}()
	if got := codigosObservados(t, reg); got["GET /meio|202"] != 1 || len(got) != 1 {
		t.Fatalf("série = %v", got)
	}
}

// http.ErrAbortHandler é a forma documentada de abortar uma resposta sem
// registrar o pânico no log do servidor; continua subindo e conta como 500
// quando nada tinha sido enviado.
func TestAbortHandlerContinuaSubindo(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /aborta", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	func() {
		defer func() {
			r := recover()
			if err, ok := r.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				t.Errorf("recuperado %v, esperava http.ErrAbortHandler", r)
			}
		}()
		a.Middleware(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/aborta", nil))
	}()
	if got := codigosObservados(t, reg); got["GET /aborta|500"] != 1 {
		t.Fatalf("série = %v", got)
	}
}
