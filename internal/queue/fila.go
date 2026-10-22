// Package queue implementa a fila sobre Redis Streams.
//
// Streams (e não Pub/Sub) porque a mensagem fica guardada até o consumidor
// confirmar: worker fora do ar não perde nada. Cada mensagem lida e não
// confirmada fica na lista de pendentes do grupo (PEL) e pode ser
// reivindicada por outro worker.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const campoID = "transferencia_id"

// Mensagem é uma entrega do stream.
type Mensagem struct {
	ID              string // id da entrada no stream
	TransferenciaID string
	Entregas        int64 // quantas vezes já foi entregue (1 na primeira leitura)
}

// Fila guarda os nomes das chaves. Prefixo permite isolar ambientes (e
// testes) no mesmo Redis.
type Fila struct {
	rdb    *redis.Client
	stream string
	mortas string
	erros  string
	grupo  string
}

// New cria a fila com as chaves <prefixo>transferencias, …:mortas, …:erros.
func New(rdb *redis.Client, prefixo string) *Fila {
	base := prefixo + "transferencias"
	return &Fila{rdb: rdb, stream: base, mortas: base + ":mortas", erros: base + ":erros", grupo: "liquidadores"}
}

// Publicar adiciona uma mensagem por transferência, num único envio.
//
// Sem MAXLEN de propósito: aparar o stream pode apagar entradas ainda não
// confirmadas. O tamanho é controlado apagando cada mensagem ao confirmar.
//
// Devolve o id de cada entrada criada, na ordem dos ids recebidos.
func (f *Fila) Publicar(ctx context.Context, ids []string) ([]string, error) {
	cmds := make([]*redis.StringCmd, len(ids))
	_, err := f.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, id := range ids {
			cmds[i] = p.XAdd(ctx, &redis.XAddArgs{Stream: f.stream, Values: map[string]any{campoID: id}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	mensagens := make([]string, len(cmds))
	for i, c := range cmds {
		mensagens[i] = c.Val()
	}
	return mensagens, nil
}

// Existentes diz quais das mensagens ainda estão no stream (esperando ou em
// processamento). Como confirmar apaga a entrada, ausência significa
// "processada" ou "perdida".
func (f *Fila) Existentes(ctx context.Context, mensagens []string) (map[string]bool, error) {
	cmds := make([]*redis.XMessageSliceCmd, len(mensagens))
	_, err := f.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, m := range mensagens {
			cmds[i] = p.XRange(ctx, f.stream, m, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	existem := make(map[string]bool, len(mensagens))
	for i, c := range cmds {
		if len(c.Val()) > 0 {
			existem[mensagens[i]] = true
		}
	}
	return existem, nil
}

// GarantirGrupo cria o grupo de consumo se não existir. Começa do início do
// stream ("0") para não pular mensagens publicadas antes do primeiro worker.
func (f *Fila) GarantirGrupo(ctx context.Context) error {
	err := f.rdb.XGroupCreateMkStream(ctx, f.stream, f.grupo, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// SemGrupo diz se o erro é NOGROUP: o stream ou o grupo deixou de existir
// (Redis reiniciado sem dados, stream apagado). O grupo morre junto com o
// stream, e o XADD do relay recria o stream sem grupo.
func SemGrupo(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "NOGROUP")
}

// Ler entrega até n mensagens novas para o consumidor, esperando até
// bloquear por novas. Sem mensagens no prazo, devolve lista vazia.
func (f *Fila) Ler(ctx context.Context, consumidor string, n int64, bloquear time.Duration) ([]Mensagem, error) {
	res, err := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: f.grupo, Consumer: consumidor, Streams: []string{f.stream, ">"},
		Count: n, Block: bloquear,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var msgs []Mensagem
	for _, s := range res {
		for _, m := range s.Messages {
			msgs = append(msgs, paraMensagem(m, 1))
		}
	}
	return msgs, nil
}

func paraMensagem(m redis.XMessage, entregas int64) Mensagem {
	id, _ := m.Values[campoID].(string)
	return Mensagem{ID: m.ID, TransferenciaID: id, Entregas: entregas}
}

// Confirmar tira a mensagem da lista de pendentes e do stream, de forma
// atômica (MULTI). Apagar mantém o stream pequeno sem precisar de MAXLEN.
func (f *Fila) Confirmar(ctx context.Context, m Mensagem) error {
	_, err := f.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAck(ctx, f.stream, f.grupo, m.ID)
		p.XDel(ctx, f.stream, m.ID)
		p.HDel(ctx, f.erros, m.ID)
		return nil
	})
	return err
}

// AnotarErro guarda o último erro da mensagem, para ir junto se ela acabar
// nas mortas.
func (f *Fila) AnotarErro(ctx context.Context, m Mensagem, erro error) error {
	return f.rdb.HSet(ctx, f.erros, m.ID, erro.Error()).Err()
}

// Reivindicar procura mensagens pendentes de qualquer consumidor (inclusive
// de workers mortos) e decide o destino de cada uma:
//
//   - parada há pelo menos base·2^(entregas-1): reivindicada para o consumidor
//     tentar de novo (espera crescente: 1×, 2×, 4×, … a base);
//   - já entregue maxEntregas vezes: vai para o stream de mortas.
//
// Devolve as reivindicadas; as mortas já saem tratadas.
func (f *Fila) Reivindicar(ctx context.Context, consumidor string, base time.Duration, maxEntregas int64) ([]Mensagem, int, error) {
	pend, err := f.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: f.stream, Group: f.grupo, Start: "-", End: "+", Count: 100, Idle: base,
	}).Result()
	if err != nil {
		return nil, 0, err
	}

	var retomar []Mensagem
	mortas := 0
	for _, p := range pend {
		if p.Idle < base<<min(p.RetryCount-1, 20) {
			continue // ainda dentro da espera desta tentativa
		}
		if p.RetryCount >= maxEntregas {
			if err := f.matar(ctx, p); err != nil {
				return retomar, mortas, err
			}
			mortas++
			continue
		}
		// XCLAIM com min-idle: se outro worker reivindicou no meio tempo, o
		// idle zerou e esta chamada não devolve nada. Sem corrida.
		msgs, err := f.rdb.XClaim(ctx, &redis.XClaimArgs{
			Stream: f.stream, Group: f.grupo, Consumer: consumidor,
			MinIdle: p.Idle, Messages: []string{p.ID},
		}).Result()
		if err != nil {
			return retomar, mortas, err
		}
		for _, m := range msgs {
			retomar = append(retomar, paraMensagem(m, p.RetryCount+1))
		}
	}
	return retomar, mortas, nil
}

func (f *Fila) matar(ctx context.Context, p redis.XPendingExt) error {
	msgs, err := f.rdb.XRange(ctx, f.stream, p.ID, p.ID).Result()
	if err != nil {
		return err
	}
	transferencia := ""
	if len(msgs) == 1 {
		transferencia = paraMensagem(msgs[0], 0).TransferenciaID
	}
	ultimoErro, err := f.rdb.HGet(ctx, f.erros, p.ID).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	_, err = f.rdb.TxPipelined(ctx, func(pp redis.Pipeliner) error {
		pp.XAdd(ctx, &redis.XAddArgs{Stream: f.mortas, Values: map[string]any{
			campoID:       transferencia,
			"mensagem":    p.ID,
			"entregas":    p.RetryCount,
			"ultimo_erro": ultimoErro,
		}})
		pp.XAck(ctx, f.stream, f.grupo, p.ID)
		pp.XDel(ctx, f.stream, p.ID)
		pp.HDel(ctx, f.erros, p.ID)
		return nil
	})
	return err
}

// Morta é uma entrada do stream de mensagens mortas.
type Morta struct {
	TransferenciaID string
	Entregas        string
	UltimoErro      string
}

// Mortas lista as mensagens mortas (para o runbook e os testes).
func (f *Fila) Mortas(ctx context.Context) ([]Morta, error) {
	msgs, err := f.rdb.XRange(ctx, f.mortas, "-", "+").Result()
	if err != nil {
		return nil, err
	}
	out := make([]Morta, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Morta{
			TransferenciaID: fmt.Sprint(m.Values[campoID]),
			Entregas:        fmt.Sprint(m.Values["entregas"]),
			UltimoErro:      fmt.Sprint(m.Values["ultimo_erro"]),
		})
	}
	return out, nil
}

// Tamanho devolve quantas entradas há no stream (pendentes de confirmação
// incluídas).
func (f *Fila) Tamanho(ctx context.Context) (int64, error) {
	return f.rdb.XLen(ctx, f.stream).Result()
}

// Descartar apaga o stream inteiro. Existe para simular perda de dados do
// Redis nos testes e no script de caos; não é usado pelos programas.
func (f *Fila) Descartar(ctx context.Context) error {
	return f.rdb.Del(ctx, f.stream).Err()
}

// EstadoDaFila é o tamanho do stream, das pendentes de confirmação e das mortas.
type EstadoDaFila struct {
	Tamanho, PendentesDeConfirmacao, Mortas int64
}

// Estado lê os três números. Stream ou grupo inexistente conta como zero:
// é o estado normal antes da primeira mensagem e depois de uma perda do Redis.
func (f *Fila) Estado(ctx context.Context) (EstadoDaFila, error) {
	var e EstadoDaFila
	var err error
	if e.Tamanho, err = f.rdb.XLen(ctx, f.stream).Result(); err != nil {
		return e, err
	}
	if e.Mortas, err = f.rdb.XLen(ctx, f.mortas).Result(); err != nil {
		return e, err
	}
	p, err := f.rdb.XPending(ctx, f.stream, f.grupo).Result()
	switch {
	case SemGrupo(err):
	case err != nil:
		return e, err
	default:
		e.PendentesDeConfirmacao = p.Count
	}
	return e, nil
}
