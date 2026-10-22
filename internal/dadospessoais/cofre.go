package dadospessoais

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/araujovito/liquidacao-pix/internal/segredos"
)

// Domínios de separação: entram como dado autenticado, então um texto cifrado
// de um tipo nunca decifra como o outro, mesmo com a mesma chave.
const (
	DominioCPF   = "pix-cpf/v1"
	DominioChave = "pix-chave-cpf/v1"
)

var (
	// ErrDecifragem cobre qualquer falha de autenticação: dado alterado, copiado
	// para outra conta, ou chave errada. Não diz qual, de propósito.
	ErrDecifragem = errors.New("dadospessoais: não foi possível decifrar")
	// ErrMestraDesconhecida indica uma chave de dados embrulhada por uma
	// chave-mestra que o chaveiro não tem.
	ErrMestraDesconhecida = errors.New("dadospessoais: chave-mestra desconhecida")
)

// Sigilo é tudo que vai ao banco para um CPF: o texto cifrado e a chave de
// dados da conta, embrulhada. Nada aqui é legível sem a chave-mestra.
type Sigilo struct {
	CPFCifrado      []byte // nonce(12) || texto cifrado || tag(16)
	MestraID        string
	NonceEmbrulho   []byte // 12 bytes
	ChaveEmbrulhada []byte // chave de dados (32) cifrada com a mestra, mais a tag(16)
}

// Mestras guarda as chaves-mestras por identificador; uma é a atual e embrulha
// as chaves de dados novas, as demais só desembrulham as antigas.
type Mestras struct {
	atual  string
	chaves map[string][]byte
	rand   io.Reader // crypto/rand; os testes injetam uma fonte que falha
}

// NovasMestras monta o chaveiro de mestras.
func NovasMestras(atual string, chaves map[string][]byte) (*Mestras, error) {
	if err := segredos.Validar("dadospessoais", atual, chaves); err != nil {
		return nil, err
	}
	copia := make(map[string][]byte, len(chaves))
	for id, k := range chaves {
		copia[id] = append([]byte(nil), k...)
	}
	return &Mestras{atual: atual, chaves: copia, rand: rand.Reader}, nil
}

// MestrasDoAmbiente lê CPF_CHAVES_MESTRAS ("v1:<hex>,v2:<hex>") e
// CPF_CHAVE_MESTRA_ATUAL. Sem valor padrão.
func MestrasDoAmbiente() (*Mestras, error) {
	atual, chaves, err := segredos.DoAmbiente("dadospessoais", "CPF_CHAVES_MESTRAS", "CPF_CHAVE_MESTRA_ATUAL")
	if err != nil {
		return nil, err
	}
	return NovasMestras(atual, chaves)
}

// ComFonteAleatoria devolve uma cópia que lê os bytes aleatórios de r. Só para
// teste: com uma fonte previsível a cifra deixa de ser segura.
func (m *Mestras) ComFonteAleatoria(r io.Reader) *Mestras {
	c := *m
	c.rand = r
	return &c
}

// Cifrar gera uma chave de dados nova para a conta, cifra o CPF com ela e a
// embrulha com a mestra atual. O CPF deve estar normalizado (11 dígitos).
func (m *Mestras) Cifrar(contaID int64, cpf string) (Sigilo, error) {
	if len(cpf) != 11 {
		return Sigilo{}, ErrCPFInvalido
	}
	dek := make([]byte, segredos.TamanhoChave)
	defer zerar(dek)
	if _, err := io.ReadFull(m.rand, dek); err != nil {
		return Sigilo{}, fmt.Errorf("dadospessoais: gerar chave: %w", err)
	}

	cifrado, err := m.selar(dek, aadCPF(contaID), []byte(cpf))
	if err != nil {
		return Sigilo{}, err
	}
	mestra := m.chaves[m.atual]
	embrulho, err := m.selar(mestra, aadChave(contaID, m.atual), dek)
	if err != nil {
		return Sigilo{}, err
	}
	return Sigilo{
		CPFCifrado: cifrado, MestraID: m.atual,
		NonceEmbrulho: embrulho[:nonceTam], ChaveEmbrulhada: embrulho[nonceTam:],
	}, nil
}

// Decifrar desembrulha a chave de dados da conta e decifra o CPF.
func (m *Mestras) Decifrar(contaID int64, s Sigilo) (string, error) {
	mestra, ok := m.chaves[s.MestraID]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrMestraDesconhecida, s.MestraID)
	}
	if len(s.NonceEmbrulho) != nonceTam {
		return "", ErrDecifragem
	}
	embrulho := append(append([]byte(nil), s.NonceEmbrulho...), s.ChaveEmbrulhada...)
	dek, err := abrir(mestra, aadChave(contaID, s.MestraID), embrulho)
	if err != nil {
		return "", ErrDecifragem
	}
	defer zerar(dek)
	if len(dek) != segredos.TamanhoChave {
		return "", ErrDecifragem
	}
	cpf, err := abrir(dek, aadCPF(contaID), s.CPFCifrado)
	if err != nil {
		return "", ErrDecifragem
	}
	return string(cpf), nil
}

const nonceTam = 12

// selar cifra com AES-256-GCM e devolve nonce || texto cifrado || tag. O nonce
// é aleatório de 96 bits; com uma chave por conta e uma cifragem por chave, a
// margem do limite de colisão do GCM é enorme.
func (m *Mestras) selar(chave, aad, texto []byte) ([]byte, error) {
	gcm, err := novoGCM(chave)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceTam)
	if _, err := io.ReadFull(m.rand, nonce); err != nil {
		return nil, fmt.Errorf("dadospessoais: gerar nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, texto, aad), nil
}

func abrir(chave, aad, selado []byte) ([]byte, error) {
	gcm, err := novoGCM(chave)
	if err != nil {
		return nil, err
	}
	if len(selado) < nonceTam+gcm.Overhead() {
		return nil, ErrDecifragem
	}
	return gcm.Open(nil, selado[:nonceTam], selado[nonceTam:], aad)
}

func novoGCM(chave []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(chave)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// aadCPF: domínio e conta. Um CPF cifrado copiado para outra conta não decifra.
func aadCPF(contaID int64) []byte {
	return appendInt(appendTexto(nil, DominioCPF), contaID)
}

// aadChave: domínio, conta e identificador da mestra. Trocar chaves embrulhadas
// entre contas, ou trocar o id da mestra, falha na autenticação.
func aadChave(contaID int64, mestraID string) []byte {
	return appendTexto(appendInt(appendTexto(nil, DominioChave), contaID), mestraID)
}

func appendInt(b []byte, v int64) []byte { return binary.BigEndian.AppendUint64(b, uint64(v)) } //nolint:gosec // complemento de dois, de propósito

func appendTexto(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s))) //nolint:gosec // textos daqui têm poucas dezenas de bytes
	return append(b, s...)
}

func zerar(b []byte) { clear(b) }
