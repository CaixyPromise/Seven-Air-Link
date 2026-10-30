package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"seven-air-link/internal/thirdparty/pbkdf2"
)

// Keep a versioned, strictly bounded format. Do not accept arbitrary KDF work
// factors from a database record: a corrupt record must fail closed, not hang.
const passwordPrefix = "$pbkdf2-sha256$v=1$i=600000$"
const PasswordIterations = 600000
const MaxPasswordBytes = 1024

var ErrPasswordPolicy = errors.New("password must contain at least 12 characters, at most 1024 UTF-8 bytes, and no NUL/CR/LF")

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > MaxPasswordBytes || strings.ContainsAny(password, "\x00\r\n") {
		return ErrPasswordPolicy
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	key := pbkdf2.Key([]byte(password), salt, PasswordIterations, 32, sha256.New)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func passwordParts(encoded string) ([]byte, []byte, error) {
	if !strings.HasPrefix(encoded, passwordPrefix) {
		return nil, nil, errors.New("unsupported password hash format")
	}
	parts := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
	if len(parts) != 2 {
		return nil, nil, errors.New("invalid password hash")
	}
	salt, e1 := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	key, e2 := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(key) != 32 {
		return nil, nil, errors.New("invalid password hash encoding")
	}
	return salt, key, nil
}

func ValidatePasswordHash(encoded string) error { _, _, e := passwordParts(encoded); return e }

// VerifyPassword accepts only bounded input and compares equal-length derived
// keys in constant time. No password or derived key belongs in logs or audit.
func VerifyPassword(encoded, password string) bool {
	if len(password) > MaxPasswordBytes || !utf8.ValidString(password) {
		return false
	}
	salt, want, e := passwordParts(encoded)
	if e != nil {
		return false
	}
	got := pbkdf2.Key([]byte(password), salt, PasswordIterations, 32, sha256.New)
	return subtle.ConstantTimeCompare(want, got) == 1
}
