package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/worker"

	// Adapter registration: the worker binary must link the built-in
	// adapters or capabilities cannot resolve.
	_ "github.com/JM-SE/llm-broker/internal/backend/fake"
	_ "github.com/JM-SE/llm-broker/internal/backend/openaicompat"
)

// runWorker implements the `worker` command: run (default), status, and
// resolve. Configuration comes from $LLM_BROKER_CONFIG_FILE.
func runWorker(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	sub := "run"
	if len(args) > 0 && args[0] != "" {
		sub = args[0]
	}
	switch sub {
	case "run", "":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "llm-broker worker: run takes no arguments; configuration comes from $LLM_BROKER_CONFIG_FILE")
			return exitUsage
		}
		return runWorkerMain(ctx, stdout, stderr)
	case "status":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "llm-broker worker status: takes no arguments")
			return exitUsage
		}
		return runWorkerStatus(ctx, stdout, stderr)
	case "resolve":
		return runWorkerResolve(ctx, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stderr, `usage:
  llm-broker worker                      run the execution worker
  llm-broker worker status               show unresolved ambiguous executions
  llm-broker worker resolve <task-id> --outcome=requeue|failed [--delay=N]
                                         resolve an ambiguous execution after
                                         the operator verified backend idleness`)
		return exitOk
	default:
		fmt.Fprintf(stderr, "llm-broker worker: unknown subcommand %q (run|status|resolve)\n", sub)
		return exitUsage
	}
}

// runWorkerMain runs the active execution loop until the process receives a
// termination signal.
func runWorkerMain(ctx context.Context, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	cfg, st, ok := openStore(ctx, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	policy := scheduler.DefaultPolicy()
	workerID := uuid.New()
	leader := scheduler.NewLeader(st, workerID, policy.HeartbeatInterval, logger)
	sched := scheduler.New(st, policy, leader)
	registry := backend.DefaultRegistry

	w := worker.New(worker.Options{
		Store:        st,
		Scheduler:    sched,
		Leader:       leader,
		Registry:     registry,
		Capabilities: cfg.Capabilities,
		Backends:     cfg.Backends,
		Policy:       policy,
		Logger:       logger,
	})
	logger.Info("worker starting", "worker_id", workerID)
	if err := w.Run(ctx); err != nil {
		logger.Error("worker stopped with error", "error", err)
		return 1
	}
	return exitOk
}

// runWorkerStatus lists unresolved ambiguous executions (running tasks with
// expired leases whose last attempt was indeterminate).
func runWorkerStatus(ctx context.Context, stdout, stderr io.Writer) int {
	_, st, ok := openStore(ctx, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	items, err := st.ListUnresolvedRecovery(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker worker status: %v\n", err)
		return 1
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "no unresolved ambiguous executions; the worker may run normally")
		return exitOk
	}
	fmt.Fprintf(stdout, "%d unresolved ambiguous execution(s); the worker is blocked until resolved:\n\n", len(items))
	for _, it := range items {
		fmt.Fprintf(stdout, "task %s\n", it.Task.ID)
		fmt.Fprintf(stdout, "  state: %s (lease expired)  attempts: %d\n", it.Task.State, it.Task.Attempts)
		fmt.Fprintf(stdout, "  last attempt #%d: class=%s code=%s\n", it.AttemptNo, it.ErrorClass, it.ErrorCode)
		fmt.Fprintf(stdout, "  created: %s\n\n", it.Task.CreatedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintln(stdout, "resolve with: llm-broker worker resolve <task-id> --outcome=requeue|failed")
	fmt.Fprintln(stdout, "requeue only after verifying the backend is idle (no inference still running).")
	return exitOk
}

// runWorkerResolve applies an operator decision to one ambiguous execution.
// The operator's verified evidence (backend idle) is the authorization; the
// resolution is recorded in the attempt row for audit.
func runWorkerResolve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "llm-broker worker resolve: missing task id")
		return exitUsage
	}
	taskID, err := uuid.Parse(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker worker resolve: invalid task id %q\n", args[0])
		return exitUsage
	}
	outcome := "failed"
	delay := 0
	for _, a := range args[1:] {
		switch {
		case strings.HasPrefix(a, "--outcome="):
			outcome = strings.TrimPrefix(a, "--outcome=")
		case strings.HasPrefix(a, "--delay="):
			if delay, err = strconv.Atoi(strings.TrimPrefix(a, "--delay=")); err != nil || delay < 0 {
				fmt.Fprintln(stderr, "llm-broker worker resolve: --delay must be a non-negative number of seconds")
				return exitUsage
			}
		default:
			fmt.Fprintf(stderr, "llm-broker worker resolve: unknown argument %q\n", a)
			return exitUsage
		}
	}
	if outcome != "requeue" && outcome != "failed" {
		fmt.Fprintln(stderr, "llm-broker worker resolve: --outcome must be requeue or failed")
		return exitUsage
	}

	_, st, ok := openStore(ctx, stderr)
	if !ok {
		return 1
	}
	defer st.Close()

	items, err := st.ListUnresolvedRecovery(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker worker resolve: %v\n", err)
		return 1
	}
	var item *postgres.RecoveryItem
	for i := range items {
		if items[i].Task.ID == taskID {
			item = &items[i]
			break
		}
	}
	if item == nil {
		fmt.Fprintf(stderr, "llm-broker worker resolve: task %s is not an unresolved ambiguous execution\n", taskID)
		return 1
	}

	switch outcome {
	case "requeue":
		availableAt := time.Now().Add(time.Duration(delay) * time.Second)
		if _, err := st.RequeueForRetry(ctx, taskID, item.AttemptNo, string(domain.ErrorIndeterminate), "operator_requeue", availableAt); err != nil {
			fmt.Fprintf(stderr, "llm-broker worker resolve: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "task %s requeued; eligible again at %s (operator authorized after backend-idle evidence)\n",
			taskID, availableAt.UTC().Format(time.RFC3339))
	case "failed":
		if _, err := st.FinishTaskFailed(ctx, taskID, item.AttemptNo, string(domain.ErrorIndeterminate), "operator_failed"); err != nil {
			fmt.Fprintf(stderr, "llm-broker worker resolve: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "task %s marked failed (operator decision; attempt #%d recorded)\n", taskID, item.AttemptNo)
	}
	return exitOk
}
