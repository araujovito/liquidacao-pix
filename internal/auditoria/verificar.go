package auditoria

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Tipos de problema que a verificação acusa. São estáveis: os testes e o
// runbook dependem deles.
const (
	ProblemaSeq           = "seq"           // buraco, repetição ou reordenação
	ProblemaElo           = "elo"           // mac_anterior não é o MAC da entrada anterior
	ProblemaMAC           = "mac"           // conteúdo alterado ou assinatura forjada
	ProblemaChave         = "chave"         // assinada por chave que o chaveiro não tem
	ProblemaTransferencia = "transferencia" // entrada difere da transferência que diz registrar
	ProblemaCabeca        = "cabeca"        // cabeça em contas não aponta para a última entrada
	ProblemaSemEntrada    = "sem-entrada"   // decisão final sem entrada na cadeia
	ProblemaAncora        = "ancora"        // cadeia recuou ou entrada ancorada foi trocada
)

// Problema é um achado da verificação, com a posição exata.
type Problema struct {
	ContaID int64
	Seq     int64 // 0 quando o achado não é de uma entrada (cabeça, decisão sem entrada)
	Tipo    string
	Detalhe string
}

func (p Problema) String() string {
	if p.Seq == 0 {
		return fmt.Sprintf("conta %d: %s: %s", p.ContaID, p.Tipo, p.Detalhe)
	}
	return fmt.Sprintf("conta %d seq %d: %s: %s", p.ContaID, p.Seq, p.Tipo, p.Detalhe)
}

// Decisao é o que a tabela transferencias diz sobre a transferência de uma
// entrada, lido junto com ela.
type Decisao struct {
	Existe       bool
	Status       string
	Motivo       *string
	PagadorID    int64
	RecebedorID  int64
	Valor        int64
	FinalizadaEm *time.Time
}

// EntradaLida é uma entrada como está no banco, com a transferência que ela
// registra.
type EntradaLida struct {
	Entrada
	Transferencia Decisao
	// Defeito descreve um problema de formato visto na leitura (um MAC que não
	// tem 32 bytes, por exemplo). Quando preenchido, os MACs acima não são
	// confiáveis e a entrada é acusada sem ser conferida.
	Defeito string
	// MACLido indica que MAC tinha exatamente 32 bytes, mesmo quando outro
	// campo da entrada tinha formato inválido. Preservá-lo evita inventar
	// problemas de cabeça, âncora e elo na entrada seguinte.
	MACLido bool
}

// Cabeca é a posição e o MAC da última entrada, como a conta os guarda.
type Cabeca struct {
	Seq int64
	MAC [TamanhoMAC]byte
}

// ContaLida reúne o que a verificação precisa de uma conta.
type ContaLida struct {
	ID       int64
	Cabeca   Cabeca
	Entradas []EntradaLida // em ordem de seq
	// SemEntrada lista decisões finais em que a conta participou e que não
	// têm entrada na cadeia dela.
	SemEntrada []uuid.UUID
}

// Ancora é a posição e o MAC de uma entrada guardados fora do banco.
type Ancora struct {
	ContaID int64
	Seq     int64
	MAC     [TamanhoMAC]byte
}

// VerificarConta confere uma cadeia inteira e devolve todos os achados, cada
// um com a posição. Não pára no primeiro: depois de um problema continua
// comparando cada entrada com a anterior como está gravada, de modo que uma
// única adulteração aparece uma vez, no lugar certo, e não em cascata.
func (c *Chaveiro) VerificarConta(conta ContaLida, ancoras []Ancora) []Problema {
	var ps []Problema
	achar := func(seq int64, tipo, f string, a ...any) {
		ps = append(ps, Problema{ContaID: conta.ID, Seq: seq, Tipo: tipo, Detalhe: fmt.Sprintf(f, a...)})
	}

	var anterior [TamanhoMAC]byte // a primeira entrada parte de zeros
	anteriorConhecido := true     // falso depois de uma entrada com defeito de formato
	esperado := int64(1)
	for _, l := range conta.Entradas {
		e := l.Entrada
		if e.ContaID != conta.ID {
			achar(e.Seq, ProblemaSeq, "entrada de outra conta (%d)", e.ContaID)
		}
		if e.Seq != esperado {
			achar(e.Seq, ProblemaSeq, "esperada a posição %d", esperado)
		}
		esperado = e.Seq + 1
		if l.Defeito != "" {
			// A entrada não pode ser conferida. Se o MAC atual era legível, ele
			// ainda serve como valor opaco para cabeça, âncora e elo seguinte.
			achar(e.Seq, ProblemaMAC, "%s", l.Defeito)
			anterior, anteriorConhecido = e.MAC, l.MACLido
			continue
		}
		if anteriorConhecido && !bytes.Equal(e.MACAnterior[:], anterior[:]) {
			achar(e.Seq, ProblemaElo, "mac_anterior não é o MAC da entrada anterior")
		}
		anterior, anteriorConhecido = e.MAC, true

		switch err := c.Conferir(e); {
		case err == nil:
		case errors.Is(err, ErrChaveDesconhecida):
			achar(e.Seq, ProblemaChave, "%v", err)
		default:
			achar(e.Seq, ProblemaMAC, "o conteúdo não bate com a assinatura")
		}
		for _, d := range divergencias(e, l.Transferencia) {
			achar(e.Seq, ProblemaTransferencia, "%s", d)
		}
	}

	ultima := Cabeca{}
	if n := len(conta.Entradas); n > 0 {
		ultima = Cabeca{Seq: conta.Entradas[n-1].Seq, MAC: conta.Entradas[n-1].MAC}
	}
	if conta.Cabeca != ultima {
		achar(0, ProblemaCabeca, "a cabeça da conta aponta para seq %d e a última entrada é seq %d", conta.Cabeca.Seq, ultima.Seq)
	}
	for _, id := range conta.SemEntrada {
		achar(0, ProblemaSemEntrada, "a transferência %s foi decidida e não tem entrada nesta cadeia", id)
	}

	for _, a := range ancoras {
		if a.ContaID != conta.ID {
			continue
		}
		achada := false
		for _, l := range conta.Entradas {
			if l.Seq == a.Seq {
				achada = l.MAC == a.MAC
				break
			}
		}
		if !achada {
			achar(a.Seq, ProblemaAncora, "a entrada ancorada não existe mais ou foi trocada")
		}
	}
	return ps
}

func divergencias(e Entrada, t Decisao) []string {
	if !t.Existe {
		return []string{"a transferência registrada não existe"}
	}
	var ds []string
	if e.ContaID != t.PagadorID && e.ContaID != t.RecebedorID {
		ds = append(ds, "a conta não participa da transferência")
	}
	if e.Status != t.Status {
		ds = append(ds, fmt.Sprintf("status %s na entrada e %s na transferência", e.Status, t.Status))
	}
	if (e.Motivo == nil) != (t.Motivo == nil) || e.Motivo != nil && *e.Motivo != *t.Motivo {
		ds = append(ds, "motivo diferente")
	}
	if e.PagadorID != t.PagadorID || e.RecebedorID != t.RecebedorID {
		ds = append(ds, "partes diferentes")
	}
	if e.ValorCentavos != t.Valor {
		ds = append(ds, fmt.Sprintf("valor %d na entrada e %d na transferência", e.ValorCentavos, t.Valor))
	}
	if t.FinalizadaEm == nil || !e.DecididaEm.Equal(*t.FinalizadaEm) {
		ds = append(ds, "decidida_em difere de finalizada_em")
	}
	return ds
}
