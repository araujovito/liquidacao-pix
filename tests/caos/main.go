// Programa caos: teste de resiliência ponta a ponta contra o sistema rodando
// em contêineres (make sistema).
//
// Gera carga contínua na API e, no meio dela, mata com SIGKILL o Redis, o
// worker, o Postgres, o relay e a API, um de cada vez, religando cada um
// depois. Por fim apaga o stream inteiro do Redis, simulando perda total de
// dados da fila: só a varredura de órfãs do relay recupera essas mensagens. O cliente se comporta como um cliente correto de API idempotente:
// em erro de rede ou 5xx, reenvia o MESMO pedido (mesmo end_to_end_id).
//
// No fim, verifica:
//   - toda transferência aceita (202) existe e chegou a status final;
//   - nenhuma ficou PENDENTE;
//   - a conciliação fecha (nenhuma liquidada duas vezes, soma global zero);
//   - o dinheiro dos clientes é o mesmo do início.
//
// Uso: make sistema && make semear && go run ./tests/caos
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/araujovito/liquidacao-pix/internal/repository"
)

var (
	api          = flag.String("api", "http://localhost:8080", "endereço da API")
	duracao      = flag.Duration("duracao", 45*time.Second, "tempo de carga")
	concorrencia = flag.Int("concorrencia", 16, "clientes simultâneos")
	contas       = flag.Int("contas", 100, "contas semeadas (cliente-0001@pix …)")
	prazo        = flag.Duration("prazo", 4*time.Minute, "prazo para tudo ficar final depois da carga")
	compose      = []string{"compose", "-f", "deployments/docker-compose.yml", "--profile", "sistema"}
)

// Roteiro de falhas, em segundos desde o início da carga.
var falhas = []struct {
	servico        string
	matar, religar time.Duration
}{
	{"redis", 4 * time.Second, 9 * time.Second},
	{"worker", 12 * time.Second, 16 * time.Second},
	{"postgres", 19 * time.Second, 25 * time.Second},
	{"relay", 28 * time.Second, 32 * time.Second},
	{"api", 35 * time.Second, 38 * time.Second},
}

// Momento em que o stream é apagado (perda total de dados do Redis).
const apagarStream = 41 * time.Second

func main() {
	flag.Parse()
	if err := rodar(); err != nil {
		fmt.Fprintln(os.Stderr, "FALHOU:", err)
		os.Exit(1)
	}
}

func rodar() error {
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

	antes, err := dinheiroDosClientes(ctx, pool)
	if err != nil {
		return err
	}
	logf("dinheiro dos clientes antes: %d centavos", antes)

	inicio := time.Now()
	go caos(inicio)
	aceitas, st := carga(inicio)
	logf("carga encerrada: %d aceitas, %d retentativas do cliente, %d desistências",
		len(aceitas), st.retentativas.Load(), st.desistencias.Load())

	// A carga passou pelo Postgres morto: a conexão do verificador pode ter
	// caído. O pgxpool reconecta sozinho na próxima consulta.
	convergiu, err := esperarFinais(ctx, pool, *prazo)
	if err != nil {
		return err
	}
	logf("tudo final %s depois do fim da carga", convergiu.Round(100*time.Millisecond))

	return verificar(ctx, pool, aceitas, antes)
}

type estatisticas struct {
	retentativas, desistencias atomic.Int64
}

