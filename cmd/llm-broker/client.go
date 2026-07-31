package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// runClient implements the `client` command: offline provisioning of API
// credentials and workload grants. Tokens are printed exactly once.
func runClient(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "llm-broker client: missing subcommand (create|disable|revoke|grant)")
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "help", "--help", "-h":
		fmt.Fprintln(stderr, `usage:
  llm-broker client create <name>           create a client and print its credential once
  llm-broker client disable <name>          disable a client's credential
  llm-broker client revoke <name>           rotate a client's credential (prints the new one once)
  llm-broker client grant <name> <workload> grant a workload to a client`)
		return exitOk
	case "create":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "llm-broker client create: missing client name")
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
	case "grant":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "llm-broker client grant: missing client name or workload")
			return exitUsage
		}
	default:
		fmt.Fprintf(stderr, "llm-broker client: unknown subcommand %q (create|disable|revoke|grant)\n", sub)
		return exitUsage
	}

	_, st, ok := openStore(ctx, stderr)
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
		err = runClientCreate(ctx, st, name, stdout, stderr)
	case "disable":
		err = runClientDisable(ctx, st, name, stdout, stderr)
	case "revoke":
		err = runClientRevoke(ctx, st, name, stdout, stderr)
	case "grant":
		err = runClientGrant(ctx, st, name, args[2], stdout, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker client %s: %v\n", sub, err)
		return 1
	}
	return exitOk
}

func runClientCreate(ctx context.Context, st *postgres.Store, name string, stdout, stderr io.Writer) error {
	tok, err := auth.Generate()
	if err != nil {
		return err
	}
	if _, err := st.CreateClient(ctx, postgres.CreateClientParams{
		Name:                 name,
		CredentialIdentifier: tok.Identifier,
		CredentialDigest:     tok.Digest[:],
		Enabled:              true,
	}); err != nil {
		return err
	}
	printCredential(stdout, fmt.Sprintf("client %q created", name), tok.Value)
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

func runClientRevoke(ctx context.Context, st *postgres.Store, name string, stdout, stderr io.Writer) error {
	c, err := st.GetClientByName(ctx, name)
	if err != nil {
		return err
	}
	tok, err := auth.Generate()
	if err != nil {
		return err
	}
	if _, err := st.RotateClientCredential(ctx, c.ID, tok.Identifier, tok.Digest[:]); err != nil {
		return err
	}
	printCredential(stdout, fmt.Sprintf("client %q credential rotated", name), tok.Value)
	return nil
}

func runClientGrant(ctx context.Context, st *postgres.Store, name, workload string, stdout, stderr io.Writer) error {
	c, err := st.GetClientByName(ctx, name)
	if err != nil {
		return err
	}
	p, err := st.GetWorkloadPolicyByName(ctx, workload)
	if errors.Is(err, postgres.ErrNotFound) {
		return fmt.Errorf("workload %q is not configured", workload)
	}
	if err != nil {
		return err
	}
	if err := st.GrantWorkload(ctx, c.ID, p.ID); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "granted workload %q to client %q\n", workload, name)
	return nil
}

// printCredential prints a one-time credential with the required warning.
func printCredential(stdout io.Writer, what, token string) {
	fmt.Fprintf(stdout, "%s\n", what)
	fmt.Fprintf(stdout, "credential (shown once, not recoverable):\n%s\n", token)
}
