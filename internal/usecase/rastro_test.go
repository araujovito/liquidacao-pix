package usecase_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

// ativarRastreamento instala um exportador em memória e o desfaz no fim do
// teste. O provedor é global, então estes testes não rodam em paralelo.
func ativarRastreamento(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	return exp
}

func spanPorNome(t *testing.T, exp *tracetest.InMemoryExporter, nome string) tracetest.SpanStub {
	t.Helper()
	for _, s := range exp.GetSpans() {
		if s.Name == nome {
			return s
		}
	}
	t.Fatalf("span %q não encontrado; há %d spans", nome, len(exp.GetSpans()))
	return tracetest.SpanStub{}
}

// O rastro de um pedido atravessa o Postgres, o Redis e três processos sem
// nenhuma chamada direta entre eles: o contexto viaja na outbox e na mensagem.
// A árvore esperada é POST (API) → aceitar_pedido → relay.publicar →
// worker.liquidar, tudo no mesmo trace.
func TestRastroAtravessaOutboxRelayEWorker(t *testing.T) {
	exp := ativarRastreamento(t)
	p := novoPipeline(t)
	p.conta("ana", 1_000)
	p.conta("beto", 0)
	exp.Reset() // descarta spans da preparação

	// Faz o papel do span servidor que o otelhttp abre na API.
	ctx, api := otel.Tracer("teste").Start(p.ctx, "POST /transferencias")
	e2e := p.proximoE2E()
	tr, _, err := usecase.Receptor{Repo: p.repo}.Aceitar(ctx, usecase.Pedido{
		EndToEndID: e2e, ChavePagador: "ana@teste", ChaveRecebedor: "beto@teste", Valor: 100})
	if err != nil {
		t.Fatal(err)
	}
	api.End()

	parar := rodar(t, p.relay().Rodar, p.worker("w1").Rodar)
	defer parar()
	p.esperarTodasFinais(10 * time.Second)
	parar()

	aceitar := spanPorNome(t, exp, "aceitar_pedido")
	relay := spanPorNome(t, exp, "relay.publicar")
	worker := spanPorNome(t, exp, "worker.liquidar")

	traco := api.SpanContext().TraceID()
	for _, s := range []tracetest.SpanStub{aceitar, relay, worker} {
		if s.SpanContext.TraceID() != traco {
			t.Errorf("%s está no trace %s, esperava %s", s.Name, s.SpanContext.TraceID(), traco)
		}
	}
	filho := func(f, pai tracetest.SpanStub) {
		t.Helper()
		if f.Parent.SpanID() != pai.SpanContext.SpanID() {
			t.Errorf("pai de %s = %s, esperava %s (%s)", f.Name, f.Parent.SpanID(), pai.SpanContext.SpanID(), pai.Name)
		}
	}
	if aceitar.Parent.SpanID() != api.SpanContext().SpanID() {
		t.Errorf("aceitar_pedido não é filho do span da API")
	}
	filho(relay, aceitar)
	filho(worker, relay)
	if relay.SpanKind != trace.SpanKindProducer || worker.SpanKind != trace.SpanKindConsumer {
		t.Errorf("tipos: relay=%v worker=%v", relay.SpanKind, worker.SpanKind)
	}
	// Causalidade: o worker só vê a mensagem depois de o relay a enviar, então
	// não pode começar antes de o relay começar. NÃO vale exigir que comece
	// depois do FIM do relay: o span do relay só fecha quando o XADD retorna, e
	// a mensagem já está visível no Redis antes disso. Um worker rápido (ou um
	// runner lento) começa alguns microssegundos antes de o span fechar; foi o
	// que o CI mostrou numa primeira versão deste teste.
	if worker.StartTime.Before(relay.StartTime) {
		t.Errorf("worker começou antes de o relay começar")
	}

	// Atributos úteis...
	attr := func(s tracetest.SpanStub, chave string) string {
		for _, a := range s.Attributes {
			if string(a.Key) == chave {
				return a.Value.String()
			}
		}
		return ""
	}
	if attr(worker, "pix.transferencia_id") != tr.ID || attr(worker, "pix.status") != "LIQUIDADA" {
		t.Errorf("atributos do worker: %v", worker.Attributes)
	}

	// ...e nenhum dado sensível em nenhum atributo, evento ou status de span.
	proibidos := []string{"ana@teste", "beto@teste", e2e}
	for _, s := range exp.GetSpans() {
		var texto []string
		for _, a := range s.Attributes {
			texto = append(texto, string(a.Key), a.Value.String())
		}
		for _, ev := range s.Events {
			texto = append(texto, ev.Name)
			for _, a := range ev.Attributes {
				texto = append(texto, a.Value.String())
			}
		}
		texto = append(texto, s.Name, s.Status.Description)
		junto := strings.Join(texto, " ")
		for _, x := range proibidos {
			if strings.Contains(junto, x) {
				t.Errorf("span %q vaza %q: %s", s.Name, x, junto)
			}
		}
	}
}

// Sem rastreamento nada muda: a outbox não guarda traceparent e a mensagem do
// stream não ganha o campo.
func TestSemRastreamentoNaoGravaTraceparent(t *testing.T) {
	otel.SetTracerProvider(noop.NewTracerProvider())
	p := novoPipeline(t)
	p.conta("ana", 1_000)
	p.conta("beto", 0)

	if _, _, err := (usecase.Receptor{Repo: p.repo}).Aceitar(p.ctx, usecase.Pedido{
		EndToEndID: p.proximoE2E(), ChavePagador: "ana@teste", ChaveRecebedor: "beto@teste", Valor: 100}); err != nil {
		t.Fatal(err)
	}
	var visto []repository.Pendente
	if _, err := p.repo.PublicarPendentesDoRastro(p.ctx, 10, func(_ context.Context, ps []repository.Pendente) ([]string, error) {
		visto = ps
		return p.fila.Publicar(p.ctx, []string{ps[0].TransferenciaID})
	}); err != nil {
		t.Fatal(err)
	}
	if len(visto) != 1 || visto[0].Traceparent != "" {
		t.Fatalf("pendentes: %+v", visto)
	}
	if err := p.fila.GarantirGrupo(p.ctx); err != nil {
		t.Fatal(err)
	}
	msgs, err := p.fila.Ler(p.ctx, "c", 10, time.Second)
	if err != nil || len(msgs) != 1 || msgs[0].Traceparent != "" {
		t.Fatalf("mensagens: %+v, erro=%v", msgs, err)
	}
}

// Um traceparent inválido (lixo na mensagem) não pode impedir a liquidação:
// rastreamento é diagnóstico, nunca caminho crítico.
func TestTraceparentInvalidoNaoImpedeLiquidacao(t *testing.T) {
	exp := ativarRastreamento(t)
	p := novoPipeline(t)
	ana, beto := p.conta("ana", 1_000), p.conta("beto", 0)
	tr := p.pedir(ana, beto, 100)
	exp.Reset()

	if _, err := p.fila.PublicarComRastro(p.ctx, []queue.Entrada{
		{TransferenciaID: tr.ID, Traceparent: "isto-nao-e-um-traceparent"}}); err != nil {
		t.Fatal(err)
	}
	parar := rodar(t, p.worker("w1").Rodar)
	defer parar()
	cont := p.esperarTodasFinais(10 * time.Second)
	if cont[domain.Liquidada] != 2 { // o aporte + a transferência
		t.Fatalf("status: %v", cont)
	}
	w := spanPorNome(t, exp, "worker.liquidar")
	if w.Parent.IsValid() {
		t.Errorf("span do worker tem pai %v apesar do traceparent inválido", w.Parent)
	}
}
