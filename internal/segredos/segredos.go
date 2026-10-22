// Package segredos lê chaveiros do ambiente. Nenhuma chave tem valor padrão e
// nenhuma mensagem de erro repete o valor de uma chave.
package segredos

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// TamanhoChave é o tamanho exigido das chaves, em bytes (HMAC-SHA256 e AES-256).
const TamanhoChave = 32

// IDValido aceita letras, dígitos, ponto, hífen e sublinhado: nada que
// confunda o formato "id:hex,id:hex" nem apareça estranho num log.
func IDValido(id string) bool {
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

// Validar confere identificadores e tamanhos de um conjunto de chaves e a
// existência da atual.
func Validar(prefixo, atual string, chaves map[string][]byte) error {
	if len(chaves) == 0 {
		return fmt.Errorf("%s: chaveiro sem chaves", prefixo)
	}
	for id, k := range chaves {
		if !IDValido(id) {
			return fmt.Errorf("%s: identificador de chave inválido %q", prefixo, id)
		}
		if len(k) != TamanhoChave {
			return fmt.Errorf("%s: chave %q tem %d bytes, esperado %d", prefixo, id, len(k), TamanhoChave)
		}
	}
	if _, ok := chaves[atual]; !ok {
		return fmt.Errorf("%s: chave atual %q não está no chaveiro", prefixo, atual)
	}
	return nil
}

// DoAmbiente lê a lista "id:hex[,id:hex]" de uma variável e o identificador
// da chave atual de outra. Devolve também as chaves já validadas.
func DoAmbiente(prefixo, varChaves, varAtual string) (atual string, chaves map[string][]byte, err error) {
	lista, atual := os.Getenv(varChaves), os.Getenv(varAtual)
	if lista == "" || atual == "" {
		return "", nil, fmt.Errorf("%s: defina %s e %s", prefixo, varChaves, varAtual)
	}
	chaves = map[string][]byte{}
	for _, par := range strings.Split(lista, ",") {
		id, h, ok := strings.Cut(strings.TrimSpace(par), ":")
		if !ok {
			return "", nil, fmt.Errorf("%s: %s deve ter o formato id:hex[,id:hex]", prefixo, varChaves)
		}
		if _, repetida := chaves[id]; repetida {
			return "", nil, fmt.Errorf("%s: identificador de chave repetido %q", prefixo, id)
		}
		k, err := hex.DecodeString(h)
		if err != nil {
			// Não repete o valor: ele é um segredo.
			return "", nil, fmt.Errorf("%s: chave %q não é hexadecimal", prefixo, id)
		}
		chaves[id] = k
	}
	if err := Validar(prefixo, atual, chaves); err != nil {
		return "", nil, err
	}
	return atual, chaves, nil
}
