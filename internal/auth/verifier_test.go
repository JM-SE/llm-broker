package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGenerateFormat(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Value, Prefix) {
		t.Errorf("value %q missing prefix", tok.Value)
	}
	wantLen := len(Prefix) + 43 // 32 bytes in raw URL base64
	if len(tok.Value) != wantLen {
		t.Errorf("value length = %d, want %d", len(tok.Value), wantLen)
	}
	if len(tok.Identifier) != IdentifierLen {
		t.Errorf("identifier length = %d, want %d", len(tok.Identifier), IdentifierLen)
	}
	if len(tok.Digest) != 32 {
		t.Errorf("digest length = %d, want 32", len(tok.Digest))
	}
	id, err := IdentifierOf(tok.Value)
	if err != nil || id != tok.Identifier {
		t.Errorf("IdentifierOf = %q, %v; want %q", id, err, tok.Identifier)
	}
}

func TestGenerateUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok.Value] {
			t.Fatalf("duplicate token %q", tok.Value)
		}
		seen[tok.Value] = true
	}
}

func TestIdentifierOfRejectsMalformed(t *testing.T) {
	valid, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"",
		"no-prefix-abcdefghijklmnopqrstuvwxyz0123456789ABCD",
		Prefix, // truncated
		Prefix + "short",
		Prefix + strings.Repeat("a", 42), // valid alphabet, wrong length
		Prefix + strings.Repeat("=", 43), // invalid base64 alphabet
		valid.Value + "extra",
	}
	for _, c := range cases {
		if _, err := IdentifierOf(c); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("IdentifierOf(%q) error = %v, want ErrMalformedToken", c, err)
		}
	}
}

func TestDigestOf(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if DigestOf(tok.Value) != tok.Digest {
		t.Error("DigestOf mismatch with generated digest")
	}
	if DigestOf(tok.Value) == DigestOf(tok.Value+"x") {
		t.Error("digest collision on altered value")
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, identifier string) (Identity, []byte, bool, error) {
		if identifier != tok.Identifier {
			return Identity{}, nil, false, nil
		}
		return Identity{ClientID: 7, Name: "alice"}, tok.Digest[:], true, nil
	}
	v := NewVerifier(lookup)
	got, err := v.Verify(context.Background(), tok.Value)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != 7 || got.Name != "alice" {
		t.Errorf("identity = %+v, want alice/7", got)
	}
	if got.DigestProof != tok.Digest {
		t.Error("identity did not carry the presented credential digest proof")
	}
}

func TestVerifyInvalidCases(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	other, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, identifier string) (Identity, []byte, bool, error) {
		if identifier != tok.Identifier {
			return Identity{}, nil, false, nil
		}
		return Identity{ClientID: 1, Name: "alice"}, tok.Digest[:], true, nil
	}
	v := NewVerifier(lookup)

	t.Run("wrong token for known identifier", func(t *testing.T) {
		if _, err := v.Verify(context.Background(), other.Value); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("error = %v, want ErrInvalidToken", err)
		}
	})
	t.Run("unknown identifier", func(t *testing.T) {
		unknown, _ := Generate()
		if _, err := v.Verify(context.Background(), unknown.Value); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("error = %v, want ErrInvalidToken", err)
		}
	})
	t.Run("disabled client", func(t *testing.T) {
		disabled := NewVerifier(func(_ context.Context, identifier string) (Identity, []byte, bool, error) {
			return Identity{ClientID: 1, Name: "alice"}, tok.Digest[:], false, nil
		})
		if _, err := disabled.Verify(context.Background(), tok.Value); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("error = %v, want ErrInvalidToken", err)
		}
	})
	t.Run("malformed value", func(t *testing.T) {
		if _, err := v.Verify(context.Background(), "not-a-token"); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("error = %v, want ErrMalformedToken", err)
		}
		if _, err := v.Verify(context.Background(), ""); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("error = %v, want ErrMalformedToken", err)
		}
	})
	t.Run("nil digest lookup", func(t *testing.T) {
		nilLookup := NewVerifier(func(_ context.Context, identifier string) (Identity, []byte, bool, error) {
			return Identity{}, nil, true, nil
		})
		if _, err := nilLookup.Verify(context.Background(), tok.Value); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("error = %v, want ErrInvalidToken", err)
		}
	})
}

func TestVerifyPropagatesLookupErrors(t *testing.T) {
	sentinel := errors.New("database down")
	v := NewVerifier(func(context.Context, string) (Identity, []byte, bool, error) {
		return Identity{}, nil, false, sentinel
	})
	tok, _ := Generate()
	if _, err := v.Verify(context.Background(), tok.Value); !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want sentinel", err)
	}
}
