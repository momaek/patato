package password

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestArgon2idRoundTrip(t *testing.T) {
	h := Hash("correct horse")
	if !Verify(Argon2id, "", "correct horse", h) {
		t.Fatal("argon2id hash should verify")
	}
	if Verify(Argon2id, "", "wrong", h) {
		t.Fatal("wrong password must not verify")
	}
	if Hash("correct horse") == h {
		t.Fatal("hashes must be salted")
	}
}

func TestXboardLegacyAlgorithms(t *testing.T) {
	md5hex := func(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }
	sha := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	bc, _ := bcrypt.GenerateFromPassword([]byte("pw-12345"), bcrypt.MinCost)
	php := "$2y$" + string(bc[4:]) // PHP password_hash 的前缀

	cases := []struct {
		algo, salt, hash string
	}{
		{"md5", "", md5hex("pw-12345")},
		{"sha256", "", sha("pw-12345")},
		{"md5salt", "s4lt", md5hex("pw-12345s4lt")},
		{"sha256salt", "s4lt", sha("pw-12345s4lt")},
		{"", "", php},
		{"bcrypt", "", string(bc)},
	}
	for _, c := range cases {
		if !Verify(c.algo, c.salt, "pw-12345", c.hash) {
			t.Errorf("%q should verify", c.algo)
		}
		if Verify(c.algo, c.salt, "nope", c.hash) {
			t.Errorf("%q must reject wrong password", c.algo)
		}
		if !NeedsUpgrade(c.algo) {
			t.Errorf("%q should be upgraded after login", c.algo)
		}
	}
}

func TestEmptyHashNeverVerifies(t *testing.T) {
	if Verify(Argon2id, "", "", "") || Verify("md5", "", "", "") {
		t.Fatal("empty hash must never verify")
	}
}