func carga(inicio time.Time) ([]string, *estatisticas) {
	var (
		mu      sync.Mutex
		aceitas []string
		seq     atomic.Int64
		st      estatisticas
		wg      sync.WaitGroup
	)
	// Os 11 caracteres livres do e2e: 3 da rodada + 8 de sequência.
	rodada := fmt.Sprintf("%03d", time.Now().Unix()%1000)
	carimbo := time.Now().UTC().Format("200601021504")
	cliente := &http.Client{Timeout: 5 * time.Second}

	for range *concorrencia {
		wg.Go(func() {
			rnd := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)) //nolint:gosec // carga, não segurança
			for time.Since(inicio) < *duracao {
				de := 1 + rnd.IntN(*contas)
				para := 1 + (de+rnd.IntN(*contas-1))%*contas
				corpo, _ := json.Marshal(map[string]any{
					"end_to_end_id":   fmt.Sprintf("E12345678%s%s%08d", carimbo, rodada, seq.Add(1)),
					"chave_pagador":   fmt.Sprintf("cliente-%04d@pix", de),
					"chave_recebedor": fmt.Sprintf("cliente-%04d@pix", para),
					"valor_centavos":  1 + rnd.IntN(5_000),
				})
				if id, ok := enviar(cliente, corpo, &st); ok {
					mu.Lock()
					aceitas = append(aceitas, id)
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	return aceitas, &st
}

// enviar reenvia o mesmo corpo até receber uma resposta definitiva, por até
// 60 s. É o comportamento esperado de um cliente de API idempotente.
func enviar(c *http.Client, corpo []byte, st *estatisticas) (string, bool) {
	espera := 50 * time.Millisecond
	limite := time.Now().Add(60 * time.Second)
	for tentativa := 0; time.Now().Before(limite); tentativa++ {
		if tentativa > 0 {
			st.retentativas.Add(1)
			time.Sleep(espera)
			espera = min(espera*2, 2*time.Second)
		}
		res, err := c.Post(*api+"/transferencias", "application/json", bytes.NewReader(corpo))
		if err != nil {
			continue
		}
		var r struct{ ID string }
		_ = json.NewDecoder(res.Body).Decode(&r)
		_ = res.Body.Close()
		switch {
		case res.StatusCode == http.StatusAccepted:
			return r.ID, true
		case res.StatusCode >= 500:
			continue
		default:
			logf("resposta definitiva inesperada: %d", res.StatusCode)
			return "", false
		}
	}
	st.desistencias.Add(1)
	return "", false
}

func caos(inicio time.Time) {
	for _, f := range falhas {
		time.Sleep(time.Until(inicio.Add(f.matar)))
		logf("SIGKILL em %s", f.servico)
		docker("kill", "-s", "SIGKILL", f.servico)
		time.Sleep(time.Until(inicio.Add(f.religar)))
		logf("religando %s", f.servico)
		docker("start", f.servico)
	}
	time.Sleep(time.Until(inicio.Add(apagarStream)))
	logf("apagando o stream do Redis (perda total da fila)")
	docker("exec", "-T", "redis", "redis-cli", "DEL", "transferencias")
}

func docker(args ...string) {
	cmd := exec.Command("docker", append(compose, args...)...) //nolint:gosec // argumentos fixos do roteiro
	if out, err := cmd.CombinedOutput(); err != nil {
		logf("docker %v: %v\n%s", args, err, out)
	}
}

func esperarFinais(ctx context.Context, pool *pgxpool.Pool, prazo time.Duration) (time.Duration, error) {
	inicio := time.Now()
	ultimo := -1
	for time.Since(inicio) < prazo {
		var pendentes int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM transferencias WHERE status = 'PENDENTE'`).Scan(&pendentes)
		if err == nil && pendentes == 0 {
			return time.Since(inicio), nil
		}
		if err == nil && pendentes != ultimo {
			logf("%d pendentes", pendentes)
			ultimo = pendentes
		}
		time.Sleep(time.Second)
	}
	return 0, fmt.Errorf("ainda havia pendentes depois de %s", prazo)
}

func verificar(ctx context.Context, pool *pgxpool.Pool, aceitas []string, antes int64) error {
	var falhou bool
	erro := func(formato string, args ...any) { falhou = true; logf("ERRO: "+formato, args...) }

	var encontradas, finais int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status <> 'PENDENTE')
		  FROM transferencias WHERE id = ANY($1::uuid[])`, aceitas,
	).Scan(&encontradas, &finais); err != nil {
		return err
	}
	if encontradas != len(aceitas) {
		erro("%d aceitas sumiram", len(aceitas)-encontradas)
	}
	if finais != len(aceitas) {
		erro("%d aceitas não estão em status final", len(aceitas)-finais)
	}

	divs, err := repository.New(pool).Conciliar(ctx)
	if err != nil {
		return err
	}
	for _, d := range divs {
		erro("conciliação: %s %s", d.Tipo, d.Detalhe)
	}

	depois, err := dinheiroDosClientes(ctx, pool)
	if err != nil {
		return err
	}
	if depois != antes {
		erro("dinheiro dos clientes mudou: %d → %d", antes, depois)
	}

	var liquidadas, rejeitadas, republicadas int
	_ = pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'LIQUIDADA'), count(*) FILTER (WHERE status = 'REJEITADA')
		  FROM transferencias WHERE id = ANY($1::uuid[])`, aceitas).Scan(&liquidadas, &rejeitadas)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE republicacoes > 0 AND transferencia_id = ANY($1::uuid[])`, aceitas).Scan(&republicadas)
	logf("resultado: %d liquidadas, %d rejeitadas por saldo, %d recuperadas pela varredura de órfãs", liquidadas, rejeitadas, republicadas)

	if falhou {
		return fmt.Errorf("verificação encontrou problemas")
	}
	logf("OK: nada perdido, nada duplicado, conciliação fechada, dinheiro conservado")
	return nil
}

func dinheiroDosClientes(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var total int64
	err := pool.QueryRow(ctx, `SELECT coalesce(sum(saldo_centavos), 0) FROM contas WHERE NOT tecnica`).Scan(&total)
	return total, err
}

var inicioLog = time.Now()

func logf(formato string, args ...any) {
	fmt.Printf("[%6.1fs] %s\n", time.Since(inicioLog).Seconds(), fmt.Sprintf(formato, args...))
}
