package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/JM-SE/llm-broker/internal/compat"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// runCompat implements the read-only consumer compatibility preflight. The
// manifest is parsed before any database wiring; desired mode never wires a
// database at all.
func runCompat(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "llm-broker compat: missing subcommand (desired|effective)")
		return exitUsage
	}
	sub := args[0]
	if sub == "help" || sub == "--help" || sub == "-h" {
		printCompatUsage(stderr)
		return exitOk
	}
	parsed, ok := parseCompatArgs(sub, args[1:], stderr)
	if !ok {
		return exitUsage
	}
	manifest, err := compat.ParseFile(parsed.manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat: invalid manifest")
		return 1
	}
	digest, err := manifest.DigestString()
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat: invalid manifest")
		return 1
	}

	path, ok := resolveConfigPath(parsed.configArgs, stderr)
	if !ok {
		return exitUsage
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat: invalid configuration")
		return 1
	}

	var decision compat.Decision
	if sub == "desired" {
		decision, err = compat.EvaluateDesired(cfg, manifest)
	} else {
		state, stateOK := readEffectiveState(ctx, cfg, manifest.Workload, parsed.client, stderr)
		if !stateOK {
			return 1
		}
		decision, err = compat.EvaluateEffective(cfg, manifest, state)
	}
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat: compatibility evaluation failed")
		return 1
	}
	printCompatDecision(stdout, digest, decision)
	if decision.Compatible() {
		return exitOk
	}
	return exitIncompatible
}

type compatArgs struct {
	manifestPath string
	client       string
	configArgs   []string
}

func parseCompatArgs(sub string, args []string, stderr io.Writer) (compatArgs, bool) {
	if sub != "desired" && sub != "effective" {
		fmt.Fprintf(stderr, "llm-broker compat: unknown subcommand %q (desired|effective)\n", sub)
		return compatArgs{}, false
	}
	var out compatArgs
	pathStarted := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if pathStarted {
			fmt.Fprintln(stderr, "llm-broker compat: configuration path must be the final argument")
			return compatArgs{}, false
		}
		if arg == "--manifest" || arg == "--client" {
			if sub == "desired" && arg == "--client" {
				fmt.Fprintln(stderr, "llm-broker compat desired: unknown flag")
				return compatArgs{}, false
			}
			if arg == "--manifest" && out.manifestPath != "" {
				fmt.Fprintln(stderr, "llm-broker compat: duplicate --manifest")
				return compatArgs{}, false
			}
			if sub == "effective" && arg == "--client" && out.client != "" {
				fmt.Fprintln(stderr, "llm-broker compat: duplicate --client")
				return compatArgs{}, false
			}
			if i+1 >= len(args) || args[i+1] == "" || args[i+1][0] == '-' {
				fmt.Fprintf(stderr, "llm-broker compat: %s requires a value\n", arg)
				return compatArgs{}, false
			}
			value := args[i+1]
			i++
			if arg == "--manifest" {
				out.manifestPath = value
			} else {
				out.client = value
			}
			continue
		}
		if len(arg) > 0 && arg[0] == '-' {
			fmt.Fprintf(stderr, "llm-broker compat: unknown flag %q\n", arg)
			return compatArgs{}, false
		}
		if len(out.configArgs) != 0 {
			fmt.Fprintln(stderr, "llm-broker compat: expected at most one configuration path")
			return compatArgs{}, false
		}
		pathStarted = true
		out.configArgs = []string{arg}
	}
	if out.manifestPath == "" {
		fmt.Fprintln(stderr, "llm-broker compat: --manifest is required")
		return compatArgs{}, false
	}
	if sub == "effective" && out.client == "" {
		fmt.Fprintln(stderr, "llm-broker compat effective: --client is required")
		return compatArgs{}, false
	}
	if err := validateCompatIdentifier(out.client, sub == "effective"); err != nil {
		fmt.Fprintln(stderr, "llm-broker compat: invalid client argument")
		return compatArgs{}, false
	}
	return out, true
}

