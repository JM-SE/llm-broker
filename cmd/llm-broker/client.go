package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// runClient implements the `client` command: offline provisioning of API
// credentials and workload grants. Credentials are written to an explicitly
// selected, exclusive 0600 file; plaintext credentials are never printed.
func runClient(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "llm-broker client: missing subcommand (create|disable|revoke|grant|ungrant)")
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "help", "--help", "-h":
		fmt.Fprintln(stderr, `usage:
	  llm-broker client create <name> --credential-file <path>
	                                             create a client and write its credential
	  llm-broker client disable <name>          disable a client's credential
	  llm-broker client revoke <name> --credential-file <path>
	                                             rotate and write the new credential
	  llm-broker client grant <name> <workload>   grant a workload to a client
  llm-broker client ungrant <name> <workload> remove only that workload grant`)
		return exitOk
	case "create":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "llm-broker client create: missing client name")
			return exitUsage
		}
		if _, err := credentialFileArg(args[2:]); err != nil {
			fmt.Fprintf(stderr, "llm-broker client create: %v\n", err)
			return exitUsage
		}
	case "disable":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "llm-broker client disable: missing client name")
			return exitUsage
		}
	case "revoke":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "llm-broker client revoke: missing client name")
			return exitUsage
		}
		if _, err := credentialFileArg(args[2:]); err != nil {
			fmt.Fprintf(stderr, "llm-broker client revoke: %v\n", err)
			return exitUsage
		}
	case "grant":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "llm-broker client grant: missing client name or workload")
			return exitUsage
		}
	case "ungrant":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "llm-broker client ungrant: missing client name or workload")
			return exitUsage
		}
	default:
		fmt.Fprintf(stderr, "llm-broker client: unknown subcommand %q (create|disable|revoke|grant|ungrant)\n", sub)
		return exitUsage
	}

	_, st, ok := openProvisionerStore(ctx, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	name := args[1]
	if err := domain.ValidateSlug("client", name); err != nil {
		fmt.Fprintf(stderr, "llm-broker client %s: %v\n", sub, err)
		return exitUsage
	}

	var err error
	switch sub {
	case "create":
		file, _ := credentialFileArg(args[2:])
		err = runClientCreate(ctx, st, name, file, stdout, stderr)
	case "disable":
		err = runClientDisable(ctx, st, name, stdout, stderr)
	case "revoke":
		file, _ := credentialFileArg(args[2:])
		err = runClientRevoke(ctx, st, name, file, stdout, stderr)
	case "grant":
		err = runClientGrant(ctx, st, name, args[2], stdout, stderr)
	case "ungrant":
		err = runClientUngrant(ctx, st, name, args[2], stdout, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker client %s: %v\n", sub, err)
		return 1
	}
	return exitOk
}

func credentialFileArg(args []string) (string, error) {
	if len(args) != 2 || args[0] != "--credential-file" || args[1] == "" {
		return "", errors.New("requires --credential-file <path>")
	}
	return args[1], nil
}

type clientCreator interface {
	CreateClient(context.Context, postgres.CreateClientParams) (postgres.Client, error)
}

func runClientCreate(ctx context.Context, st clientCreator, name, credentialFile string, stdout, stderr io.Writer) error {
	tok, err := auth.Generate()
	if err != nil {
		return err
	}
	if err := writeCredentialFile(credentialFile, tok.Value); err != nil {
		return err
	}
	if _, err := st.CreateClient(ctx, postgres.CreateClientParams{
		Name:                 name,
		CredentialIdentifier: tok.Identifier,
		CredentialDigest:     tok.Digest[:],
		Enabled:              true,
	}); err != nil {
		if removeErr := os.Remove(credentialFile); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("create client: %w (remove uncommitted credential file: %v)", err, removeErr)
		}
		return err
	}
	fmt.Fprintf(stdout, "client %q created; credential written to %s (mode 0600)\n", name, credentialFile)
	return nil
}

func runClientDisable(ctx context.Context, st *postgres.Store, name string, stdout, stderr io.Writer) error {
	c, err := st.GetClientByName(ctx, name)
	if err != nil {
		return err
	}
	if err := st.SetClientEnabled(ctx, c.ID, false); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "client %q disabled; its credential is rejected\n", name)
	return nil
}

type clientRotator interface {
	GetClientByName(context.Context, string) (postgres.Client, error)
	RotateClientCredential(context.Context, int64, string, []byte) (postgres.Client, error)
}

func runClientRevoke(ctx context.Context, st clientRotator, name, credentialFile string, stdout, stderr io.Writer) error {
	c, err := st.GetClientByName(ctx, name)
	if err != nil {
		return err
	}
	tok, err := auth.Generate()
	if err != nil {
		return err
	}
	if err := writeCredentialFile(credentialFile, tok.Value); err != nil {
		return err
	}
	if _, err := st.RotateClientCredential(ctx, c.ID, tok.Identifier, tok.Digest[:]); err != nil {
		if removeErr := os.Remove(credentialFile); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("rotate client: %w (remove uncommitted credential file: %v)", err, removeErr)
		}
		return err
	}
	fmt.Fprintf(stdout, "client %q credential rotated; credential written to %s (mode 0600)\n", name, credentialFile)
	return nil
}

func runClientGrant(ctx context.Context, st *postgres.Store, name, workload string, stdout, stderr io.Writer) error {
	c, err := st.GetClientByName(ctx, name)
	if err != nil {
		return err
	}
	if err := st.GrantWorkloadBySlug(ctx, c.ID, workload); errors.Is(err, postgres.ErrNotFound) {
		return fmt.Errorf("workload is not configured")
	} else if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "granted workload %q to client %q\n", workload, name)
	return nil
}

func runClientUngrant(ctx context.Context, st *postgres.Store, name, workload string, stdout, stderr io.Writer) error {
	c, err := st.GetClientByName(ctx, name)
	if err != nil {
		return err
	}
	if err := st.RevokeWorkloadBySlug(ctx, c.ID, workload); errors.Is(err, postgres.ErrNotFound) {
		return fmt.Errorf("workload is not configured")
	} else if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed workload grant for client %q\n", name)
	return nil
}

func writeCredential(path, what, token string, stdout io.Writer) error {
	if err := writeCredentialFile(path, token); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s; credential written to %s (mode 0600)\n", what, path)
	return nil
}

func writeCredentialFile(path, token string) error {
	if path == "" {
		return errors.New("credential file path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if info, err := os.Stat(dir); err != nil {
			return fmt.Errorf("credential file directory: %w", err)
		} else if !info.IsDir() {
			return fmt.Errorf("credential file directory is not a directory")
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create credential file: %w", err)
	}
	if _, err := io.WriteString(f, token+"\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write credential file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close credential file: %w", err)
	}
	return nil
}
