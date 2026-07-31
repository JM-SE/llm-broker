package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestClientCRUD(t *testing.T) {
	ctx := context.Background()
	c, err := adminStore.CreateClient(ctx, CreateClientParams{
		Name:                 "crud-client",
		CredentialIdentifier: "crudclient01",
		CredentialDigest:     testHash('c'),
		Enabled:              true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := adminStore.GetClientByName(ctx, "crud-client")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got.ID != c.ID || !bytes.Equal(got.CredentialDigest, testHash('c')) || !got.Enabled {
		t.Errorf("client mismatch: %+v", got)
	}
	byID, err := adminStore.GetClientByIdentifier(ctx, "crudclient01")
	if err != nil || byID.ID != c.ID {
		t.Errorf("get by identifier: %+v, %v", byID, err)
	}

	if err := adminStore.SetClientEnabled(ctx, c.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err = adminStore.GetClientByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.Enabled {
		t.Error("client still enabled")
	}

	// Duplicate name conflicts.
	_, err = adminStore.CreateClient(ctx, CreateClientParams{
		Name:                 "crud-client",
		CredentialIdentifier: "crudclient02",
		CredentialDigest:     testHash('d'),
		Enabled:              true,
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate client: want ErrConflict, got %v", err)
	}
}

func TestWorkloadPolicyUpsert(t *testing.T) {
	ctx := context.Background()
	policy := mustUpsertPolicy(t, adminStore, "upsert-workload")
	got, err := adminStore.GetWorkloadPolicyByName(ctx, policy.Name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PriorityClass != "normal" || got.MaxAttempts != 3 {
		t.Errorf("policy mismatch: %+v", got)
	}

	// Upserting the same name replaces the policy (same row id).
	got.PriorityClass = "high"
	got.MaxOutputTokens = 4096
	updated, err := adminStore.UpsertWorkloadPolicy(ctx, got)
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if updated.ID != policy.ID {
		t.Errorf("upsert changed id: %d -> %d", policy.ID, updated.ID)
	}
	if updated.PriorityClass != "high" || updated.MaxOutputTokens != 4096 {
		t.Errorf("upsert did not apply changes: %+v", updated)
	}
}

func TestWorkloadGrants(t *testing.T) {
	ctx := context.Background()
	client := mustCreateClient(t, adminStore, "grant-client")
	policy := mustUpsertPolicy(t, adminStore, "grant-workload")

	has, err := adminStore.ClientHasWorkload(ctx, client.ID, policy.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if has {
		t.Fatal("grant present before GrantWorkload")
	}

	if err := adminStore.GrantWorkload(ctx, client.ID, policy.ID); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Granting twice is idempotent.
	if err := adminStore.GrantWorkload(ctx, client.ID, policy.ID); err != nil {
		t.Fatalf("grant again: %v", err)
	}
	has, err = adminStore.ClientHasWorkload(ctx, client.ID, policy.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !has {
		t.Fatal("grant not visible")
	}

	if err := adminStore.RevokeWorkload(ctx, client.ID, policy.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	has, err = adminStore.ClientHasWorkload(ctx, client.ID, policy.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if has {
		t.Fatal("grant still visible after revoke")
	}
}
