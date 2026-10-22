// Package auditoria assina as decisões de liquidação numa cadeia de HMAC por
// conta (ADR 0006). Aqui ficam o formato dos bytes assinados e o chaveiro; a
// gravação está no repositório e a verificação em Verificar.
package auditoria

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/araujovito/liquidacao-pix/internal/segredos"
)

// TamanhoChave é o tamanho exigido das chaves do HMAC, em bytes.
const TamanhoChave = segredos.TamanhoChave

var (
	// ErrSemChaveiro é devolvido quando se tenta liquidar sem chaveiro: uma
	// liquidação sem trilha assinada não deve acontecer em silêncio.
	ErrSemChaveiro = errors.New("auditoria: chaveiro não configurado")
	// ErrChaveDesconhecida indica uma entrada assinada por uma chave que o
	// chaveiro não tem.
	ErrChaveDesconhecida = errors.New("auditoria: chave desconhecida")
	// ErrMACInvalido indica que o MAC da entrada não confere com o conteúdo.
	ErrMACInvalido = errors.New("auditoria: MAC não confere")
)

// Chaveiro guarda as chaves do HMAC por identificador. Uma é a atual (assina
// as entradas novas); as demais só servem para verificar o passado.
type Chaveiro struct {
	atual  string
	chaves map[string][]byte
}

// NovoChaveiro monta um chaveiro. Cada chave precisa ter TamanhoChave bytes e
// o identificador da atual precisa existir.
func NovoChaveiro(atual string, chaves map[string][]byte) (*Chaveiro, error) {
	if err := segredos.Validar("auditoria", atual, chaves); err != nil {
		return nil, err
	}
	copia := make(map[string][]byte, len(chaves))
	for id, k := range chaves {
		copia[id] = append([]byte(nil), k...)
	}
	return &Chaveiro{atual: atual, chaves: copia}, nil
}

// ChaveiroDoAmbiente lê AUDITORIA_CHAVES ("v1:<hex>,v2:<hex>") e
// AUDITORIA_CHAVE_ATUAL. Não há valor padrão: sem as variáveis, falha.
func ChaveiroDoAmbiente() (*Chaveiro, error) {
	atual, chaves, err := segredos.DoAmbiente("auditoria", "AUDITORIA_CHAVES", "AUDITORIA_CHAVE_ATUAL")
	if err != nil {
		return nil, err
	}
	return NovoChaveiro(atual, chaves)
}

// Atual devolve o identificador da chave que assina as entradas novas.
func (c *Chaveiro) Atual() string { return c.atual }

func (c *Chaveiro) mac(id string, dados []byte) ([]byte, error) {
	k, ok := c.chaves[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrChaveDesconhecida, id)
	}
	h := hmac.New(sha256.New, k)
	h.Write(dados)
	return h.Sum(nil), nil
}
