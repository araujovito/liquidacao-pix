// Programa conciliar roda a conciliação do livro-razão e imprime o resultado
// e a verificação da cadeia de auditoria numa linha; sai com código 1 se houver
// divergência ou adulteração. Usado pelos scripts de
// carga para confirmar que a carga não quebrou nenhuma invariante.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

func main() {
	ctx := context.Background()
	url, err := repository.URLDoAmbiente()
	if err != nil {
		fmt.Println("erro:", err)
		os.Exit(1)
	}
	pool, err := repository.Conectar(ctx, url)
	if err != nil {
		fmt.Println("erro:", err)
		os.Exit(1)
	}
	defer pool.Close()

	repo := repository.New(pool)
	divs, err := repo.Conciliar(ctx)
	if err != nil {
		fmt.Println("erro:", err)
		os.Exit(1)
	}
	if len(divs) > 0 {
		fmt.Printf("%d divergências (primeira: %s %s)\n", len(divs), divs[0].Tipo, divs[0].Detalhe)
		os.Exit(1)
	}
	chaveiro, err := auditoria.ChaveiroDoAmbiente()
	if err != nil {
		fmt.Println("erro:", err)
		os.Exit(1)
	}
	r, err := usecase.VerificarAuditoria(ctx, repo, chaveiro)
	if err != nil {
		fmt.Println("erro:", err)
		os.Exit(1)
	}
	if len(r.Problemas) > 0 {
		fmt.Printf("auditoria: %d problemas (primeiro: %s)\n", len(r.Problemas), r.Problemas[0])
		os.Exit(1)
	}
	fmt.Printf("fechada, sem divergências; auditoria íntegra (%d entradas em %d contas).\n", r.Entradas, r.Contas)
}
