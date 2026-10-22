package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
	"github.com/araujovito/liquidacao-pix/internal/web"
)

const arquivoDoContrato = "../../docs/openapi.yaml"

// O contrato é escrito à mão; sem um teste, ele diverge da API sem ninguém
// perceber, e documentação errada é pior do que nenhuma. Este teste passa
// requisições REAIS pela API, contra um Postgres real, e valida cada resposta
// (status, cabeçalhos e corpo) contra docs/openapi.yaml, nos dois sentidos:
//
//   - código → contrato: toda resposta que a API devolve precisa estar
//     documentada (um status ou um campo novo sem atualizar o contrato reprova);
//   - contrato → código: toda resposta documentada precisa ser exercida por
//     algum caso (documentar um status que a API nunca devolve também reprova).
func TestAPIRespeitaOContratoOpenAPI(t *testing.T) {
	ctx := context.Background()
	textoDoContrato, err := os.ReadFile(arquivoDoContrato)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromFile(arquivoDoContrato)
	if err != nil {
		t.Fatalf("carregar o contrato: %v", err)
	}
	// Validate confere o documento e também os exemplos contra os esquemas.
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("o contrato é inválido: %v", err)
	}
	doc.Servers = nil // as requisições vão a um servidor de teste com porta aleatória
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	openapi3filter.RegisterBodyDecoder("application/problem+json", openapi3filter.JSONBodyDecoder)

	a := novoAmbiente(t)

	// Segundo servidor, cujo banco já está fechado: provoca 500 e 503 de verdade.
	poolQuebrado, err := pgxpool.New(ctx, a.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	poolQuebrado.Close()
	repoQuebrado := repository.New(poolQuebrado)
	quebrado := httptest.NewServer((&web.Servidor{
		Receptor: usecase.Receptor{Repo: repoQuebrado}, Repo: repoQuebrado, Log: slog.New(slog.DiscardHandler),
	}).Rotas())
	t.Cleanup(quebrado.Close)

	exercidas := map[string]bool{}

	// chamar faz a requisição de verdade, valida a resposta contra o contrato e
	// devolve o corpo. tipoEsperado, se não vazio, é o campo "type" do problema.
	chamar := func(nome string, srv *httptest.Server, metodo, caminho, contentType string, corpo []byte, statusEsperado int, tipoEsperado string) (http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, metodo, srv.URL+caminho, bytes.NewReader(corpo))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)

		if res.StatusCode != statusEsperado {
			t.Fatalf("%s: status %d, esperava %d (corpo: %s)", nome, res.StatusCode, statusEsperado, b)
		}
		route, params, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("%s: a rota %s %s não está no contrato: %v", nome, metodo, caminho, err)
		}
		err = openapi3filter.ValidateResponse(ctx, &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
			Status:                 res.StatusCode,
			Header:                 res.Header,
			Body:                   io.NopCloser(bytes.NewReader(b)),
			// Sem isto, um status que o contrato não documenta passaria em silêncio.
			Options: &openapi3filter.Options{IncludeResponseStatus: true},
		})
		if err != nil {
			t.Errorf("%s: a resposta viola o contrato: %v\ncorpo: %s", nome, err, b)
		}
		exercidas[chaveDaResposta(route, res.StatusCode)] = true

		if tipoEsperado != "" {
			var p struct{ Type string }
			_ = json.Unmarshal(b, &p)
			if p.Type != "urn:liquidacao-pix:erro:"+tipoEsperado {
				t.Errorf("%s: type = %q, esperava urn:liquidacao-pix:erro:%s", nome, p.Type, tipoEsperado)
			}
			if !strings.Contains(string(textoDoContrato), "erro:"+tipoEsperado) {
				t.Errorf("%s: o type %q não aparece no contrato", nome, tipoEsperado)
			}
		}
		return res.Header, b
	}

	const json_ = "application/json"
	e2e := "E1234567820261022100000000000001"
	corpoOK := []byte(pedido(e2e, "ana@pix", "beto@pix", 1_500))

	// --- POST /transferencias ---------------------------------------------
	cab, b := chamar("202 novo", a.srv, "POST", "/transferencias", json_, corpoOK, 202, "")
	var criada struct{ ID string }
	_ = json.Unmarshal(b, &criada)
	if cab.Get("Location") != "/transferencias/"+criada.ID || cab.Get("Idempotent-Replayed") != "" {
		t.Errorf("202 novo: Location=%q Idempotent-Replayed=%q", cab.Get("Location"), cab.Get("Idempotent-Replayed"))
	}

	// A idempotência o OpenAPI não modela; este é o lugar onde o contrato a prova.
	cab, b = chamar("202 repetido", a.srv, "POST", "/transferencias", json_, corpoOK, 202, "")
	var repetida struct{ ID string }
	_ = json.Unmarshal(b, &repetida)
	if repetida.ID != criada.ID || cab.Get("Idempotent-Replayed") != "true" {
		t.Errorf("202 repetido: id %q (esperava %q), Idempotent-Replayed=%q", repetida.ID, criada.ID, cab.Get("Idempotent-Replayed"))
	}

	chamar("409 mesma chave, outro valor", a.srv, "POST", "/transferencias", json_,
		[]byte(pedido(e2e, "ana@pix", "beto@pix", 1_501)), 409, "end-to-end-em-conflito")
	chamar("400 json quebrado", a.srv, "POST", "/transferencias", json_, []byte(`{"end_to_end_id":`), 400, "json-invalido")
	chamar("400 conteúdo depois do objeto", a.srv, "POST", "/transferencias", json_, append(append([]byte{}, corpoOK...), []byte(` {"x":1}`)...), 400, "json-invalido")
	chamar("413 corpo grande", a.srv, "POST", "/transferencias", json_, []byte(`{"end_to_end_id":"`+strings.Repeat("x", 5000)+`"}`), 413, "corpo-grande-demais")
	chamar("415 tipo de conteúdo", a.srv, "POST", "/transferencias", "text/plain", corpoOK, 415, "tipo-de-conteudo-invalido")
	chamar("422 regra de domínio", a.srv, "POST", "/transferencias", json_,
		[]byte(pedido("E1234567820261022100000000000002", "ana@pix", "beto@pix", 0)), 422, "pedido-invalido")
	chamar("422 chave desconhecida", a.srv, "POST", "/transferencias", json_,
		[]byte(pedido("E1234567820261022100000000000003", "ana@pix", "ninguem@pix", 10)), 422, "chave-desconhecida")
	chamar("500 banco fechado", quebrado, "POST", "/transferencias", json_,
		[]byte(pedido("E1234567820261022100000000000004", "ana@pix", "beto@pix", 10)), 500, "erro-interno")

	// --- GET /transferencias/{id} -----------------------------------------
	chamar("200 consulta", a.srv, "GET", "/transferencias/"+criada.ID, "", nil, 200, "")
	chamar("404 uuid inexistente", a.srv, "GET", "/transferencias/00000000-0000-0000-0000-000000000000", "", nil, 404, "nao-encontrada")
	chamar("500 consulta com banco fechado", quebrado, "GET", "/transferencias/"+criada.ID, "", nil, 500, "erro-interno")

	// --- GET /saude ---------------------------------------------------------
	chamar("200 saúde", a.srv, "GET", "/saude", "", nil, 200, "")
	chamar("503 saúde com banco fechado", quebrado, "GET", "/saude", "", nil, 503, "banco-indisponivel")

	// --- contrato → código: nada documentado pode ficar sem ser exercido ----
	var faltando []string
	for caminho, item := range doc.Paths.Map() {
		for metodo, op := range item.Operations() {
			for status := range op.Responses.Map() {
				if !exercidas[fmt.Sprintf("%s %s %s", metodo, caminho, status)] {
					faltando = append(faltando, fmt.Sprintf("%s %s → %s", metodo, caminho, status))
				}
			}
		}
	}
	sort.Strings(faltando)
	if len(faltando) > 0 {
		t.Errorf("respostas documentadas que nenhum caso exercita: %v", faltando)
	}
}

func chaveDaResposta(r *routers.Route, status int) string {
	return fmt.Sprintf("%s %s %d", r.Method, r.Path, status)
}
