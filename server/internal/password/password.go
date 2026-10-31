// Package password 负责终端用户密码的哈希与校验。
//
// 新密码用 argon2id；从 Xboard 迁移来的密码保留原算法（bcrypt、md5、sha256 及加盐变体），
// 用户下次登录成功后升级为 argon2id。
package password

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const Argon2id = "argon2id"

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// Hash 用 argon2id 生成可自描述的哈希串。
func Hash(plain string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key))
}

// Verify 按 algo 校验密码，algo 取值沿用 Xboard 的 password_algo；空值等同 bcrypt。
func Verify(algo, salt, plain, hash string) bool {
	if hash == "" {
		return false
	}
	switch algo {
	case Argon2id:
		return verifyArgon(plain, hash)
	case "md5":
		return eq(md5hex(plain), hash)
	case "sha256":
		return eq(sha256hex(plain), hash)
	case "md5salt":
		return eq(md5hex(plain+salt), hash)
	case "sha256salt":
		return eq(sha256hex(plain+salt), hash)
	default:
		// PHP 的 password_hash 生成 $2y$ 前缀，Go 的 bcrypt 只认 $2a$/$2b$，算法本身一致。
		h := hash
		if strings.HasPrefix(h, "$2y$") {
			h = "$2a$" + h[4:]
		}
		return bcrypt.CompareHashAndPassword([]byte(h), []byte(plain)) == nil
	}
}

// NeedsUpgrade 表示登录成功后应把哈希升级为 argon2id。
func NeedsUpgrade(algo string) bool { return algo != Argon2id }

func verifyArgon(plain, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func eq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// Validate 检查新密码是否满足最低要求。
func Validate(plain string) error {
	if len([]rune(plain)) < 8 {
		return fmt.Errorf("密码至少 8 位")
	}
	return nil
}
