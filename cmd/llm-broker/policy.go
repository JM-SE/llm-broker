package main

import (
	"context"
	"fmt"
	"io"

	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// runPolicy implements the configuration-only validate command and the
// provisioner-backed diff/apply/status commands.
func runPolicy(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "llm-broker policy: missing subcommand (validate|diff|apply|status)")
		return exitUsage
	}
	sub := args[0]
	if sub == "help" || sub == "--help" || sub == "-h" {
		fmt.Fprintln(stderr, "usage: llm-broker policy validate|diff|apply|status [path]")
		return exitOk
	}
	if len(args) > 2 {
		fmt.Fprintln(stderr, "llm-broker policy: expected at most one configuration path")
		return exitUsage
	}
	path, ok := resolveConfigPath(args[1:], stderr)
	if !ok {
		return exitUsage
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	if sub == "validate" {
		fmt.Fprintf(stdout, "ok: %s is a valid configuration (version %d)\n", config.FilePath(path), config.Version)
		return exitOk
	}
	if sub != "diff" && sub != "apply" && sub != "status" {
		fmt.Fprintf(stderr, "llm-broker policy: unknown subcommand %q (validate|diff|apply|status)\n", sub)
		return exitUsage
	}
	desired, err := cfg.PolicyProjections()
	if err != nil {
		fmt.Fprintf(stderr, "invalid policy projection: %v\n", err)
		return 1
	}
	_, st, ok := openProvisionerStore(ctx, stderr)
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.CheckSchemaCompatible(ctx); err != nil {
		fmt.Fprintf(stderr, "llm-broker policy %s: schema is not ready: %v\n", sub, err)
		return 1
	}
	var summary postgres.PolicyReportSummary
	switch sub {
	case "diff":
		summary, err = st.DiffPolicies(ctx, desired)
	case "apply":
		summary, err = st.ApplyPolicies(ctx, desired)
	case "status":
		summary, err = st.StatusPolicies(ctx, desired)
	}
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker policy %s: %v\n", sub, err)
		return 1
	}
	printPolicySummary(stdout, sub, summary)
	if sub == "apply" && (summary.Create > 0 || summary.TopologyMismatch > 0) {
		fmt.Fprintln(stderr, "policy apply: topology changed; planned stop/apply/restart is required before serving")
	}
	if sub == "diff" && (summary.Create > 0 || summary.Update > 0 || summary.TopologyMismatch > 0) {
		return 2
	}
	if sub == "status" && (summary.Create > 0 || summary.Update > 0 || summary.TopologyMismatch > 0) {
		return 2
	}
	return exitOk
}

func printPolicySummary(out io.Writer, command string, s postgres.PolicyReportSummary) {
	fmt.Fprintf(out, "%s: create=%d update=%d unchanged=%d unmanaged=%d topology-mismatch=%d\n",
		command, s.Create, s.Update, s.Unchanged, s.Unmanaged, s.TopologyMismatch)
}
