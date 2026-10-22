package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
	"github.com/araujovito/liquidacao-pix/internal/web"
)

const e2e1 = "E1234567820261021143000000000001"

type ambiente struct {
	t    *testing.T
	pool *pgxpool.Pool
	srv  *httptest.Server
}

func novoAmbiente(t *testing.T) *ambiente {
	pool := testinfra.Postgres(t)
	repo := repository.New(pool)
	ctx := context.Background()
	liq := usecase.Liquidador{Repo: repo, Auditoria: testinfra.Chaveiro(t)}
	for i, chave := range []string{"ana@pix", "beto@pix"} {
		id, err := repo.CriarConta(ctx, chave, chave)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := liq.Aportar(ctx, id, 10_000, fmt.Sprintf("E9999999920261021143000000000%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	s := &web.Servidor{Receptor: usecase.Receptor{Repo: repo}, Repo: repo, Log: slog.New(slog.DiscardHandler)}
	srv := httptest.NewServer(s.Rotas())
	t.Cleanup(srv.Close)
	return &ambiente{t: t, pool: pool, srv: srv}
}

func pedido(e2e, de, para string, valor int64) string {
	return fmt.Sprintf(`{"end_to_end_id":%q,"chave_pagador":%q,"chave_recebedor":%q,"valor_centavos":%d}`, e2e, de, para, valor)
}

type resposta struct {
	status int
	cab    http.Header
	corpo  map[string]any
}

func (a *ambiente) req(metodo, rota, corpo string) resposta {
	a.t.Helper()
	r, err := http.NewRequest(metodo, a.srv.URL+rota, strings.NewReader(corpo))
	if err != nil {
		a.t.Fatal(err)
	}
	if corpo != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	out := resposta{status: res.StatusCode, cab: res.Header}
	_ = json.Unmarshal(b, &out.corpo)
	return out
}

func (a *ambiente) linhasNaOutbox() int {
	var n int
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func TestAceitaGravaComOutboxEConsulta(t *testing.T) {
	a := novoAmbiente(t)
	r := a.req("POST", "/transferencias", pedido(e2e1, "ana@pix", "beto@pix", 1_500))
	if r.status != http.StatusAccepted {
		t.Fatalf("status %d: %v", r.status, r.corpo)
	}
	if r.corpo["status"] != "PENDENTE" {
		t.Fatalf("esperava PENDENTE: %v", r.corpo)
	}
	if a.linhasNaOutbox() != 1 {
		t.Fatal("transferência aceita sem linha na outbox")
	}

	c := a.req("GET", r.cab.Get("Location"), "")
	if c.status != http.StatusOK || c.corpo["id"] != r.corpo["id"] {
		t.Fatalf("consulta: %d %v", c.status, c.corpo)
	}
}

func TestRepetirPedidoDevolveOMesmo(t *testing.T) {
	a := novoAmbiente(t)
	corpo := pedido(e2e1, "ana@pix", "beto@pix", 1_500)
	primeira := a.req("POST", "/transferencias", corpo)
	segunda := a.req("POST", "/transferencias", corpo)

	if segunda.status != http.StatusAccepted || segunda.corpo["id"] != primeira.corpo["id"] {
		t.Fatalf("repetição devolveu outra coisa: %d %v", segunda.status, segunda.corpo)
	}
	if segunda.cab.Get("Idempotent-Replayed") != "true" || primeira.cab.Get("Idempotent-Replayed") != "" {
		t.Fatal("cabeçalho Idempotent-Replayed errado")
	}
	if a.linhasNaOutbox() != 1 {
		t.Fatal("repetição gerou segunda linha na outbox")
	}
}

func TestMesmaChaveComOutroConteudoConflita(t *testing.T) {
	a := novoAmbiente(t)
	a.req("POST", "/transferencias", pedido(e2e1, "ana@pix", "beto@pix", 1_500))
	r := a.req("POST", "/transferencias", pedido(e2e1, "ana@pix", "beto@pix", 1_501))
	if r.status != http.StatusConflict || r.corpo["type"] != "urn:liquidacao-pix:erro:end-to-end-em-conflito" {
		t.Fatalf("esperava 409: %d %v", r.status, r.corpo)
	}
}

// Vinte clientes mandam o mesmo pedido ao mesmo tempo (ex.: retentativa
// agressiva). Todos recebem a mesma transferência e só uma linha vai para a
// outbox: quem decide é a restrição UNIQUE do banco.
func TestPedidosSimultaneosComMesmaChave(t *testing.T) {
	a := novoAmbiente(t)
	corpo := pedido(e2e1, "ana@pix", "beto@pix", 700)
	ids := make([]any, 20)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			r := a.req("POST", "/transferencias", corpo)
			if r.status != http.StatusAccepted {
				t.Errorf("status %d: %v", r.status, r.corpo)
			}
			ids[i] = r.corpo["id"]
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids diferentes: %v", ids)
		}
	}
	if n := a.linhasNaOutbox(); n != 1 {
		t.Fatalf("%d linhas na outbox", n)
	}
}

// Pedidos concorrentes com a mesma chave, mas conteúdos diferentes, devem
// eleger exatamente um conteúdo. Os demais recebem conflito e nenhum pedido
// cria uma segunda transferência ou linha de outbox.
func TestPedidosSimultaneosConflitantes(t *testing.T) {
	a := novoAmbiente(t)
	const clientes = 20
	resultados := make(chan resposta, clientes)
	inicio := make(chan struct{})
	var wg sync.WaitGroup
	for i := range clientes {
		wg.Go(func() {
			<-inicio
			resultados <- a.req("POST", "/transferencias", pedido(e2e1, "ana@pix", "beto@pix", int64(700+i)))
		})
	}
	close(inicio)
	wg.Wait()
	close(resultados)

	aceitas, conflitos := 0, 0
	for r := range resultados {
		switch r.status {
		case http.StatusAccepted:
			aceitas++
		case http.StatusConflict:
			conflitos++
		default:
			t.Fatalf("status inesperado %d: %v", r.status, r.corpo)
		}
	}
	if aceitas != 1 || conflitos != clientes-1 {
		t.Fatalf("aceitas=%d conflitos=%d", aceitas, conflitos)
	}
	if n := a.linhasNaOutbox(); n != 1 {
		t.Fatalf("%d linhas na outbox", n)
	}
}

func TestErrosDeEntrada(t *testing.T) {
	a := novoAmbiente(t)
	casos := []struct {
		nome   string
		corpo  string
		status int
		tipo   string
	}{
		{"json quebrado", `{"end_to_end_id":`, 400, "json-invalido"},
		{"dois objetos", pedido(e2e1, "ana@pix", "beto@pix", 10) + `{}`, 400, "json-invalido"},
		{"campo desconhecido", `{"valor":10}`, 400, "json-invalido"},
		{"corpo grande", `{"end_to_end_id":"` + strings.Repeat("x", 5000) + `"}`, 413, "corpo-grande-demais"},
		{"chave inexistente", pedido(e2e1, "ana@pix", "ninguem@pix", 10), 422, "chave-desconhecida"},
		{"valor zero", pedido(e2e1, "ana@pix", "beto@pix", 0), 422, "pedido-invalido"},
		{"e2e fora do formato", pedido("abc", "ana@pix", "beto@pix", 10), 422, "pedido-invalido"},
		{"mesma conta", pedido(e2e1, "ana@pix", "ana@pix", 10), 422, "pedido-invalido"},
		// A conta de liquidez pode ficar negativa; debitá-la pela API criaria
		// dinheiro do nada. Para a API, ela não existe.
		{"debitar liquidez", pedido(e2e1, repository.ChaveLiquidez, "ana@pix", 10), 422, "chave-desconhecida"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := a.req("POST", "/transferencias", c.corpo)
			if r.status != c.status || r.corpo["type"] != "urn:liquidacao-pix:erro:"+c.tipo {
				t.Fatalf("esperava %d %s, veio %d %v", c.status, c.tipo, r.status, r.corpo)
			}
			if r.cab.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("content-type %q", r.cab.Get("Content-Type"))
			}
		})
	}
	if a.linhasNaOutbox() != 0 {
		t.Fatal("pedido inválido chegou à outbox")
	}
}

