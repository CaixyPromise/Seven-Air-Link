package secure

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"seven-air-link/internal/thirdparty/pbkdf2"
)

func TestPasswordHashPolicySaltAndVerification(t *testing.T) {
	password := "  correct horse 电池 staple  "
	a, e := HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	b, e := HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	if a == b || strings.Contains(a, password) {
		t.Fatal("salt reuse/plaintext password")
	}
	if !VerifyPassword(a, password) || VerifyPassword(a, strings.TrimSpace(password)) || VerifyPassword(a, "incorrect password") {
		t.Fatal("verification/whitespace fidelity")
	}
	for _, p := range []string{"short", strings.Repeat("a", 1025), "twelve chars\n", string([]byte{0xff})} {
		if _, e := HashPassword(p); e == nil {
			t.Fatalf("accepted invalid password %q", p)
		}
	}
	for _, v := range []string{"", "$argon2id$unknown", strings.Replace(a, "i=600000", "i=999999999999", 1), strings.Replace(a, "v=1", "v=99", 1), a + "$extra"} {
		if ValidatePasswordHash(v) == nil || VerifyPassword(v, password) {
			t.Fatal("invalid stored KDF accepted")
		}
	}
}
func TestPBKDF2SHA256KnownVectors(t *testing.T) {
	for _, v := range []struct {
		n    int
		want string
	}{{1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"}, {2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"}} {
		got := pbkdf2.Key([]byte("password"), []byte("salt"), v.n, 32, sha256.New)
		if hex.EncodeToString(got) != v.want {
			t.Fatal("PBKDF2 test vector mismatch")
		}
	}
}
