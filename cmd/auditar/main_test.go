package main

import (
	"errors"
	"testing"
)

type escritorQueFalha struct{ chamadas int }

func (e *escritorQueFalha) Write(p []byte) (int, error) {
	e.chamadas++
	if e.chamadas == 2 {
		return 0, errors.New("disco cheio")
	}
	return len(p), nil
}

func TestEscreverAncorasPropagaFalha(t *testing.T) {
	w := &escritorQueFalha{}
	if err := escreverAncoras(w, []string{"1 1 aa", "2 1 bb"}); err == nil {
		t.Fatal("a escrita parcial da âncora deveria falhar")
	}
}
