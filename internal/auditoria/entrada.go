package auditoria

import (
	"crypto/hmac"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Dominio identifica o formato dos bytes assinados. Mudar a representação
// exige uma versão nova, nunca uma alteração silenciosa desta.
const Dominio = "pix-auditoria/v1"

// TamanhoMAC é o tamanho do HMAC-SHA256, em bytes.
const TamanhoMAC = 32

const (
	StatusLiquidada = "LIQUIDADA"
	StatusRejeitada = "REJEITADA"
)

// Entrada é um elo da cadeia de uma conta: a decisão sobre uma transferência,
// a posição na cadeia e a assinatura.
type Entrada struct {
	ContaID         int64
	Seq             int64
	TransferenciaID uuid.UUID
	Status          string
	Motivo          *string // presente se, e somente se, Status == REJEITADA
	PagadorID       int64
	RecebedorID     int64
	ValorCentavos   int64
	DecididaEm      time.Time // em microssegundos; ver Validar
	ChaveID         string
	MACAnterior     [TamanhoMAC]byte
	MAC             [TamanhoMAC]byte
}

// Validar recusa uma entrada que não poderia ter vindo de uma decisão real.
func (e Entrada) Validar() error {
	switch {
	case e.ContaID <= 0:
		return errors.New("auditoria: conta inválida")
	case e.Seq <= 0:
		return errors.New("auditoria: seq deve começar em 1")
	case e.Status != StatusLiquidada && e.Status != StatusRejeitada:
		return fmt.Errorf("auditoria: status %q não é uma decisão final", e.Status)
	case (e.Status == StatusRejeitada) != (e.Motivo != nil):
		return errors.New("auditoria: motivo deve existir se, e somente se, a transferência foi rejeitada")
	case !e.DecididaEm.Equal(e.DecididaEm.Truncate(time.Microsecond)):
		// O banco guarda microssegundos. Um instante mais fino assinaria bytes
		// que nunca mais se reconstroem a partir do que foi gravado.
		return errors.New("auditoria: decidida_em deve estar em microssegundos")
	}
	return nil
}

// Codificar devolve os bytes que o HMAC assina. O formato é explícito: texto
// com tamanho prefixado, inteiros em 8 bytes big-endian (complemento de dois),
// UUID e MAC crus, campo opcional com um byte de presença. Nenhum delimitador,
// então dois conteúdos diferentes nunca geram os mesmos bytes.
func (e Entrada) Codificar() []byte {
	var b []byte
	b = appendTexto(b, Dominio)
	b = appendInt(b, e.ContaID)
	b = appendInt(b, e.Seq)
	b = append(b, e.TransferenciaID[:]...)
	b = appendTexto(b, e.Status)
	if e.Motivo == nil {
		b = append(b, 0)
	} else {
		b = append(b, 1)
		b = appendTexto(b, *e.Motivo)
	}
	b = appendInt(b, e.PagadorID)
	b = appendInt(b, e.RecebedorID)
	b = appendInt(b, e.ValorCentavos)
	b = appendInt(b, e.DecididaEm.UnixMicro())
	b = appendTexto(b, e.ChaveID)
	b = append(b, e.MACAnterior[:]...)
	return b
}

func appendInt(b []byte, v int64) []byte { return binary.BigEndian.AppendUint64(b, uint64(v)) } //nolint:gosec // complemento de dois, de propósito

func appendTexto(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s))) //nolint:gosec // textos daqui têm poucas dezenas de bytes
	return append(b, s...)
}

// Assinar preenche ChaveID (a chave atual) e MAC. MACAnterior, ContaID e Seq
// já devem estar definidos: o MAC liga a entrada à posição que ela ocupa.
func (c *Chaveiro) Assinar(e *Entrada) error {
	e.ChaveID = c.atual
	if err := e.Validar(); err != nil {
		return err
	}
	m, err := c.mac(e.ChaveID, e.Codificar())
	if err != nil {
		return err
	}
	copy(e.MAC[:], m)
	return nil
}

// Conferir recalcula o MAC com a chave registrada na própria entrada.
func (c *Chaveiro) Conferir(e Entrada) error {
	m, err := c.mac(e.ChaveID, e.Codificar())
	if err != nil {
		return err
	}
	if !hmac.Equal(m, e.MAC[:]) {
		return ErrMACInvalido
	}
	return nil
}
