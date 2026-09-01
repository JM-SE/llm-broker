package config

import (
	"strings"
	"testing"
	"time"
)

func h02Config() *Config {
	return &Config{
		Version:   Version,
		API:       APIConfig{Listen: "127.0.0.1:18080", BodyLimitBytes: DefaultBodyLimitBytes},
		Database:  DatabaseConfig{URLEnv: "H02_DATABASE_URL", ProvisionerURLEnv: "H02_PROVISIONER_URL", MigrationURLEnv: "H02_MIGRATION_URL", MaxConnections: DefaultMaxConnections, ConnectTimeoutSeconds: DefaultConnectTimeout},
		Retention: RetentionConfig{Payload: Duration{Duration: time.Hour}, Result: Duration{Duration: 24 * time.Hour}, Metadata: Duration{Duration: 48 * time.Hour}},
		Backends: map[string]BackendConfig{
			"backend-alpha": {Type: AdapterFake},
			"backend-beta":  {Type: AdapterFake, Features: []string{"structured_output"}},
		},
		Capabilities: map[string]Capability{
			"capability-alpha": {Backend: "backend-alpha"},
			"capability-beta":  {Backend: "backend-beta"},
		},
		Workloads: map[string]WorkloadPolicy{
			"workload-alpha": {PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1, MaxOutputTokens: 2048, AllowedGeneration: map[string]GenerationConstraint{}, AllowedCapabilities: []string{"capability-alpha"}, WorkloadTimeoutSeconds: 300},
			"workload-beta":  {PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1, MaxOutputTokens: 2048, AllowedGeneration: map[string]GenerationConstraint{}, AllowedCapabilities: []string{"capability-beta"}, WorkloadTimeoutSeconds: 300},
		},
	}
}

func TestH02DualRouteConfiguration(t *testing.T) {
	c := h02Config()
	if err := c.Validate(); err != nil {
		t.Fatalf("valid dual-route config rejected: %v", err)
	}
	if got := c.Topology().Capabilities; got["capability-alpha"] != "backend-alpha" || got["capability-beta"] != "backend-beta" {
		t.Fatalf("capability topology = %#v", got)
	}
	if got := c.Topology().Backends["backend-beta"].Features; len(got) != 1 || got[0] != "structured_output" {
		t.Fatalf("backend-beta features = %#v", got)
	}
}

func TestH02UnknownBackendReferenceIsRejectedWithoutEchoingValue(t *testing.T) {
	c := h02Config()
	beta := c.Backends["backend-beta"]
	beta.CredentialEnv = "H02_SECRET_SENTINEL"
	c.Backends["backend-beta"] = beta
	c.Capabilities["capability-beta"] = Capability{Backend: "backend-missing"}
	err := c.Validate()
	if err == nil {
		t.Fatal("unknown backend reference was accepted")
	}
	if strings.Contains(err.Error(), "H02_SECRET_SENTINEL") {
		t.Fatalf("validation error echoed sensitive-looking value: %v", err)
	}
}

func TestH02RouteMappingChangesTopologyProjectionAndDigest(t *testing.T) {
	a := h02Config()
	b := h02Config()
	b.Capabilities["capability-alpha"] = Capability{Backend: "backend-beta"}
	if a.Topology().Capabilities["capability-alpha"] == b.Topology().Capabilities["capability-alpha"] {
		t.Fatal("route mapping did not change topology projection")
	}
	if a.TopologyDigest() == b.TopologyDigest() {
		t.Fatal("route mapping did not change topology digest")
	}
}
