// Package web expõe os casos de uso por HTTP. Erros seguem o formato
// "problem details" (RFC 9457), para que o cliente distinga cada caso por um
// campo estável em vez de interpretar mensagens.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/metricas"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

// tamanhoMaximoCorpo: um pedido válido tem uns 200 bytes. O limite impede que
// um corpo gigante ocupe memória do servidor.
const tamanhoMaximoCorpo = 4 << 10

// Servidor reúne as dependências dos handlers.
type Servidor struct {
	Receptor usecase.Receptor
	Repo     *repository.Repositorio
	Log      *slog.Logger
	Metricas *metricas.API // opcional
}

// Rotas monta o roteador (padrões de rota do net/http, Go 1.22+).
func (s *Servidor) Rotas() http.Handler {
	mux := http.NewServeMux()
	// O span nomeado pelo padrão da rota (nunca pelo caminho, que tem o id)
	// mantém a cardinalidade baixa. /saude fica de fora: seria ruído.
	mux.Handle("POST /transferencias", otelhttp.NewHandler(http.HandlerFunc(s.criar), "POST /transferencias"))
	mux.Handle("GET /transferencias/{id}", otelhttp.NewHandler(http.HandlerFunc(s.consultar), "GET /transferencias/{id}"))
	mux.HandleFunc("GET /saude", s.saude)
	return s.Metricas.Middleware(mux)
}

type pedidoJSON struct {
	EndToEndID     string `json:"end_to_end_id"`
	ChavePagador   string `json:"chave_pagador"`
	ChaveRecebedor string `json:"chave_recebedor"`
	ValorCentavos  int64  `json:"valor_centavos"`
}

type transferenciaJSON struct {
	ID             string     `json:"id"`
	EndToEndID     string     `json:"end_to_end_id"`
	ValorCentavos  int64      `json:"valor_centavos"`
	Status         string     `json:"status"`
	MotivoRejeicao string     `json:"motivo_rejeicao,omitempty"`
	CriadaEm       time.Time  `json:"criada_em"`
	FinalizadaEm   *time.Time `json:"finalizada_em,omitempty"`
}

func paraJSON(t domain.Transferencia) transferenciaJSON {
	return transferenciaJSON{
		ID: t.ID, EndToEndID: t.EndToEndID, ValorCentavos: int64(t.Valor),
		Status: string(t.Status), MotivoRejeicao: t.MotivoRejeicao,
		CriadaEm: t.CriadaEm, FinalizadaEm: t.FinalizadaEm,
	}
}

// criar aceita o pedido e responde 202 Accepted. O 202 só sai depois do
// commit da transferência e da outbox (ADR 0001).
func (s *Servidor) criar(w http.ResponseWriter, r *http.Request) {
	tipo, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || tipo != "application/json" {
		problema(w, http.StatusUnsupportedMediaType, "tipo-de-conteudo-invalido", "use Content-Type: application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, tamanhoMaximoCorpo)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // campo com nome errado vira erro, não valor zero silencioso
	var p pedidoJSON
	if err = dec.Decode(&p); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			problema(w, http.StatusRequestEntityTooLarge, "corpo-grande-demais", "o corpo passa de 4 KiB")
			return
		}
		problema(w, http.StatusBadRequest, "json-invalido", err.Error())
		return
	}
	// Um pedido contém exatamente um valor JSON. Sem esta segunda leitura,
	// `{"pedido": "válido"} {"conteúdo": "ignorado"}` seria aceito.
	var extra any
	if err = dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			problema(w, http.StatusRequestEntityTooLarge, "corpo-grande-demais", "o corpo passa de 4 KiB")
			return
		}
		problema(w, http.StatusBadRequest, "json-invalido", "o corpo deve conter exatamente um objeto JSON")
		return
	}

	t, repetido, err := s.Receptor.Aceitar(r.Context(), usecase.Pedido{
		EndToEndID: p.EndToEndID, ChavePagador: p.ChavePagador,
		ChaveRecebedor: p.ChaveRecebedor, Valor: domain.Centavos(p.ValorCentavos),
	})
	switch {
	case err == nil:
	case errors.Is(err, usecase.ErrConflito):
		problema(w, http.StatusConflict, "end-to-end-em-conflito", err.Error())
		return
	case errors.Is(err, usecase.ErrChaveDesconhecida):
		problema(w, http.StatusUnprocessableEntity, "chave-desconhecida", err.Error())
		return
	case errors.Is(err, domain.ErrEndToEndInvalido), errors.Is(err, domain.ErrValorInvalido), errors.Is(err, domain.ErrMesmaConta):
		problema(w, http.StatusUnprocessableEntity, "pedido-invalido", err.Error())
		return
	default:
		s.erroInterno(w, r, err)
		return
	}

	w.Header().Set("Location", "/transferencias/"+t.ID)
	if repetido {
		// Mesma resposta da primeira vez; o cabeçalho só informa que nada novo
		// foi gravado.
		w.Header().Set("Idempotent-Replayed", "true")
	}
	s.Metricas.Aceito(repetido)
	responderJSON(w, http.StatusAccepted, paraJSON(t))
}

func (s *Servidor) consultar(w http.ResponseWriter, r *http.Request) {
	t, err := s.Repo.BuscarTransferencia(r.Context(), r.PathValue("id"))
	if errors.Is(err, repository.ErrNaoEncontrada) {
		problema(w, http.StatusNotFound, "nao-encontrada", "transferência não encontrada")
		return
	}
	if err != nil {
		s.erroInterno(w, r, err)
		return
	}
	responderJSON(w, http.StatusOK, paraJSON(t))
}

func (s *Servidor) saude(w http.ResponseWriter, r *http.Request) {
	ctx, cancelar := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancelar()
	if err := s.Repo.Ping(ctx); err != nil {
		problema(w, http.StatusServiceUnavailable, "banco-indisponivel", "postgres não respondeu")
		return
	}
	responderJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// erroInterno registra o erro e devolve uma mensagem genérica: detalhe de
// banco não vaza para o cliente.
func (s *Servidor) erroInterno(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.ErrorContext(r.Context(), "erro interno", "metodo", r.Method, "rota", r.URL.Path, "erro", err)
	problema(w, http.StatusInternalServerError, "erro-interno", "erro interno")
}

func problema(w http.ResponseWriter, status int, tipo, detalhe string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "urn:liquidacao-pix:erro:" + tipo,
		"title":  http.StatusText(status),
		"status": status,
		"detail": detalhe,
	})
}

func responderJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
