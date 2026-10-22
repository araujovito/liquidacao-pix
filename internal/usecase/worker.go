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
	"github.com/araujovito/liquidacao-pix/internal/telemetria"
)

// Worker consome o stream e liquida.
type Worker struct {
	Liquidador Liquidador
	Fila       *queue.Fila
	Log        *slog.Logger
	Consumidor string // nome único no grupo (ex.: host-pid-n)

	Metricas *metricas.Worker // opcional

	Lote int64
	// EsperaBase é quanto uma mensagem fica parada antes da primeira nova
	// tentativa; dobra a cada entrega. Precisa ser maior que o tempo normal de
	// uma liquidação, ou mensagens em andamento seriam reivindicadas à toa
	// (inofensivo, pela idempotência, mas desperdício).
	EsperaBase  time.Duration
	MaxEntregas int64
}

// Rodar consome até ctx ser cancelado. A mensagem em processamento termina
// antes de sair (encerramento gracioso).
func (w Worker) Rodar(ctx context.Context) error {
	if err := w.Fila.GarantirGrupo(ctx); err != nil {
		return err
	}
	esperaErro := 100 * time.Millisecond
	proximaReivindicacao := time.Now()
	for ctx.Err() == nil {
		if time.Now().After(proximaReivindicacao) {
			proximaReivindicacao = time.Now().Add(w.EsperaBase / 2)
			retomar, mortas, err := w.Fila.Reivindicar(ctx, w.Consumidor, w.EsperaBase, w.MaxEntregas)
			if err != nil && ctx.Err() == nil && !queue.SemGrupo(err) {
				w.Log.WarnContext(ctx, "worker: reivindicação falhou", "erro", err)
			}
			if mortas > 0 {
				w.Metricas.Mortas(mortas)
				w.Log.ErrorContext(ctx, "worker: mensagens movidas para mortas", "quantidade", mortas)
			}
			for _, m := range retomar {
				w.processar(m)
			}
		}

		msgs, err := w.Fila.Ler(ctx, w.Consumidor, w.Lote, time.Second)
		if queue.SemGrupo(err) {
			// Achado do teste de caos: sem isto, depois de o Redis perder o
			// stream, os workers ficavam recebendo NOGROUP para sempre.
			w.Log.WarnContext(ctx, "worker: grupo de consumo sumiu; recriando")
			w.Metricas.GrupoRecriado()
			err = w.Fila.GarantirGrupo(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			w.Metricas.Falha("ler")
			w.Log.WarnContext(ctx, "worker: leitura do stream falhou", "erro", err, "nova_tentativa_em", esperaErro)
			select {
			case <-ctx.Done():
			case <-time.After(esperaErro):
			}
			esperaErro = min(esperaErro*2, 5*time.Second)
			continue
		}
		esperaErro = 100 * time.Millisecond
		for _, m := range msgs {
			w.processar(m)
		}
	}
	return nil
}

// processar usa um contexto próprio, desligado do cancelamento do worker:
// um SIGTERM no meio da liquidação deixa a transação terminar em vez de
// desfazê-la e deixar a mensagem para outro worker.
func (w Worker) processar(m queue.Mensagem) {
	ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()

	// Continua o rastro do pedido: o pai é o span do relay, cujo contexto veio
	// na mensagem. Sem traceparent (rastreamento desligado na publicação), abre
	// um rastro novo.
	ctx, span := telemetria.Tracer().Start(telemetria.ComTraceparent(ctx, m.Traceparent), "worker.liquidar",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("pix.transferencia_id", m.TransferenciaID), attribute.Int64("pix.entrega", m.Entregas)))
	defer span.End()

	inicio := time.Now()
	t, err := w.Liquidador.Liquidar(ctx, m.TransferenciaID)
	w.Metricas.Tentativa(time.Since(inicio).Seconds())
	if err != nil {
		w.Metricas.Falha("liquidar")
		span.RecordError(err)
		span.SetStatus(codes.Error, "liquidação falhou; a mensagem volta por reivindicação")
		// Sem XACK: a mensagem fica pendente e volta por Reivindicar.
		w.Log.WarnContext(ctx, "worker: liquidação falhou", "transferencia", m.TransferenciaID, "entrega", m.Entregas, "erro", err)
		if err := w.Fila.AnotarErro(ctx, m, err); err != nil {
			w.Log.WarnContext(ctx, "worker: anotar erro falhou", "erro", err)
		}
		return
	}
	if err := w.Fila.Confirmar(ctx, m); err != nil {
		// Liquidou mas não confirmou: será entregue de novo e a segunda
		// liquidação não fará nada.
		w.Metricas.Falha("confirmar")
		span.RecordError(err)
		span.SetStatus(codes.Error, "liquidou, mas não confirmou a mensagem")
		w.Log.WarnContext(ctx, "worker: confirmação falhou", "transferencia", m.TransferenciaID, "erro", err)
		return
	}
	span.SetAttributes(attribute.String("pix.status", string(t.Status)))
	w.Log.DebugContext(ctx, "liquidada", "transferencia", t.ID, "status", t.Status)
}
