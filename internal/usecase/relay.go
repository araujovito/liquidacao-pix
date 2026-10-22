package usecase

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/queue"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/telemetria"
)

// Relay copia a outbox do Postgres para o Redis Stream.
type Relay struct {
	Repo *repository.Repositorio
	Fila *queue.Fila
	Log  *slog.Logger

	Metricas *metricas.Relay // opcional

	Lote      int           // linhas por transação
	Intervalo time.Duration // espera quando a outbox está vazia

	// Varredura de órfãs: PENDENTE publicada há mais de IdadeOrfa volta para
	// a outbox, no máximo MaxRepublicacoes vezes.
	IdadeOrfa        time.Duration
	MaxRepublicacoes int
}

// Rodar publica até ctx ser cancelado. Erros de Redis ou Postgres não
// derrubam o relay: ele espera (cada vez mais, até 5 s) e tenta de novo, e a
// outbox guarda tudo enquanto isso.
func (r Relay) Rodar(ctx context.Context) error {
	espera := r.Intervalo
	proximaVarredura := time.Now().Add(r.IdadeOrfa / 2)
	for {
		n, err := r.Repo.PublicarPendentesDoRastro(ctx, r.Lote, r.publicar)
		if ctx.Err() != nil {
			return nil
		}
		switch {
		case err != nil:
			r.Metricas.Falha()
			r.Log.WarnContext(ctx, "relay: publicação falhou; outbox mantém os pedidos", "erro", err, "nova_tentativa_em", espera)
			espera = min(espera*2, 5*time.Second)
		case n == r.Lote:
			r.Metricas.Publicadas(n)
			espera = r.Intervalo
			continue // outbox cheia: emenda o próximo lote sem dormir
		default:
			r.Metricas.Publicadas(n)
			espera = r.Intervalo
		}

		if time.Now().After(proximaVarredura) {
			proximaVarredura = time.Now().Add(r.IdadeOrfa / 2)
			if k, err := r.varrerOrfas(ctx); err != nil {
				r.Log.WarnContext(ctx, "relay: varredura de órfãs falhou", "erro", err)
			} else if k > 0 {
				r.Metricas.Republicadas(k)
				r.Log.WarnContext(ctx, "relay: transferências pendentes republicadas", "quantidade", k)
				continue
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(espera):
		}
	}
}

// varrerOrfas republica as transferências cuja mensagem sumiu do Redis.
//
// A primeira versão republicava toda PENDENTE publicada há mais de IdadeOrfa.
// O teste de caos mostrou o problema: com a fila atrasada (entrada maior que a
// vazão do worker), mensagens que só estavam esperando eram tratadas como
// perdidas e publicadas de novo — 14 mil duplicatas numa rodada, carga extra
// justamente quando o sistema já está atrasado. Agora só volta o que de fato
// não está mais no stream.
//
// Percorre todas as candidatas em páginas. A versão que olhava só as
// primeiras 1.000 a cada 15 s recuperava ~67 por segundo: no teste de caos,
// depois de apagar o stream com 50 mil mensagens, não convergia em 4 min.
func (r Relay) varrerOrfas(ctx context.Context) (int64, error) {
	const pagina = 1_000
	var total int64
	var cursor int64
	for {
		candidatas, err := r.Repo.CandidatasOrfas(ctx, r.IdadeOrfa, r.MaxRepublicacoes, pagina, cursor)
		if err != nil || len(candidatas) == 0 {
			return total, err
		}
		cursor = candidatas[len(candidatas)-1].Linha

		mensagens := make([]string, 0, len(candidatas))
		for _, c := range candidatas {
			if c.MensagemID != "" {
				mensagens = append(mensagens, c.MensagemID)
			}
		}
		existem, err := r.Fila.Existentes(ctx, mensagens)
		if err != nil {
			return total, err
		}
		var perdidas []int64
		for _, c := range candidatas {
			if !existem[c.MensagemID] {
				perdidas = append(perdidas, c.Linha)
			}
		}
		if len(perdidas) > 0 {
			k, err := r.Repo.Republicar(ctx, perdidas)
			total += k
			if err != nil {
				return total, err
			}
		}
		if len(candidatas) < pagina {
			return total, nil
		}
	}
}

// publicar envia o lote ao stream continuando o rastro de cada pedido: abre um
// span "relay.publicar" filho do span que aceitou o pedido (o traceparent veio
// da outbox) e grava o contexto desse span na mensagem, para o worker seguir
// dali. O envio é um só (pipeline); o span de cada pedido começa no início do
// lote e termina quando ele acaba, que é a parte da vida do pedido passada
// aqui.
func (r Relay) publicar(ctx context.Context, pendentes []repository.Pendente) ([]string, error) {
	inicio := time.Now()
	spans := make([]trace.Span, len(pendentes))
	entradas := make([]queue.Entrada, len(pendentes))
	for i, p := range pendentes {
		pctx, span := telemetria.Tracer().Start(
			telemetria.ComTraceparent(ctx, p.Traceparent), "relay.publicar",
			trace.WithTimestamp(inicio), trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithAttributes(attribute.String("pix.transferencia_id", p.TransferenciaID)))
		spans[i] = span
		entradas[i] = queue.Entrada{TransferenciaID: p.TransferenciaID, Traceparent: telemetria.Traceparent(pctx)}
	}

	mensagens, err := r.Fila.PublicarComRastro(ctx, entradas)
	for _, span := range spans {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "publicação no stream falhou")
		}
		span.End()
	}
	return mensagens, err
}
