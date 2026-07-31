// Package auth implements client credential generation and constant-time
// verification. Tokens are shown to the operator exactly once at
// provisioning; the database stores only a SHA-256 digest plus a short
// identifier used for lookup. Verification never compares secrets in
// variable time and never reveals whether a client exists.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// Prefix marks broker credentials so malformed values fail fast.
	Prefix = "lbk_"
	// TokenRawBytes is the entropy of one credential (256 bits).
	TokenRawBytes = 32
	// IdentifierLen is the lookup prefix length in characters.
	IdentifierLen = 12
)

// Errors returned by verification.
var (
	ErrMalformedToken = errors.New("malformed credential")
	ErrInvalidToken   = errors.New("invalid credential")
)

// Token is a freshly provisioned credential. Value is shown to the operator
// exactly once; Digest is what gets stored; Identifier is the lookup key.
type Token struct {
	Value      string
	Identifier string
	Digest     [32]byte
}

// Generate creates a new credential from a CSPRNG.
func Generate() (Token, error) {
	raw := make([]byte, TokenRawBytes)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, fmt.Errorf("generate credential: %w", err)
	}
	value := Prefix + base64.RawURLEncoding.EncodeToString(raw)
	return Token{
		Value:      value,
		Identifier: value[len(Prefix) : len(Prefix)+IdentifierLen],
		Digest:     sha256.Sum256([]byte(value)),
	}, nil
}

// DigestOf returns the SHA-256 digest of a credential value.
func DigestOf(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

// IdentifierOf extracts the lookup identifier from a credential value,
// validating shape (prefix, length, base64 alphabet).
func IdentifierOf(value string) (string, error) {
	if !strings.HasPrefix(value, Prefix) {
		return "", ErrMalformedToken
	}
	encoded := value[len(Prefix):]
	if len(encoded) != base64.RawURLEncoding.EncodedLen(TokenRawBytes) {
		return "", ErrMalformedToken
	}
	if _, err := base64.RawURLEncoding.DecodeString(encoded); err != nil {
		return "", ErrMalformedToken
	}
	return encoded[:IdentifierLen], nil
}
