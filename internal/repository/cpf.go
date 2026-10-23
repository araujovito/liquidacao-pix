package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/araujovito/liquidacao-pix/internal/dadospessoais"
)

var (
	ErrSemCPF           = errors.New("conta sem CPF cadastrado")
	ErrCPFApagado       = errors.New("CPF apagado (chave descartada)")
	ErrCPFJaCadastrado  = errors.New("conta já tem CPF (cadastrado ou apagado)")
	ErrContaSemTitular  = errors.New("conta técnica não tem titular")
	ErrCPFInconsistente = errors.New("CPF cifrado sem chave e sem registro de apagamento")
)

// CadastrarCPF cifra o CPF com uma chave de dados nova e grava os dois, na
// mesma transação. O CPF em claro não chega ao banco nem fica em nenhuma
// variável depois de a função voltar. Recusa sobrescrever: um CPF cadastrado
// (ou já apagado) não é trocado por esta via.
func (r *Repositorio) CadastrarCPF(ctx context.Context, m *dadospessoais.Mestras, chavePix, cpf string) error {
	cpf, err := dadospessoais.NormalizarCPF(cpf)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		id, cifrado, err := contaDoTitular(ctx, tx, chavePix)
		if err != nil {
			return err
		}
		if cifrado {
			return ErrCPFJaCadastrado
		}
		s, err := m.Cifrar(id, cpf)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chaves_cpf (conta_id, mestra_id, nonce_embrulho, chave_embrulhada)
			VALUES ($1, $2, $3, $4)`, id, s.MestraID, s.NonceEmbrulho, s.ChaveEmbrulhada); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE contas SET cpf_cifrado = $2 WHERE id = $1`, id, s.CPFCifrado)
		return err
	})
}

// CPFDaConta lê e decifra o CPF. Devolve ErrCPFApagado depois do apagamento,
// distinto de uma falha de decifragem: sem a chave não há o que tentar.
func (r *Repositorio) CPFDaConta(ctx context.Context, m *dadospessoais.Mestras, chavePix string) (string, error) {
	var (
		id       int64
		tecnica  bool
		cifrado  []byte
		apagado  bool
		mestraID *string
		nonce    []byte
		chave    []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT c.id, c.tecnica, c.cpf_cifrado, c.cpf_apagado_em IS NOT NULL,
		       k.mestra_id, k.nonce_embrulho, k.chave_embrulhada
		  FROM contas c LEFT JOIN chaves_cpf k ON k.conta_id = c.id
		 WHERE c.chave_pix = $1`, chavePix).Scan(&id, &tecnica, &cifrado, &apagado, &mestraID, &nonce, &chave)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrNaoEncontrada
	case err != nil:
		return "", err
	case tecnica:
		return "", ErrContaSemTitular
	case cifrado == nil:
		return "", ErrSemCPF
	case apagado:
		return "", ErrCPFApagado
	case mestraID == nil:
		return "", ErrCPFInconsistente
	}
	return m.Decifrar(id, dadospessoais.Sigilo{
		CPFCifrado: cifrado, MestraID: *mestraID, NonceEmbrulho: nonce, ChaveEmbrulhada: chave})
}

// ApagarCPF descarta a chave de dados da conta (crypto-shredding). O texto
// cifrado permanece no banco, ilegível. É idempotente: apagar de novo não
// muda nada e não é erro.
func (r *Repositorio) ApagarCPF(ctx context.Context, chavePix string) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		id, cifrado, err := contaDoTitular(ctx, tx, chavePix)
		if err != nil {
			return err
		}
		if !cifrado {
			return ErrSemCPF
		}
		if _, err := tx.Exec(ctx, `DELETE FROM chaves_cpf WHERE conta_id = $1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE contas SET cpf_apagado_em = coalesce(cpf_apagado_em, now()) WHERE id = $1`, id)
		return err
	})
}

// contaDoTitular trava a conta de um cliente e diz se ela já tem CPF cifrado.
func contaDoTitular(ctx context.Context, tx pgx.Tx, chavePix string) (id int64, cifrado bool, err error) {
	var tecnica bool
	err = tx.QueryRow(ctx,
		`SELECT id, tecnica, cpf_cifrado IS NOT NULL FROM contas WHERE chave_pix = $1 FOR UPDATE`, chavePix,
	).Scan(&id, &tecnica, &cifrado)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, fmt.Errorf("conta %q: %w", chavePix, ErrNaoEncontrada)
	case err != nil:
		return 0, false, err
	case tecnica:
		return 0, false, ErrContaSemTitular
	}
	return id, cifrado, nil
}
