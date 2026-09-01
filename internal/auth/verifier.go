package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
)

// Identity is the authenticated client attached to a request.
type Identity struct {
	ClientID int64
	Name     string
	// DigestProof is derived from the presented credential. It is deliberately
	// not the credential itself and is safe to carry across the API/store
	// boundary for the post-lock recheck.
	DigestProof [32]byte
}

// LookupFunc resolves a token identifier to the stored digest, the enabled
// state, and the client identity. A not-found result is (zero, nil, false,
// nil); only real lookup errors are returned as errors.
type LookupFunc func(ctx context.Context, identifier string) (Identity, []byte, bool, error)

// Verifier authenticates credentials in constant time relative to the
// presented secret. Unknown identifiers are compared against a fixed dummy
// digest so that verification cost does not reveal client existence.
type Verifier struct {
	lookup LookupFunc
	dummy  [32]byte
}

// NewVerifier builds a verifier backed by lookup.
func NewVerifier(lookup LookupFunc) *Verifier {
	return &Verifier{
		lookup: lookup,
		dummy:  sha256.Sum256([]byte("llm-broker-dummy-credential")),
	}
}

// Verify authenticates a bearer credential value. It returns ErrMalformedToken
// for values that cannot be a broker credential and ErrInvalidToken for
// unknown, mismatched, or disabled credentials — the same error for every
// invalid case, by design.
func (v *Verifier) Verify(ctx context.Context, value string) (Identity, error) {
	identifier, err := IdentifierOf(value)
	if err != nil {
		return Identity{}, ErrMalformedToken
	}
	ident, digest, enabled, err := v.lookup(ctx, identifier)
	if err != nil {
		return Identity{}, err
	}
	if digest == nil {
		digest = v.dummy[:]
	}
	provided := DigestOf(value)
	if !enabled || subtle.ConstantTimeCompare(provided[:], digest) != 1 {
		return Identity{}, ErrInvalidToken
	}
	ident.DigestProof = provided
	return ident, nil
}
