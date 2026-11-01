// Package secret 提供随机 ID、令牌、哈希，以及用主密钥做的加密和签名。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var b32 = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return b
}

// ID 生成带前缀的对外 ID，例如 chg_01j9x4qk7m2a。
func ID(prefix string) string { return prefix + "_" + b32.EncodeToString(randBytes(10)) }

// Token 生成带前缀的随机令牌，明文只交给调用方一次，库里只存 Hash。
func Token(prefix string) string { return prefix + "_" + b32.EncodeToString(randBytes(24)) }

// Hash 是令牌入库前的单向哈希。
func Hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// HexToken 生成 n 字节的十六进制随机串，订阅 token 沿用 Xboard 的 32 位十六进制格式。
func HexToken(n int) string { return hex.EncodeToString(randBytes(n)) }

// NumericCode 生成 n 位数字码，例如审批用的一次性码。
func NumericCode(n int) string {
	out := make([]byte, 0, n)
	for len(out) < n {
		for _, c := range randBytes(n) {
			// 丢掉 250 以上的值，避免取模带来的偏差
			if c < 250 && len(out) < n {
				out = append(out, '0'+c%10)
			}
		}
	}
	return string(out)
}

// UserCode 是给人看的短码，格式 XXXX-XXXX，去掉了容易混淆的字符。
func UserCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := randBytes(8)
	var sb strings.Builder
	for i, c := range b {
		if i == 4 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return sb.String()
}

// UUID 生成 v4 UUID 字符串，用作用户连接凭证。
func UUID() string {
	b := randBytes(16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Box 用主密钥加密需要可逆保存的数据（例如节点密钥），并给回调数据签名。
type Box struct {
	aead   cipher.AEAD
	macKey []byte
}

func NewBox(masterKey string) (*Box, error) {
	if len(masterKey) < 32 {
		return nil, errors.New("master key too short")
	}
	encKey := sha256.Sum256([]byte("patato/enc/" + masterKey))
	macKey := sha256.Sum256([]byte("patato/mac/" + masterKey))
	block, err := aes.NewCipher(encKey[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, macKey: macKey[:]}, nil
}

func (b *Box) Seal(plain []byte) []byte {
	nonce := randBytes(b.aead.NonceSize())
	return b.aead.Seal(nonce, nonce, plain, nil)
}

func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

// Sign 返回 msg 的 HMAC，截断为 n 个十六进制字符。
func (b *Box) Sign(msg string, n int) string {
	m := hmac.New(sha256.New, b.macKey)
	m.Write([]byte(msg))
	s := hex.EncodeToString(m.Sum(nil))
	if n > 0 && n < len(s) {
		s = s[:n]
	}
	return s
}

func (b *Box) Verify(msg, sig string) bool {
	want := b.Sign(msg, len(sig))
	return len(sig) >= 8 && hmac.Equal([]byte(want), []byte(sig))
}
