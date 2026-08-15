package model

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func setupImage2AsyncJobTestDB(t *testing.T) string {
	t.Helper()
	require.NotNil(t, DB)
	require.NoError(t, DB.AutoMigrate(&Image2AsyncJob{}))
	// Tests run against the package TestMain database. Use a unique prefix so
	// unrelated model tests and external disposable DB runs remain untouched.
	prefix := "image2-test-" + uuid.NewString()
	t.Cleanup(func() {
		DB.Where("id LIKE ?", prefix+"%").Delete(&Image2AsyncJob{})
	})
	return prefix
}

func image2AsyncTestJob(prefix, suffix string) *Image2AsyncJob {
	now := time.Now().UTC()
	return &Image2AsyncJob{
		ID:             prefix + suffix,
		ScopeKey:       prefix + "-scope-" + suffix,
		IdempotencyKey: "idem-" + suffix,
		UserID:         42,
		Operation:      "generations",
		RequestID:      "request-" + suffix,
		Status:         Image2AsyncJobStatusPending,
		CreatedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
	}
}

func TestImage2AsyncJobCASAllowsExactlyOneCrossInstanceClaim(t *testing.T) {
	prefix := setupImage2AsyncJobTestDB(t)
	job := image2AsyncTestJob(prefix, "claim")
	require.NoError(t, CreateImage2AsyncJob(job))

	now := time.Now().UTC()
	leaseUntil := now.Add(time.Hour)
	owners := []string{"node-a", "node-b"}
	type claimResult struct {
		claimed bool
		err     error
	}
	results := make(chan claimResult, len(owners))
	var wg sync.WaitGroup
	for _, owner := range owners {
		owner := owner
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := ClaimImage2AsyncJob(job.ID, owner, now, leaseUntil)
			results <- claimResult{claimed: claimed, err: err}
		}()
	}
	wg.Wait()
	close(results)
	claims := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.claimed {
			claims++
		}
	}
	require.Equal(t, 1, claims, "exactly one instance may execute the upstream")

	var stored Image2AsyncJob
	require.NoError(t, DB.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, Image2AsyncJobStatusRunning, stored.Status)
	require.Contains(t, []string{"node-a", "node-b"}, stored.LeaseOwner)
}

func TestImage2AsyncJobCompletionIsLeaseScopedAndReadableCrossInstance(t *testing.T) {
	prefix := setupImage2AsyncJobTestDB(t)
	job := image2AsyncTestJob(prefix, "complete")
	require.NoError(t, CreateImage2AsyncJob(job))
	now := time.Now().UTC()
	require.Eventually(t, func() bool {
		claimed, err := ClaimImage2AsyncJob(job.ID, "node-a", now, now.Add(time.Hour))
		return err == nil && claimed
	}, time.Second, time.Millisecond)

	completed, err := CompleteImage2AsyncJob(job.ID, "node-b", 200, []byte(`{"data":[{"url":"safe"}]}`), "", "", now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, completed, "a different node cannot overwrite the result")
	completed, err = CompleteImage2AsyncJob(job.ID, "node-a", 200, []byte(`{"data":[{"url":"safe"}]}`), "", "", now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, completed)

	// This read is intentionally performed through the same DB API a second
	// load-balanced node uses; no process-local map is involved.
	readBack, err := GetImage2AsyncJob(job.ID)
	require.NoError(t, err)
	require.Equal(t, Image2AsyncJobStatusSucceeded, readBack.Status)
	require.Equal(t, `{"data":[{"url":"safe"}]}`, readBack.ResponseBody)
	require.Equal(t, "", readBack.LeaseOwner)
	require.Nil(t, readBack.LeaseUntil)
}

func TestImage2AsyncJobRecoveryFailsStalePendingAndRunningWithoutReplay(t *testing.T) {
	prefix := setupImage2AsyncJobTestDB(t)
	now := time.Now().UTC()
	stale := now.Add(-2 * time.Hour)
	runningLease := now.Add(-time.Second)
	pending := image2AsyncTestJob(prefix, "pending-restart")
	pending.CreatedAt = stale
	running := image2AsyncTestJob(prefix, "running-restart")
	running.Status = Image2AsyncJobStatusRunning
	running.CreatedAt = stale
	running.LeaseOwner = "dead-process"
	running.LeaseUntil = &runningLease
	require.NoError(t, CreateImage2AsyncJob(pending))
	require.NoError(t, CreateImage2AsyncJob(running))

	changed, err := RecoverImage2AsyncJobs(now, now.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Equal(t, 2, changed)
	for _, id := range []string{pending.ID, running.ID} {
		stored, getErr := GetImage2AsyncJob(id)
		require.NoError(t, getErr)
		require.Equal(t, Image2AsyncJobStatusFailed, stored.Status)
		require.Equal(t, 503, stored.HTTPStatus)
		require.Contains(t, stored.ResponseBody, "image2_async_interrupted")
		require.Empty(t, stored.LeaseOwner)
		require.Nil(t, stored.LeaseUntil)
	}
	// A stale job is terminal; no second claim can turn it back into running.
	claimed, err := ClaimImage2AsyncJob(pending.ID, "new-node", now, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestImage2AsyncJobPruneIsBoundedAndRetainsActiveRows(t *testing.T) {
	prefix := setupImage2AsyncJobTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		finished := now.Add(-time.Hour)
		job := image2AsyncTestJob(prefix, "expired-"+string(rune('a'+i)))
		job.Status = Image2AsyncJobStatusSucceeded
		job.CreatedAt = finished
		job.FinishedAt = &finished
		job.ExpiresAt = now.Add(-time.Minute)
		require.NoError(t, CreateImage2AsyncJob(job))
	}
	active := image2AsyncTestJob(prefix, "active")
	active.Status = Image2AsyncJobStatusRunning
	active.ExpiresAt = now.Add(-time.Minute)
	require.NoError(t, CreateImage2AsyncJob(active))

	deleted, err := PruneExpiredImage2AsyncJobs(now, 2)
	require.NoError(t, err)
	require.Equal(t, 2, deleted, "cleanup must obey the bounded batch size")
	var remainingExpired int64
	require.NoError(t, DB.Model(&Image2AsyncJob{}).Where("id LIKE ? AND status = ?", prefix+"expired-%", Image2AsyncJobStatusSucceeded).Count(&remainingExpired).Error)
	require.Equal(t, int64(1), remainingExpired)
	var activeStored Image2AsyncJob
	require.NoError(t, DB.First(&activeStored, "id = ?", active.ID).Error)
	require.Equal(t, Image2AsyncJobStatusRunning, activeStored.Status)
}
