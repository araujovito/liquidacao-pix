// Comando auditar verifica a cadeia de HMAC da auditoria (ADR 0006): recalcula
// cada assinatura, confere elos, posições, a cabeça de cada conta e a
// correspondência de cada entrada com a transferência que ela registra.
//
// Uso:
//
//	auditar                               verifica e resume
//	auditar -ancora > ancora.txt          verifica e imprime as cabeças (conta seq mac)
//	auditar -conferir-ancora ancora.txt   verifica e exige que as entradas ancoradas continuem lá
//
// Códigos de saída: 0 cadeia íntegra, 1 adulteração ou inconsistência
// encontrada, 2 não foi possível verificar (banco, chave, arquivo).
//
// A âncora é o que denuncia o apagamento das últimas entradas de uma conta
// (a cadeia sozinha não sabe que havia mais). Guarde-a fora do banco.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/araujovito/liquidacao-pix/internal/auditoria"
	"github.com/araujovito/liquidacao-pix/internal/repository"
)

// maxLinhas limita quantos achados são impressos: um banco criado antes da
// fase 7 acusa uma decisão sem entrada por transferência, e dezenas de
// milhares de linhas escondem o que importa. O resumo por tipo é completo.
const maxLinhas = 50

// escrever ignora o erro de escrita: se o terminal ou o pipe fechou, não há
// outro lugar para avisar, e o código de saída é o que importa.
func escrever(w io.Writer, f string, a ...any) { _, _ = fmt.Fprintf(w, f, a...) }

func main() {
	imprimirAncora := flag.Bool("ancora", false, "imprime a cabeça de cada cadeia (conta seq mac) se a verificação passar")
	arquivoAncora := flag.String("conferir-ancora", "", "arquivo de âncoras de uma execução anterior")
	flag.Parse()
	os.Exit(executar(os.Stdout, os.Stderr, *imprimirAncora, *arquivoAncora))
}

func executar(out, errOut io.Writer, imprimirAncora bool, arquivoAncora string) int {
	falha := func(f string, a ...any) int {
		escrever(errOut, "auditar: "+f+"\n", a...)
		return 2
	}
	chaveiro, err := auditoria.ChaveiroDoAmbiente()
	if err != nil {
		return falha("%v", err)
	}
	var ancoras []auditoria.Ancora
	if arquivoAncora != "" {
		f, err := os.Open(arquivoAncora) //nolint:gosec // o caminho é argumento do operador, de propósito
		if err != nil {
			return falha("%v", err)
		}
		ancoras, err = auditoria.LerAncoras(f)
		f.Close() //nolint:errcheck,gosec // só leitura
		if err != nil {
			return falha("%v", err)
		}
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

	var contas, entradas int
	var problemas []auditoria.Problema
	var cabecas []string
	vistas := map[int64]bool{}
	err = repository.New(pool).LerAuditoria(ctx, func(c auditoria.ContaLida) error {
		contas++
		vistas[c.ID] = true
		entradas += len(c.Entradas)
		problemas = append(problemas, chaveiro.VerificarConta(c, ancoras)...)
		if c.Cabeca.Seq > 0 {
			cabecas = append(cabecas, auditoria.FormatarAncora(c.ID, c.Cabeca))
		}
		return nil
	})
	if err != nil {
		return falha("%v", err)
	}
	for _, a := range ancoras {
		if !vistas[a.ContaID] {
			problemas = append(problemas, auditoria.Problema{ContaID: a.ContaID, Seq: a.Seq,
				Tipo: auditoria.ProblemaAncora, Detalhe: "a conta ancorada não existe mais"})
		}
	}

	if len(problemas) > 0 {
		porTipo := map[string]int{}
		for i, p := range problemas {
			porTipo[p.Tipo]++
			if i < maxLinhas {
				escrever(errOut, "%s\n", p)
			}
		}
		if len(problemas) > maxLinhas {
			escrever(errOut, "... e mais %d (mostrados os %d primeiros)\n", len(problemas)-maxLinhas, maxLinhas)
		}
		for _, tipo := range slices.Sorted(maps.Keys(porTipo)) {
			escrever(errOut, "auditar: %6d do tipo %s\n", porTipo[tipo], tipo)
		}
		escrever(errOut, "auditar: %d problema(s), %d contas, %d entradas lidas\n", len(problemas), contas, entradas)
		return 1
	}
	escrever(errOut, "auditar: íntegra: %d contas, %d entradas\n", contas, entradas)
	if imprimirAncora {
		for _, l := range cabecas {
			escrever(out, "%s\n", l)
		}
	}
	return 0
}
