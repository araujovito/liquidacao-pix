// Comando titular cuida do CPF do titular de uma conta (ADR 0006).
//
//	echo 529.982.247-25 | titular cadastrar-cpf -chave cliente-0001@pix
//	titular mostrar-cpf -chave cliente-0001@pix      # mascarado: ***.982.247-**
//	titular apagar-cpf  -chave cliente-0001@pix      # descarta a chave; irreversível
//
// O CPF é lido da entrada padrão, não de um argumento: argumentos ficam no
// histórico do shell e na lista de processos. A saída nunca mostra o CPF
// inteiro.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/araujovito/liquidacao-pix/internal/dadospessoais"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

func main() { os.Exit(executar(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func executar(args []string, in io.Reader, out, errOut io.Writer) int {
	falha := func(f string, a ...any) int {
		fmt.Fprintf(errOut, "titular: "+f+"\n", a...)
		return 1
	}
	if len(args) == 0 {
		return falha("uso: titular cadastrar-cpf|mostrar-cpf|apagar-cpf -chave <chave pix>")
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errOut)
	chave := fs.String("chave", "", "chave PIX da conta")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *chave == "" {
		return falha("-chave é obrigatória")
	}

	ctx := context.Background()
	url, err := repository.URLDoAmbiente()
	if err != nil {
		return falha("%v", err)
	}
	pool, err := repository.Conectar(ctx, url)
	if err != nil {
		return falha("%v", err)
	}
	defer pool.Close()
	repo := repository.New(pool)

	switch cmd {
	case "cadastrar-cpf":
		m, err := dadospessoais.MestrasDoAmbiente()
		if err != nil {
			return falha("%v", err)
		}
		linha, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return falha("ler o CPF: %v", err)
		}
		if err := repo.CadastrarCPF(ctx, m, *chave, strings.TrimSpace(linha)); err != nil {
			return falha("%v", err)
		}
		fmt.Fprintf(out, "CPF cadastrado (cifrado) para %s\n", *chave)
	case "mostrar-cpf":
		m, err := dadospessoais.MestrasDoAmbiente()
		if err != nil {
			return falha("%v", err)
		}
		cpf, err := repo.CPFDaConta(ctx, m, *chave)
		if err != nil {
			return falha("%v", err)
		}
		fmt.Fprintf(out, "%s: %s\n", *chave, dadospessoais.Mascarar(cpf))
	case "apagar-cpf":
		if err := repo.ApagarCPF(ctx, *chave); err != nil {
			return falha("%v", err)
		}
		fmt.Fprintf(out, "CPF de %s apagado: a chave foi descartada\n", *chave)
	default:
		return falha("comando desconhecido %q", cmd)
	}
	return 0
}
