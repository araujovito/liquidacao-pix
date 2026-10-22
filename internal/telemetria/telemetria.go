// Package telemetria configura o rastreamento distribuído (OpenTelemetry) e
// carrega o contexto do rastro por dentro da outbox e do stream.
//
// O caminho de um pedido atravessa quatro processos e duas filas
// (API → outbox → relay → Redis → worker), e nenhuma chamada HTTP liga um elo
// ao seguinte. Por isso o contexto viaja como dado: a API grava o traceparent
// (padrão W3C) na linha da outbox, o relay o abre, cria o seu span e grava o
// próprio contexto na mensagem do stream, e o worker continua dali. O
// resultado é uma árvore única por pedido, com o tempo de espera de cada fila
// visível como intervalo entre spans.
//
// O que NUNCA vai para atributo de span: chave PIX, CPF, nome de titular,
// valor, end_to_end_id. Só o id da transferência (UUID opaco), o status e
// contagens. O corpo das requisições não é registrado.
package telemetria

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

const nomeDoTracer = "github.com/araujovito/liquidacao-pix"

// Tracer devolve o tracer dos spans manuais do projeto.
func Tracer() trace.Tracer { return otel.Tracer(nomeDoTracer) }

// Iniciar liga o rastreamento se OTEL_EXPORTER_OTLP_ENDPOINT estiver definida.
// Sem ela não faz nada: os spans viram no-op e o custo é desprezível. A
// amostragem segue as variáveis padrão do OpenTelemetry (OTEL_TRACES_SAMPLER
// e OTEL_TRACES_SAMPLER_ARG); sob carga pesada, reduza a razão.
//
// Devolve a função que esvazia e encerra o exportador; chame-a no desligamento
// com um contexto novo, para os últimos spans não se perderem.
func Iniciar(ctx context.Context, log *slog.Logger, servico string) (func(context.Context) error, error) {
	// O propagador é global mesmo sem exportador: o traceparent gravado na
	// outbox precisa continuar válido se o rastreamento for ligado depois.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("exportador OTLP: %w", err)
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(servico)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	log.Info("rastreamento ligado", "servico", servico, "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	return tp.Shutdown, nil
}

// Traceparent devolve o contexto do rastro de ctx no formato W3C
// ("00-<trace>-<span>-<flags>"), ou "" se ctx não tem span válido.
func Traceparent(ctx context.Context) string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ""
	}
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	return c.Get("traceparent")
}

// ComTraceparent devolve um contexto cujo span pai é o do traceparent. Valor
// vazio ou malformado devolve ctx sem pai: o span seguinte abre um rastro novo
// em vez de falhar.
func ComTraceparent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}