func TestContentTypeDoPedido(t *testing.T) {
	a := novoAmbiente(t)
	corpo := pedido(e2e1, "ana@pix", "beto@pix", 10)
	for _, caso := range []struct {
		nome, contentType string
		status            int
	}{
		{"ausente", "", http.StatusUnsupportedMediaType},
		{"texto", "text/plain", http.StatusUnsupportedMediaType},
		{"json com charset", "application/json; charset=utf-8", http.StatusAccepted},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodPost, a.srv.URL+"/transferencias", strings.NewReader(corpo))
			if err != nil {
				t.Fatal(err)
			}
			if caso.contentType != "" {
				r.Header.Set("Content-Type", caso.contentType)
			}
			res, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != caso.status {
				t.Fatalf("status %d, esperava %d", res.StatusCode, caso.status)
			}
		})
	}
}

func TestConsultaInexistente(t *testing.T) {
	a := novoAmbiente(t)
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "nao-e-uuid", "'; DROP TABLE contas; --"} {
		if r := a.req("GET", "/transferencias/"+strings.ReplaceAll(id, " ", "%20"), ""); r.status != http.StatusNotFound {
			t.Errorf("%q: status %d", id, r.status)
		}
	}
}

func TestSaude(t *testing.T) {
	a := novoAmbiente(t)
	if r := a.req("GET", "/saude", ""); r.status != http.StatusOK {
		t.Fatalf("status %d", r.status)
	}
}