func validateCompatIdentifier(value string, required bool) error {
	if !required {
		return nil
	}
	return validateSlugArgument(value)
}

func validateSlugArgument(value string) error {
	return domain.ValidateSlug("client", value)
}

func printCompatUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: llm-broker compat desired --manifest <path> [config-path]")
	fmt.Fprintln(w, "       llm-broker compat effective --manifest <path> --client <name> [config-path]")
}

func printCompatDecision(out io.Writer, digest string, decision compat.Decision) {
	status := "incompatible"
	if decision.Compatible() {
		status = "compatible"
	}
	fmt.Fprintln(out, status)
	fmt.Fprintln(out, digest)
	for _, finding := range decision.Findings.Sorted() {
		fmt.Fprintf(out, "%s=%d\n", finding.Code, finding.Count)
	}
}

func readEffectiveState(ctx context.Context, cfg *config.Config, workload, client string, stderr io.Writer) (compat.EffectiveState, bool) {
	url := os.Getenv(cfg.Database.ProvisionerURLEnv)
	if cfg.Database.ProvisionerURLEnv == "" || url == "" {
		fmt.Fprintln(stderr, "llm-broker compat effective: operational error")
		return compat.EffectiveState{}, false
	}
	st, err := postgres.Open(ctx, url, postgres.Options{
		MaxConns:       int32(cfg.Database.MaxConnections),
		ConnectTimeout: time.Duration(cfg.Database.ConnectTimeoutSeconds) * time.Second,
	})
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat effective: operational error")
		return compat.EffectiveState{}, false
	}
	defer st.Close()
	if err := st.CheckSchemaCompatible(ctx); err != nil {
		fmt.Fprintln(stderr, "llm-broker compat effective: operational error")
		return compat.EffectiveState{}, false
	}
	state := compat.EffectiveState{}
	clientRow, err := st.GetCompatibilityClientByName(ctx, client)
	if err == postgres.ErrNotFound {
		state.ClientFound = false
		return state, true
	}
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat effective: operational error")
		return compat.EffectiveState{}, false
	}
	state.ClientFound = true
	state.ClientEnabled = clientRow.Enabled
	policyRow, err := st.GetWorkloadPolicyByName(ctx, workload)
	if err == postgres.ErrNotFound {
		return state, true
	}
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat effective: operational error")
		return compat.EffectiveState{}, false
	}
	state.PolicyFound = true
	state.Policy = effectivePolicy(policyRow)
	state.GrantPresent, err = st.ClientHasWorkload(ctx, clientRow.ID, policyRow.ID)
	if err != nil {
		fmt.Fprintln(stderr, "llm-broker compat effective: operational error")
		return compat.EffectiveState{}, false
	}
	return state, true
}

func effectivePolicy(p postgres.WorkloadPolicy) policy.Workload {
	var digest [32]byte
	if len(p.TopologyDigest) == len(digest) {
		copy(digest[:], p.TopologyDigest)
	}
	return policy.Workload{
		Name: p.Name, PriorityClass: p.PriorityClass, MaxActiveTasks: p.MaxActiveTasks,
		MaxAttempts: p.MaxAttempts, RetryDelaysSeconds: append([]int(nil), p.RetryDelaysSeconds...),
		MaxOutputTokens: p.MaxOutputTokens, MaxOutputBytes: p.MaxOutputBytes,
		MaxOutputBytesExplicit:   p.MaxOutputBytesExplicit,
		AllowedCapabilities:      append([]string(nil), p.AllowedCapabilities...),
		AllowedGeneration:        append([]byte(nil), p.AllowedGeneration...),
		RequiredFeatures:         append([]string(nil), p.RequiredFeatures...),
		WorkloadTimeoutSeconds:   p.WorkloadTimeoutSeconds,
		PayloadRetentionSeconds:  p.PayloadRetentionSeconds,
		ResultRetentionSeconds:   p.ResultRetentionSeconds,
		MetadataRetentionSeconds: p.MetadataRetentionSeconds,
		TopologyDigest:           digest,
	}
}
