package web_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/araujovito/liquidacao-pix/internal/web"
)

// O span é nomeado pelo padrão da rota: mil ids diferentes dão um só nome.
// Com o caminho no nome, cada consulta criaria um nome novo, o que estraga a
// agregação no backend de traces.
func TestSpanDaAPIUsaOPadraoDaRota(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	// Repo nulo: ids que não são UUID são recusados antes de tocar no banco.
	h := (&web.Servidor{Log: slog.New(slog.DiscardHandler)}).Rotas()
	for _, id := range []string{"a", "b", "c", "nao-e-uuid", "%27%3B%20DROP%20TABLE%20contas"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/transferencias/"+id, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %d", rec.Code)
		}
	}

	nomes := map[string]int{}
	for _, s := range exp.GetSpans() {
		nomes[s.Name]++
	}
	if len(nomes) != 1 || nomes["GET /transferencias/{id}"] != 5 {
		t.Fatalf("nomes de span: %v", nomes)
	}
}
