// Package domain guarda as entidades e as regras que não dependem de banco,
// fila nem HTTP. Tudo aqui é testável sem Docker.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Centavos representa dinheiro. Inteiro de 64 bits: sem ponto flutuante,
// sem erro de arredondamento, e cabe em bigint no Postgres.
type Centavos int64

func (c Centavos) String() string {
	sinal := ""
	if c < 0 {
		sinal, c = "-", -c
	}
	return fmt.Sprintf("%sR$ %d,%02d", sinal, c/100, c%100)
}

// Status de uma transferência. Só PENDENTE pode mudar; os outros são finais.
type Status string

const (
	Pendente  Status = "PENDENTE"
	Liquidada Status = "LIQUIDADA"
	Rejeitada Status = "REJEITADA"
)

func (s Status) Final() bool { return s == Liquidada || s == Rejeitada }

// Motivos de rejeição gravados na transferência.
const (
	MotivoSaldoInsuficiente = "SALDO_INSUFICIENTE"
)

// ValorMaximo limita cada transferência a R$ 1 milhão, para que a soma de
// muitos valores nunca chegue perto do limite de int64.
const ValorMaximo Centavos = 100_000_000

// Transferencia é o pedido de mover dinheiro de uma conta para outra.
type Transferencia struct {
	ID             string
	EndToEndID     string
	PagadorID      int64
	RecebedorID    int64
	Valor          Centavos
	Status         Status
	MotivoRejeicao string
	CriadaEm       time.Time
	FinalizadaEm   *time.Time
}

// Formato do identificador de ponta a ponta do PIX (manual do Bacen):
// "E" + ISPB do participante (8 dígitos) + data e hora AAAAMMDDHHMM +
// 11 caracteres alfanuméricos. Total de 32 caracteres.
var formatoEndToEnd = regexp.MustCompile(`^E[0-9]{8}[0-9]{12}[A-Za-z0-9]{11}$`)

var (
	ErrEndToEndInvalido = errors.New("end_to_end_id fora do formato do PIX")
	ErrValorInvalido    = errors.New("valor deve ser positivo e no máximo R$ 1.000.000,00")
	ErrMesmaConta       = errors.New("pagador e recebedor são a mesma conta")
)

// Validar confere as regras que não dependem do banco.
func (t Transferencia) Validar() error {
	var errs []error
	if !formatoEndToEnd.MatchString(t.EndToEndID) {
		errs = append(errs, ErrEndToEndInvalido)
	}
	if t.Valor <= 0 || t.Valor > ValorMaximo {
		errs = append(errs, ErrValorInvalido)
	}
	if t.PagadorID == t.RecebedorID {
		errs = append(errs, ErrMesmaConta)
	}
	return errors.Join(errs...)
}

// Lancamento é uma linha do livro-razão. Negativo debita, positivo credita.
type Lancamento struct {
	TransferenciaID string
	ContaID         int64
	Valor           Centavos
}

// Decisao é o resultado de tentar liquidar uma transferência pendente.
type Decisao struct {
	Status      Status
	Motivo      string
	Lancamentos []Lancamento
}

// Decidir aplica a regra de liquidação. Recebe o saldo do pagador já lido sob
// trava; não faz I/O, por isso pode ser testada exaustivamente.
//
// pagadorTecnico indica a conta de liquidez, que pode ficar negativa.
func Decidir(t Transferencia, saldoPagador Centavos, pagadorTecnico bool) Decisao {
	if !pagadorTecnico && saldoPagador < t.Valor {
		return Decisao{Status: Rejeitada, Motivo: MotivoSaldoInsuficiente}
	}
	return Decisao{
		Status: Liquidada,
		Lancamentos: []Lancamento{
			{TransferenciaID: t.ID, ContaID: t.PagadorID, Valor: -t.Valor},
			{TransferenciaID: t.ID, ContaID: t.RecebedorID, Valor: t.Valor},
		},
	}
}
