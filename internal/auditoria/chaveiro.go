// Package auditoria assina as decisões de liquidação numa cadeia de HMAC por
// conta (ADR 0006). Aqui ficam o formato dos bytes assinados e o chaveiro; a
// gravação está no repositório e a verificação em Verificar.
package auditoria

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// TamanhoChave é o tamanho exigido das chaves do HMAC, em bytes.
const TamanhoChave = 32

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
	if len(chaves) == 0 {
		return nil, errors.New("auditoria: chaveiro sem chaves")
	}
	copia := make(map[string][]byte, len(chaves))
	for id, k := range chaves {
		if !idValido(id) {
			return nil, fmt.Errorf("auditoria: identificador de chave inválido %q", id)
		}
		if len(k) != TamanhoChave {
			return nil, fmt.Errorf("auditoria: chave %q tem %d bytes, esperado %d", id, len(k), TamanhoChave)
		}
		copia[id] = append([]byte(nil), k...)
	}
	if _, ok := copia[atual]; !ok {
		return nil, fmt.Errorf("auditoria: chave atual %q não está no chaveiro", atual)
	}
	return &Chaveiro{atual: atual, chaves: copia}, nil
}

// ChaveiroDoAmbiente lê AUDITORIA_CHAVES ("v1:<hex>,v2:<hex>") e
// AUDITORIA_CHAVE_ATUAL. Não há valor padrão: sem as variáveis, falha.
func ChaveiroDoAmbiente() (*Chaveiro, error) {
	lista := os.Getenv("AUDITORIA_CHAVES")
	atual := os.Getenv("AUDITORIA_CHAVE_ATUAL")
	if lista == "" || atual == "" {
		return nil, errors.New("auditoria: defina AUDITORIA_CHAVES e AUDITORIA_CHAVE_ATUAL")
	}
	chaves := map[string][]byte{}
	for _, par := range strings.Split(lista, ",") {
		id, h, ok := strings.Cut(strings.TrimSpace(par), ":")
		if !ok {
			return nil, errors.New("auditoria: AUDITORIA_CHAVES deve ter o formato id:hex[,id:hex]")
		}
		if _, repetida := chaves[id]; repetida {
			return nil, fmt.Errorf("auditoria: identificador de chave repetido %q", id)
		}
		k, err := hex.DecodeString(h)
		if err != nil {
			// Não repete o valor: ele é um segredo.
			return nil, fmt.Errorf("auditoria: chave %q não é hexadecimal", id)
		}
		chaves[id] = k
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

// idValido aceita letras, dígitos, ponto, hífen e sublinhado: nada que
// confunda o formato "id:hex,id:hex" nem apareça estranho num log.
func idValido(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}
