package postgres

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestH00C1AdvisoryCallersUseCanonicalDomainsAndDirectSlugs(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate H00 C1 advisory caller test")
	}
	dir := filepath.Dir(thisFile)
	read := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read production caller %s: %v", name, err)
		}
		return string(body)
	}

	submissions := read("submissions.go")
	for _, want := range []string{
		"policy.PolicyLockKey(p.WorkloadSlug)",
		"policy.ClientLockKey(p.ClientID)",
		"policy.GrantAdmissionLockKey(p.ClientID, p.WorkloadSlug)",
		"readSubmissionPolicy(ctx, tx, p.WorkloadSlug)",
	} {
		if !strings.Contains(submissions, want) {
			t.Fatalf("submissions.go missing canonical direct-slug caller %q", want)
		}
	}
	if strings.Contains(submissions, "GlobalPolicyApplyLockKey") {
		t.Fatal("submission path acquired the global apply mutex")
	}

	preflight := read("preflight.go")
	for _, want := range []string{
		"policy.PolicyLockKey(p.WorkloadSlug)",
		"policy.ClientLockKey(p.ClientID)",
		"policy.GrantAdmissionLockKey(p.ClientID, p.WorkloadSlug)",
		"readSubmissionPolicy(ctx, tx, p.WorkloadSlug)",
	} {
		if !strings.Contains(preflight, want) {
			t.Fatalf("preflight.go missing canonical direct-slug caller %q", want)
		}
	}
	if strings.Contains(preflight, "GlobalPolicyApplyLockKey") {
		t.Fatal("preflight path acquired the global apply mutex")
	}

	clients := read("clients.go")
	for _, want := range []string{
		"policy.ClientNameLockKey(p.Name)",
		"policy.PolicyLockKey(slug)",
		"policy.GrantAdmissionLockKey(clientID, slug)",
	} {
		if !strings.Contains(clients, want) {
			t.Fatalf("clients.go missing canonical provisioner caller %q", want)
		}
	}
	if strings.Contains(clients, "GlobalPolicyApplyLockKey") {
		t.Fatal("entity mutation path acquired the global apply mutex")
	}

	policies := read("policies.go")
	global := "policy.GlobalPolicyApplyLockKey()"
	if strings.Count(policies, global) != 2 {
		t.Fatalf("policy inspection/apply global helper count = %d, want 2", strings.Count(policies, global))
	}
	if !strings.Contains(policies, "desired = sortedWorkloads(desired)") ||
		!strings.Contains(policies, "policy.PolicyLockKey(workload.Name)") {
		t.Fatal("policy apply does not centralize lexical workload locking")
	}
	globalAt := strings.Index(policies, global)
	entityAt := strings.Index(policies, "policy.PolicyLockKey(workload.Name)")
	if globalAt < 0 || entityAt < 0 || globalAt > entityAt {
		t.Fatal("policy apply acquires an entity lock before the global apply mutex")
	}

	for _, name := range []string{"submissions.go", "preflight.go", "policies.go"} {
		body := read(name)
		if strings.Contains(body, "ClientNameLockKey") {
			t.Fatalf("pre-ID client-name namespace combined with authorization locks in %s", name)
		}
	}
}
