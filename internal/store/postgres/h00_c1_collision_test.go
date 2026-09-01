package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	_ "unsafe"
)

// The production policy package deliberately keeps its digest hook
// package-private. This test-only linkname is the smallest seam that lets the
// PostgreSQL matrix force two distinct policy slugs to the same policy-domain
// key without adding an exported production bypass.
//
//go:linkname h00C1PolicyAdvisoryDigest github.com/JM-SE/llm-broker/internal/policy.advisoryKeyDigest
var h00C1PolicyAdvisoryDigest func(string) [32]byte

func h00C1InstallForcedPolicyCollision(t *testing.T, slugs ...string) {
	t.Helper()
	targets := make(map[string]struct{}, len(slugs))
	for _, slug := range slugs {
		targets[slug] = struct{}{}
	}
	original := h00C1PolicyAdvisoryDigest
	var fixed [32]byte
	for i := range fixed {
		fixed[i] = 0xa5
	}
	h00C1PolicyAdvisoryDigest = func(identity string) [32]byte {
		if _, ok := targets[identity]; ok {
			return fixed
		}
		return sha256.Sum256([]byte(identity))
	}
	t.Cleanup(func() { h00C1PolicyAdvisoryDigest = original })
}

func TestH00C1ForcedSameDomainCollisionAuthorizationPredicateIsolation(t *testing.T) {
	deniedClient, deniedWorkload, deniedParams := h00C1StoreFixture(t, 10)
	allowedClient, allowedWorkload, allowedParams := h00C1StoreFixture(t, 10)
	if err := provisionerStore.RevokeWorkloadBySlug(context.Background(), deniedClient.ID, deniedWorkload.Name); err != nil {
		t.Fatalf("remove denied fixture grant: %v", err)
	}
	h00C1InstallForcedPolicyCollision(t, deniedWorkload.Name, allowedWorkload.Name)
	deniedBefore := h00C1SnapshotFor(t, deniedClient.ID, deniedWorkload.ID)
	allowedBefore := h00C1SnapshotFor(t, allowedClient.ID, allowedWorkload.ID)

	key := h00C1ForcedPolicyCollisionKey(t, deniedWorkload.Name)
	barrier := newH00LockBarrier(t, key)
	deniedDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := rtStore.SubmitTask(ctx, deniedParams)
		deniedDone <- err
	}()
	waitForH00C1AdvisoryState(t, key, 1, 1)

	// The forced collision is in the denied workload's policy domain only. A
	// separate client/workload queues on that same key (over-serialization),
	// then must still make its own authorization decision after release.
	var allowed SubmissionResult
	allowedDone := make(chan struct {
		result SubmissionResult
		err    error
	}, 1)
	go func() {
		allowedCtx, cancelAllowed := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelAllowed()
		result, err := rtStore.SubmitTask(allowedCtx, allowedParams)
		allowedDone <- struct {
			result SubmissionResult
			err    error
		}{result, err}
	}()
	waitForH00C1AdvisoryState(t, key, 1, 2)
	barrier.release(t)
	select {
	case outcome := <-deniedDone:
		if !errors.Is(outcome, ErrSubmissionForbidden) {
			t.Fatalf("denied collision submission = %v, want forbidden after release", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("denied collision submission did not finish after release")
	}
	select {
	case outcome := <-allowedDone:
		if outcome.err != nil || !outcome.result.Created {
			t.Fatalf("isolated authorized submission = %+v, err=%v; want one created task", outcome.result, outcome.err)
		}
		allowed = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("isolated authorized submission did not finish after release")
	}

	deniedAfter := h00C1SnapshotFor(t, deniedClient.ID, deniedWorkload.ID)
	allowedAfter := h00C1SnapshotFor(t, allowedClient.ID, allowedWorkload.ID)
	h00C1AssertChangedTables(t, deniedBefore, deniedAfter)
	h00C1AssertChangedTables(t, allowedBefore, allowedAfter, "tasks", "payloads")
	if rows := h00C1TaskRows(t, deniedClient.ID, deniedWorkload.ID); len(rows) != 0 {
		t.Fatalf("denied collision created task rows: %+v", rows)
	}
	if rows := h00C1TaskRows(t, allowedClient.ID, allowedWorkload.ID); len(rows) != 1 || rows[0].ID != allowed.Task.ID {
		t.Fatalf("isolated authorized collision task rows = %+v", rows)
	}
}

func TestH00C1ForcedSameDomainCollisionAdmissionPredicateIsolation(t *testing.T) {
	limitedClient, limitedWorkload, firstParams := h00C1StoreFixture(t, 1)
	first, err := rtStore.SubmitTask(context.Background(), firstParams)
	if err != nil || !first.Created {
		t.Fatalf("seed admission-limited task = %+v, err=%v", first, err)
	}
	limitedSecond := firstParams
	limitedSecond.IdempotencyKey = uniqueName("h00-c1-limited-second")
	limitedSecond.RequestHash = testHash(0x55)
	otherClient, otherWorkload, otherParams := h00C1StoreFixture(t, 10)
	h00C1InstallForcedPolicyCollision(t, limitedWorkload.Name, otherWorkload.Name)
	limitedBefore := h00C1SnapshotFor(t, limitedClient.ID, limitedWorkload.ID)
	otherBefore := h00C1SnapshotFor(t, otherClient.ID, otherWorkload.ID)

	key := h00C1ForcedPolicyCollisionKey(t, limitedWorkload.Name)
	barrier := newH00LockBarrier(t, key)
	limitedDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := rtStore.SubmitTask(ctx, limitedSecond)
		limitedDone <- err
	}()
	waitForH00C1AdvisoryState(t, key, 1, 1)

	otherDone := make(chan struct {
		result SubmissionResult
		err    error
	}, 1)
	go func() {
		otherCtx, cancelOther := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelOther()
		result, err := rtStore.SubmitTask(otherCtx, otherParams)
		otherDone <- struct {
			result SubmissionResult
			err    error
		}{result, err}
	}()
	waitForH00C1AdvisoryState(t, key, 1, 2)
	barrier.release(t)
	var other SubmissionResult
	select {
	case err := <-limitedDone:
		if !errors.Is(err, ErrSubmissionAdmission) {
			t.Fatalf("limited collision submission = %v, want admission after release", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("limited collision submission did not finish after release")
	}
	select {
	case outcome := <-otherDone:
		if outcome.err != nil || !outcome.result.Created {
			t.Fatalf("isolated admission control submission = %+v, err=%v", outcome.result, outcome.err)
		}
		other = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("isolated admission control submission did not finish after release")
	}

	limitedAfter := h00C1SnapshotFor(t, limitedClient.ID, limitedWorkload.ID)
	otherAfter := h00C1SnapshotFor(t, otherClient.ID, otherWorkload.ID)
	h00C1AssertChangedTables(t, limitedBefore, limitedAfter)
	h00C1AssertChangedTables(t, otherBefore, otherAfter, "tasks", "payloads")
	if active, err := rtStore.CountActiveTasks(context.Background(), limitedClient.ID, limitedWorkload.ID); err != nil || active != 1 {
		t.Fatalf("limited collision active count = %d, err=%v; want one", active, err)
	}
	if rows := h00C1TaskRows(t, limitedClient.ID, limitedWorkload.ID); len(rows) != 1 || rows[0].ID != first.Task.ID {
		t.Fatalf("limited collision task rows = %+v", rows)
	}
	if rows := h00C1TaskRows(t, otherClient.ID, otherWorkload.ID); len(rows) != 1 || rows[0].ID != other.Task.ID {
		t.Fatalf("isolated other task rows = %+v", rows)
	}
}

func TestH00C1ForcedSameDomainCollisionMutationTargetIsolation(t *testing.T) {
	blockedClient, blockedWorkload, _ := h00C1StoreFixture(t, 10)
	otherClient, otherWorkload, _ := h00C1StoreFixture(t, 10)
	h00C1InstallForcedPolicyCollision(t, blockedWorkload.Name, otherWorkload.Name)
	blockedBefore := h00C1SnapshotFor(t, blockedClient.ID, blockedWorkload.ID)
	otherBefore := h00C1SnapshotFor(t, otherClient.ID, otherWorkload.ID)

	key := h00C1ForcedPolicyCollisionKey(t, blockedWorkload.Name)
	barrier := newH00LockBarrier(t, key)
	blockedDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		blockedDone <- provisionerStore.RevokeWorkloadBySlug(ctx, blockedClient.ID, blockedWorkload.Name)
	}()
	waitForH00C1AdvisoryState(t, key, 1, 1)

	// A same-domain wait on blockedWorkload must not retarget another actual
	// grant row. The unrelated mutation commits while the collision is held.
	otherDone := make(chan error, 1)
	go func() {
		otherCtx, cancelOther := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelOther()
		otherDone <- provisionerStore.RevokeWorkloadBySlug(otherCtx, otherClient.ID, otherWorkload.Name)
	}()
	waitForH00C1AdvisoryState(t, key, 1, 2)
	barrier.release(t)
	select {
	case err := <-blockedDone:
		if err != nil {
			t.Fatalf("blocked mutation after collision release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked mutation did not finish after release")
	}
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatalf("isolated mutation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("isolated mutation did not finish after release")
	}

	blockedAfter := h00C1SnapshotFor(t, blockedClient.ID, blockedWorkload.ID)
	otherAfter := h00C1SnapshotFor(t, otherClient.ID, otherWorkload.ID)
	h00C1AssertChangedTables(t, blockedBefore, blockedAfter, "grants")
	h00C1AssertChangedTables(t, otherBefore, otherAfter, "grants")
	for name, fixture := range map[string]struct {
		client   Client
		workload WorkloadPolicy
	}{
		"blocked": {blockedClient, blockedWorkload},
		"other":   {otherClient, otherWorkload},
	} {
		granted, err := provisionerStore.ClientHasWorkload(context.Background(), fixture.client.ID, fixture.workload.ID)
		if err != nil {
			t.Fatalf("read %s final grant: %v", name, err)
		}
		if granted {
			t.Fatalf("%s grant remained after targeted mutation", name)
		}
	}
}
