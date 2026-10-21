// Comando semear cria contas de exemplo com saldo, para demonstração e teste
// de carga. As chaves são cliente-0001@pix, cliente-0002@pix, ...
//
// Uso: semear -contas 100 -saldo 1000000
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/araujovito/liquidacao-pix/internal/domain"
	"github.com/araujovito/liquidacao-pix/internal/repository"
	"github.com/araujovito/liquidacao-pix/internal/usecase"
)

func main() {
	contas := flag.Int("contas", 100, "quantas contas criar")
	saldo := flag.Int64("saldo", 1_000_000, "saldo inicial de cada conta, em centavos")
	flag.Parse()

	if err := semear(*contas, domain.Centavos(*saldo)); err != nil {
		slog.Error("semear", "erro", err)
		os.Exit(1)
	}
}

func semear(n int, saldo domain.Centavos) error {
	ctx := context.Background()
	url, err := repository.URLDoAmbiente()
	if err != nil {
		return err
	}
	pool, err := repository.Conectar(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	repo := repository.New(pool)
	liq := usecase.Liquidador{Repo: repo}

	// ISPB fictício 99999999, data e hora atuais e, nos 11 caracteres livres,
	// o número da conta + 7 dígitos base 36 do relógio em nanossegundos: rodar
	// de novo, mesmo no mesmo minuto, não colide com a rodada anterior.
	agora := time.Now().UTC()
	carimbo := agora.Format("200601021504")
	rodada := strconv.FormatInt(agora.UnixNano()%78_364_164_096, 36) // 36^7
	rodada = strings.Repeat("0", 7-len(rodada)) + rodada
	criadas := 0
	for i := 1; i <= n; i++ {
		chave := fmt.Sprintf("cliente-%04d@pix", i)
		id, err := repo.IDDeContaCliente(ctx, chave)
		if err != nil {
			if id, err = repo.CriarConta(ctx, chave, fmt.Sprintf("Cliente %04d", i)); err != nil {
				return err
			}
			criadas++
		}
		e2e := fmt.Sprintf("E99999999%s%04d%s", carimbo, i, rodada)
		if _, err := liq.Aportar(ctx, id, saldo, e2e); err != nil {
			return fmt.Errorf("aporte em %s: %w", chave, err)
		}
	}
	slog.Info("contas semeadas", "novas", criadas, "aportes", n, "valor_cada", saldo.String())
	return nil
}
