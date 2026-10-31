package kernel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// certStore 生成并缓存自签名证书，按域名存一份，重启后沿用，客户端固定证书时不会失效。
type certStore struct{ dir string }

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9.-]+`)

func (s *certStore) selfSigned(domain string) (certPEM, keyPEM string, expiry *time.Time, err error) {
	base := filepath.Join(s.dir, "self-"+unsafeName.ReplaceAllString(domain, "_"))
	if c, err1 := os.ReadFile(base + ".crt"); err1 == nil {
		if k, err2 := os.ReadFile(base + ".key"); err2 == nil {
			if exp := certExpiry(c); exp != nil && time.Until(*exp) > 30*24*time.Hour {
				return string(c), string(k), exp, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	notAfter := time.Now().AddDate(10, 0, 0)
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(domain); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{domain}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", nil, err
	}
	c := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	k := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	if err := os.MkdirAll(s.dir, 0o700); err == nil {
		_ = os.WriteFile(base+".crt", c, 0o600)
		_ = os.WriteFile(base+".key", k, 0o600)
	}
	return string(c), string(k), &notAfter, nil
}

// certExpiry 返回 PEM 里第一张证书的到期时间。
func certExpiry(b []byte) *time.Time {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	t := c.NotAfter
	return &t
}

func fileExpiry(path string) *time.Time {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return certExpiry(b)
}
