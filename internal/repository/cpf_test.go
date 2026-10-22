package repository_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/araujovito/liquidacao-pix/internal/dadospessoais"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/testinfra"
)

const cpfDeTeste = "52998224725"

func mestrasDeTeste(t *testing.T, id string, b byte) *dadospessoais.Mestras {
	t.Helper()
	m, err := dadospessoais.NovasMestras(id, map[string][]byte{id: bytes.Repeat([]byte{b}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ondeApareceOTexto devolve as tabelas em que alguma linha, vista como texto
// (bytea sai em hexadecimal), contém algum dos padrões.
func ondeApareceOTexto(t *testing.T, pool *pgxpool.Pool, padroes ...string) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	var tabelas []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tabelas = append(tabelas, n)
	}
	rows.Close()
	var achou []string
	for _, tb := range tabelas {
		for _, p := range padroes {
			var n int
			q := fmt.Sprintf(`SELECT count(*) FROM %s x WHERE x::text LIKE '%%' || $1 || '%%'`, tb)
			if err := pool.QueryRow(ctx, q, p).Scan(&n); err != nil {
				t.Fatalf("%s: %v", tb, err)
			}
			if n > 0 {
				achou = append(achou, tb)
				break
			}
		}
	}
	return achou
}

func padroesDoCPF() []string {
	return []string{cpfDeTeste, "529.982.247-25", hex.EncodeToString([]byte(cpfDeTeste))}
}

func TestCPFIdaEVoltaSemClaroEmNenhumaTabela(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo, ctx, m := repository.New(pool), context.Background(), mestrasDeTeste(t, "m1", 1)
	if _, err := repo.CriarConta(ctx, "ana@teste", "Ana"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CadastrarCPF(ctx, m, "ana@teste", "529.982.247-25"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.CPFDaConta(ctx, m, "ana@teste")
	if err != nil || got != cpfDeTeste {
		t.Fatalf("CPFDaConta = %q, %v", got, err)
	}
	if onde := ondeApareceOTexto(t, pool, padroesDoCPF()...); len(onde) != 0 {
		t.Fatalf("o CPF em claro apareceu nas tabelas %v", onde)
	}
}

// Controle positivo: prova que a varredura enxerga um CPF em claro, para que
// "não achou" no teste acima signifique alguma coisa.
func TestVarreduraEnxergaCPFEmClaro(t *testing.T) {
	pool := testinfra.Postgres(t)
	if _, err := repository.New(pool).CriarConta(context.Background(), "x@teste", "CPF "+cpfDeTeste); err != nil {
		t.Fatal(err)
	}
	if onde := ondeApareceOTexto(t, pool, padroesDoCPF()...); len(onde) != 1 || onde[0] != "contas" {
		t.Fatalf("a varredura não achou o CPF plantado em contas: %v", onde)
	}
}

func TestCPFApagarDescartaAChaveEMantemOTextoCifrado(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo, ctx, m := repository.New(pool), context.Background(), mestrasDeTeste(t, "m1", 1)
	if _, err := repo.CriarConta(ctx, "ana@teste", "Ana"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CadastrarCPF(ctx, m, "ana@teste", cpfDeTeste); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApagarCPF(ctx, "ana@teste"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApagarCPF(ctx, "ana@teste"); err != nil {
		t.Fatalf("apagar de novo deveria ser idempotente: %v", err)
	}
	if _, err := repo.CPFDaConta(ctx, m, "ana@teste"); !errors.Is(err, repository.ErrCPFApagado) {
		t.Fatalf("erro = %v, esperado ErrCPFApagado", err)
	}
	var chaves, cifrados int
	var apagadoEm *string
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM chaves_cpf),
		       (SELECT count(*) FROM contas WHERE cpf_cifrado IS NOT NULL),
		       (SELECT cpf_apagado_em::text FROM contas WHERE chave_pix = 'ana@teste')`).Scan(&chaves, &cifrados, &apagadoEm); err != nil {
		t.Fatal(err)
	}
	if chaves != 0 || cifrados != 1 || apagadoEm == nil {
		t.Fatalf("chaves=%d cifrados=%d apagado_em=%v: a chave deveria sumir e o texto cifrado ficar", chaves, cifrados, apagadoEm)
	}
	// Mesmo com o texto cifrado e a chave-mestra, sem a chave de dados não há CPF.
	if err := repo.CadastrarCPF(ctx, m, "ana@teste", cpfDeTeste); !errors.Is(err, repository.ErrCPFJaCadastrado) {
		t.Fatalf("recadastrar depois de apagar: %v, esperado ErrCPFJaCadastrado", err)
	}
}

func TestCPFRecusas(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo, ctx, m := repository.New(pool), context.Background(), mestrasDeTeste(t, "m1", 1)
	if _, err := repo.CriarConta(ctx, "ana@teste", "Ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CriarConta(ctx, "beto@teste", "Beto"); err != nil {
		t.Fatal(err)
	}

	if err := repo.CadastrarCPF(ctx, m, "ana@teste", "529.982.247-26"); !errors.Is(err, dadospessoais.ErrCPFInvalido) {
		t.Errorf("CPF inválido: %v", err)
	}
	if err := repo.CadastrarCPF(ctx, m, "ninguem@teste", cpfDeTeste); !errors.Is(err, repository.ErrNaoEncontrada) {
		t.Errorf("conta inexistente: %v", err)
	}
	if err := repo.CadastrarCPF(ctx, m, repository.ChaveLiquidez, cpfDeTeste); !errors.Is(err, repository.ErrContaSemTitular) {
		t.Errorf("conta técnica: %v", err)
	}
	if err := repo.ApagarCPF(ctx, "ana@teste"); !errors.Is(err, repository.ErrSemCPF) {
		t.Errorf("apagar sem ter: %v", err)
	}
	if _, err := repo.CPFDaConta(ctx, m, "ana@teste"); !errors.Is(err, repository.ErrSemCPF) {
		t.Errorf("ler sem ter: %v", err)
	}
	if err := repo.CadastrarCPF(ctx, m, "ana@teste", cpfDeTeste); err != nil {
		t.Fatal(err)
	}
	if err := repo.CadastrarCPF(ctx, m, "ana@teste", "11144477735"); !errors.Is(err, repository.ErrCPFJaCadastrado) {
		t.Errorf("sobrescrever: %v", err)
	}
	if got, _ := repo.CPFDaConta(ctx, m, "ana@teste"); got != cpfDeTeste {
		t.Errorf("a tentativa de sobrescrever mudou o CPF: %q", got)
	}
	// Nada ficou pela metade depois das recusas: só a conta de Ana tem chave.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM chaves_cpf`).Scan(&n); err != nil || n != 1 {
		t.Errorf("chaves_cpf tem %d linhas (erro %v), esperado 1", n, err)
	}
}

func TestCPFMestraErradaOuAusenteNaoDecifra(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo, ctx := repository.New(pool), context.Background()
	if _, err := repo.CriarConta(ctx, "ana@teste", "Ana"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CadastrarCPF(ctx, mestrasDeTeste(t, "m1", 1), "ana@teste", cpfDeTeste); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CPFDaConta(ctx, mestrasDeTeste(t, "m1", 9), "ana@teste"); !errors.Is(err, dadospessoais.ErrDecifragem) {
		t.Errorf("mestra com outro valor: %v", err)
	}
	if _, err := repo.CPFDaConta(ctx, mestrasDeTeste(t, "outra", 1), "ana@teste"); !errors.Is(err, dadospessoais.ErrMestraDesconhecida) {
		t.Errorf("mestra com outro id: %v", err)
	}
}

// Quem copia o CPF cifrado de uma conta para outra, dentro do banco, não o lê.
func TestCPFCopiadoParaOutraContaNaoDecifra(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo, ctx, m := repository.New(pool), context.Background(), mestrasDeTeste(t, "m1", 1)
	for _, c := range []string{"ana@teste", "beto@teste"} {
		if _, err := repo.CriarConta(ctx, c, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CadastrarCPF(ctx, m, "ana@teste", cpfDeTeste); err != nil {
		t.Fatal(err)
	}
	if err := repo.CadastrarCPF(ctx, m, "beto@teste", "11144477735"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE contas SET cpf_cifrado = (SELECT cpf_cifrado FROM contas WHERE chave_pix = 'ana@teste')
	                              WHERE chave_pix = 'beto@teste'`); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.CPFDaConta(ctx, m, "beto@teste"); !errors.Is(err, dadospessoais.ErrDecifragem) {
		t.Fatalf("CPF de Ana lido na conta de Beto: %q, %v", got, err)
	}
}
