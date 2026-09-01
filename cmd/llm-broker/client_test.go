package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

func TestWriteCredentialUsesExclusive0600File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	var out bytes.Buffer
	secret := "lbk_test-secret-must-not-be-printed"
	if err := writeCredential(path, "client created", secret, &out); err != nil {
		t.Fatalf("writeCredential: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 600", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if string(content) != secret+"\n" {
		t.Fatalf("credential content = %q", content)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("credential appeared in command output: %q", out.String())
	}
	if err := writeCredential(path, "again", secret, &out); err == nil {
		t.Fatal("second write unexpectedly replaced credential")
	}
}

func TestCredentialFileArg(t *testing.T) {
	if got, err := credentialFileArg([]string{"--credential-file", "/tmp/token"}); err != nil || got != "/tmp/token" {
		t.Fatalf("valid credential-file args = %q, %v", got, err)
	}
	for _, args := range [][]string{nil, {}, {"--credential-file"}, {"--other", "/tmp/token"}} {
		if _, err := credentialFileArg(args); err == nil {
			t.Fatalf("credential-file args %v unexpectedly accepted", args)
		}
	}
}

type fakeClientCreator struct {
	calls int
	err   error
}

func (f *fakeClientCreator) CreateClient(context.Context, postgres.CreateClientParams) (postgres.Client, error) {
	f.calls++
	return postgres.Client{}, f.err
}

func TestClientCreateWritesBeforeDatabaseCommit(t *testing.T) {
	creator := &fakeClientCreator{err: errors.New("database unavailable")}
	path := filepath.Join(t.TempDir(), "credential")
	if err := runClientCreate(context.Background(), creator, "client", path, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("create unexpectedly succeeded")
	}
	if creator.calls != 1 {
		t.Fatalf("create calls = %d, want 1", creator.calls)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted credential file stat = %v, want not found", err)
	}
}

func TestClientCreateDoesNotTouchDatabaseWhenCredentialFileFails(t *testing.T) {
	creator := &fakeClientCreator{}
	path := filepath.Join(t.TempDir(), "missing", "credential")
	if err := runClientCreate(context.Background(), creator, "client", path, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("create unexpectedly succeeded")
	}
	if creator.calls != 0 {
		t.Fatalf("create calls = %d after file failure, want 0", creator.calls)
	}
}

type fakeClientRotator struct {
	client postgres.Client
	calls  int
	err    error
}

func (f *fakeClientRotator) GetClientByName(context.Context, string) (postgres.Client, error) {
	return f.client, nil
}

func (f *fakeClientRotator) RotateClientCredential(context.Context, int64, string, []byte) (postgres.Client, error) {
	f.calls++
	return postgres.Client{}, f.err
}

func TestClientRotatePreservesOldCredentialWhenDatabaseCommitFails(t *testing.T) {
	rotator := &fakeClientRotator{client: postgres.Client{ID: 7}, err: errors.New("database unavailable")}
	path := filepath.Join(t.TempDir(), "replacement")
	if err := runClientRevoke(context.Background(), rotator, "client", path, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("rotate unexpectedly succeeded")
	}
	if rotator.calls != 1 {
		t.Fatalf("rotate calls = %d, want 1", rotator.calls)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted replacement file stat = %v, want not found", err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
