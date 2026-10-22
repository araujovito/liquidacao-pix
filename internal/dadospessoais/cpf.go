// Package dadospessoais protege o CPF do titular: AES-256-GCM com uma chave de
// dados por conta, embrulhada por uma chave-mestra do ambiente (ADR 0006).
// Apagar o dado é descartar a chave da conta; o texto cifrado fica e deixa de
// poder ser lido.
package dadospessoais

import (
	"errors"
	"strings"
)

// ErrCPFInvalido é devolvido por NormalizarCPF.
var ErrCPFInvalido = errors.New("CPF inválido")

// NormalizarCPF aceita 11 dígitos com ou sem pontuação e devolve só os
// dígitos, depois de conferir os dois dígitos verificadores. Rejeita as
// sequências repetidas (111.111.111-11), que passam na conta mas não existem.
func NormalizarCPF(entrada string) (string, error) {
	var d strings.Builder
	for _, r := range entrada {
		switch {
		case r >= '0' && r <= '9':
			d.WriteRune(r)
		case r == '.' || r == '-' || r == ' ':
		default:
			return "", ErrCPFInvalido
		}
	}
	cpf := d.String()
	if len(cpf) != 11 || strings.Count(cpf, string(cpf[0])) == 11 {
		return "", ErrCPFInvalido
	}
	if digito(cpf[:9], 10) != cpf[9] || digito(cpf[:10], 11) != cpf[10] {
		return "", ErrCPFInvalido
	}
	return cpf, nil
}

// digito calcula um dígito verificador: soma ponderada dos dígitos de base,
// com pesos de peso-1 a 2, módulo 11.
func digito(base string, peso int) byte {
	soma := 0
	for i := range len(base) {
		soma += int(base[i]-'0') * (peso - i)
	}
	r := (soma * 10) % 11
	if r == 10 {
		r = 0
	}
	return byte('0' + r)
}

// Mascarar mostra só os dígitos do meio (***.456.789-**), o suficiente para
// uma pessoa reconhecer o dado sem que ele vá para um log ou uma tela inteiro.
func Mascarar(cpf string) string {
	if len(cpf) != 11 {
		return "***"
	}
	return "***." + cpf[3:6] + "." + cpf[6:9] + "-**"
}
