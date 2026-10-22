package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
)

// serie devolve as combinações de rótulos de uma família de métricas, em
// "nome=valor,nome=valor".
func series(t *testing.T, reg interface {
	Gather() ([]*dto.MetricFamily, error)
}, familia string) map[string]uint64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uint64{}
	for _, f := range fams {
		if f.GetName() != familia {
			continue
		}
		for _, m := range f.GetMetric() {
			chave := ""
			for _, l := range m.GetLabel() {
				if chave != "" {
					chave += ","
				}
				chave += l.GetName() + "=" + l.GetValue()
			}
			if h := m.GetHistogram(); h != nil {
				out[chave] = h.GetSampleCount()
			} else if c := m.GetCounter(); c != nil {
				out[chave] = uint64(c.GetValue())
			}
		}
	}
	return out
}

// O rótulo "rota" é o padrão do roteador, não o caminho: mil ids diferentes
// geram uma única série. Caminhos que não existem viram "nao_encontrada",
// para que varreduras aleatórias não criem séries sem limite.
func TestMetricasDaAPINaoExplodemACardinalidade(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /transferencias/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /transferencias", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	h := a.Middleware(mux)

	chamar := func(metodo, caminho string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(metodo, caminho, nil))
	}
	for i := range 1000 {
		chamar("GET", "/transferencias/id-"+string(rune('a'+i%26))+"-"+string(rune('a'+i/26%26)))
	}
	for i := range 500 {
		chamar("GET", "/caminho/aleatorio/"+string(rune('a'+i%26))+string(rune('a'+i/26%26)))
	}
	chamar("POST", "/transferencias")

	got := series(t, reg, "pix_api_requisicao_duracao_segundos")
	esperado := map[string]uint64{
		"codigo=200,rota=GET /transferencias/{id}": 1000,
		"codigo=404,rota=nao_encontrada":           500,
		"codigo=202,rota=POST /transferencias":     1,
	}
	if len(got) != len(esperado) {
		t.Fatalf("séries = %v, esperava só %v", got, esperado)
	}
	for k, n := range esperado {
		if got[k] != n {
			t.Errorf("%s: %d observações, esperava %d (todas: %v)", k, got[k], n, got)
		}
	}
}

func TestAceitoDistingueNovoDeRepetido(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)
	a.Aceito(false)
	a.Aceito(false)
	a.Aceito(true)
	got := series(t, reg, "pix_api_pedidos_aceitos_total")
	if got["resultado=novo"] != 2 || got["resultado=repetido"] != 1 {
		t.Fatalf("contagens: %v", got)
	}
}

// net/http ignora WriteHeader depois do primeiro. O middleware deve medir o
// mesmo código que chegou ao cliente, mesmo diante de um handler defeituoso.
func TestMetricasDaAPIConservamPrimeiroStatusEscrito(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /duplo-status", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.WriteHeader(http.StatusInternalServerError)
	})
	rec := httptest.NewRecorder()
	a.Middleware(mux).ServeHTTP(rec, httptest.NewRequest("GET", "/duplo-status", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status enviado = %d, esperava %d", rec.Code, http.StatusAccepted)
	}
	got := series(t, reg, "pix_api_requisicao_duracao_segundos")
	if got["codigo=202,rota=GET /duplo-status"] != 1 {
		t.Fatalf("métrica não conservou o primeiro status: %v", got)
	}
	if got["codigo=500,rota=GET /duplo-status"] != 0 {
		t.Fatalf("métrica registrou status ignorado pelo servidor: %v", got)
	}
}

func TestMetricasDaAPITratamCorpoComoStatus200(t *testing.T) {
	reg := metricas.NovoRegistro()
	a := metricas.NovaAPI(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /corpo-antes-status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("resposta"))
		w.WriteHeader(http.StatusInternalServerError)
	})
	rec := httptest.NewRecorder()
	a.Middleware(mux).ServeHTTP(rec, httptest.NewRequest("GET", "/corpo-antes-status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status enviado = %d, esperava %d", rec.Code, http.StatusOK)
	}
	got := series(t, reg, "pix_api_requisicao_duracao_segundos")
	if got["codigo=200,rota=GET /corpo-antes-status"] != 1 {
		t.Fatalf("métrica não tratou a escrita do corpo como status 200: %v", got)
	}
	if got["codigo=500,rota=GET /corpo-antes-status"] != 0 {
		t.Fatalf("métrica registrou status posterior ao corpo: %v", got)
	}
}

// Métricas ausentes (receptor nulo) não podem derrubar o servidor: é como
// todos os testes e o código sem métricas rodam.
func TestMetricasNulasSaoInofensivas(t *testing.T) {
	var a *metricas.API
	a.Aceito(true)
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 204 {
		t.Fatalf("status %d", rec.Code)
	}
}
