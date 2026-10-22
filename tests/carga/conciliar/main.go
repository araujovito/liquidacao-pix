// Programa conciliar roda a conciliação do livro-razão e imprime o resultado
// numa linha; sai com código 1 se houver divergência. Usado pelos scripts de
// carga para confirmar que a carga não quebrou nenhuma invariante.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/araujovito/liquidacao-pix/internal/repository"
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

	divs, err := repository.New(pool).Conciliar(ctx)
	if err != nil {
		fmt.Println("erro:", err)
		os.Exit(1)
	}
	if len(divs) == 0 {
		fmt.Println("fechada, sem divergências.")
		return
	}
	fmt.Printf("%d divergências (primeira: %s %s)\n", len(divs), divs[0].Tipo, divs[0].Detalhe)
	os.Exit(1)
}
