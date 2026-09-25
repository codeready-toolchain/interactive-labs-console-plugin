package bundle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRefreshCoordinatorAcceptsNamedDependencies(t *testing.T) {
	entries := LoadEntries(func(context.Context) ([]RefreshEntry, error) {
		return []RefreshEntry{{Repository: "https://example.test/labs.git", Branch: "main", LabPath: "lab"}}, nil
	})
	var received RepositoryRefreshRequest
	refresher := RepositoryRefresher(func(_ context.Context, request RepositoryRefreshRequest) error {
		received = request
		return nil
	})
	reporter := RefreshFailureReporter(func(context.Context, RefreshEntry, string) {})

	coordinator := NewRefreshCoordinator(entries, refresher, reporter, time.Hour, 1)

	require.NoError(t, coordinator.Refresh(context.Background()))
	assert.Equal(t, RepositoryRefreshRequest{Repository: "https://example.test/labs.git", Branch: "main"}, received)
}

func TestRefreshCoordinatorReturnsCatalogSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "source unavailable", err: errors.New("catalog unavailable")},
		{name: "configuration invalid", err: errors.New("catalog configuration error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refreshes, reports int
			coordinator := NewRefreshCoordinator(
				func(context.Context) ([]RefreshEntry, error) { return nil, tc.err },
				func(context.Context, RepositoryRefreshRequest) error { refreshes++; return nil },
				func(context.Context, RefreshEntry, string) { reports++ },
				time.Hour,
				1,
			)

			err := coordinator.Refresh(context.Background())

			require.ErrorIs(t, err, tc.err)
			assert.Zero(t, refreshes)
			assert.Zero(t, reports)
		})
	}
}

func TestRefreshCoordinatorGroupsEntriesByRepositoryAndBranch(t *testing.T) {
	entries := []RefreshEntry{
		{CatalogRef: "one", Repository: "https://example.test/labs.git", Branch: "main", LabPath: "one"},
		{CatalogRef: "two", Repository: "https://example.test/labs.git", Branch: "main", LabPath: "two"},
		{CatalogRef: "three", Repository: "https://example.test/labs.git", Branch: "release", LabPath: "three"},
		{CatalogRef: "four", Repository: "https://example.test/other.git", Branch: "main", LabPath: "four"},
	}
	calls := make(map[string]struct{})
	var mu sync.Mutex
	coordinator := NewRefreshCoordinator(
		func(context.Context) ([]RefreshEntry, error) { return entries, nil },
		func(_ context.Context, request RepositoryRefreshRequest) error {
			mu.Lock()
			defer mu.Unlock()
			calls[request.Repository+"|"+request.Branch] = struct{}{}
			return nil
		},
		nil,
		time.Hour,
		1,
	)

	require.NoError(t, coordinator.Refresh(context.Background()))
	assert.Equal(t, map[string]struct{}{
		"https://example.test/labs.git|main":    {},
		"https://example.test/labs.git|release": {},
		"https://example.test/other.git|main":   {},
	}, calls)
}

type deadlineCountingContext struct {
	context.Context
	mu    sync.Mutex
	calls int
}

func (c *deadlineCountingContext) Deadline() (time.Time, bool) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Context.Deadline()
}

func (c *deadlineCountingContext) deadlineCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestRefreshCoordinatorBoundsWorkersAndDelaysSourceDeadlines(t *testing.T) {
	ctx := &deadlineCountingContext{Context: context.Background()}
	entries := []RefreshEntry{
		{Repository: "https://example.test/one.git", Branch: "main", LabPath: "one"},
		{Repository: "https://example.test/two.git", Branch: "main", LabPath: "two"},
		{Repository: "https://example.test/three.git", Branch: "main", LabPath: "three"},
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	workers, maximum := 0, 0
	var deadlines []time.Duration
	coordinator := NewRefreshCoordinator(
		func(context.Context) ([]RefreshEntry, error) { return entries, nil },
		func(sourceCtx context.Context, _ RepositoryRefreshRequest) error {
			deadline, ok := sourceCtx.Deadline()
			if !ok {
				return errors.New("source deadline missing")
			}
			mu.Lock()
			workers++
			maximum = max(maximum, workers)
			deadlines = append(deadlines, time.Until(deadline))
			first := len(deadlines) == 1
			mu.Unlock()
			if first {
				close(firstStarted)
				<-releaseFirst
			}
			mu.Lock()
			workers--
			mu.Unlock()
			return nil
		},
		nil,
		time.Hour,
		1,
	)

	done := make(chan error, 1)
	go func() { done <- coordinator.Refresh(ctx) }()
	<-firstStarted
	assert.Equal(t, 1, ctx.deadlineCalls(), "queued sources must not receive deadlines before a slot opens")
	close(releaseFirst)
	require.NoError(t, <-done)

	assert.Equal(t, 3, ctx.deadlineCalls())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, maximum)
	require.Len(t, deadlines, 3)
	for _, remaining := range deadlines {
		assert.LessOrEqual(t, remaining, 10*time.Minute)
		assert.Greater(t, remaining, 9*time.Minute)
	}
}

func TestRefreshCoordinatorReportsEachFailedCatalogEntry(t *testing.T) {
	entries := []RefreshEntry{
		{CatalogRef: "remote-one", Repository: "https://example.test/remote.git", Branch: "main", LabPath: "one"},
		{CatalogRef: "remote-two", Repository: "https://example.test/remote.git", Branch: "main", LabPath: "two"},
		{CatalogRef: "head", Repository: "https://example.test/head.git", Branch: "main", LabPath: "three"},
		{CatalogRef: "clone", Repository: "https://example.test/clone.git", Branch: "main", LabPath: "four"},
	}
	var mu sync.Mutex
	reports := make(map[string]string)
	coordinator := NewRefreshCoordinator(
		func(context.Context) ([]RefreshEntry, error) { return entries, nil },
		func(_ context.Context, request RepositoryRefreshRequest) error {
			switch request.Repository {
			case "https://example.test/remote.git":
				return ErrRemoteReferenceLookupFailed
			case "https://example.test/head.git":
				return ErrHeadResolutionFailed
			default:
				return errors.New("clone failed")
			}
		},
		func(_ context.Context, entry RefreshEntry, reason string) {
			mu.Lock()
			defer mu.Unlock()
			reports[entry.CatalogRef] = reason
		},
		time.Hour,
		1,
	)

	require.NoError(t, coordinator.Refresh(context.Background()))
	assert.Equal(t, map[string]string{
		"remote-one": "RemoteReferenceLookupFailed",
		"remote-two": "RemoteReferenceLookupFailed",
		"head":       "HeadResolutionFailed",
		"clone":      "CloneFailed",
	}, reports)
}

func TestRefreshCoordinatorCancellationReporting(t *testing.T) {
	t.Run("does not report parent cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan struct{})
		done := make(chan error, 1)
		var reports atomic.Int32
		coordinator := NewRefreshCoordinator(
			func(context.Context) ([]RefreshEntry, error) {
				return []RefreshEntry{{Repository: "https://example.test/labs.git", Branch: "main", LabPath: "lab"}}, nil
			},
			func(sourceCtx context.Context, _ RepositoryRefreshRequest) error {
				close(started)
				<-sourceCtx.Done()
				return sourceCtx.Err()
			},
			func(context.Context, RefreshEntry, string) { reports.Add(1) },
			time.Hour,
			1,
		)

		go func() { done <- coordinator.Refresh(ctx) }()
		<-started
		cancel()
		require.NoError(t, <-done)
		assert.Zero(t, reports.Load())
	})

	t.Run("classifies source deadline separately", func(t *testing.T) {
		assert.Equal(t, "SourceRefreshTimedOut", refreshFailureReason(context.DeadlineExceeded))
	})
}

func TestRefreshCoordinatorRunSkipsQueuedTicksAndDrainsActiveSweep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		firstStarted := make(chan struct{})
		firstFinished := make(chan struct{})
		secondStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		var sweeps atomic.Int32
		coordinator := NewRefreshCoordinator(
			func(context.Context) ([]RefreshEntry, error) {
				return []RefreshEntry{{Repository: "https://example.test/labs.git", Branch: "main", LabPath: "lab"}}, nil
			},
			func(ctx context.Context, _ RepositoryRefreshRequest) error {
				if sweeps.Add(1) == 1 {
					close(firstStarted)
					<-releaseFirst
					close(firstFinished)
					return nil
				}
				close(secondStarted)
				<-ctx.Done()
				return ctx.Err()
			},
			nil,
			time.Minute,
			2,
		)
		done := make(chan struct{})
		go func() { coordinator.Run(ctx); close(done) }()

		<-firstStarted
		time.Sleep(3 * time.Minute)
		close(releaseFirst)
		<-firstFinished
		synctest.Wait()
		assert.Equal(t, int32(1), sweeps.Load(), "ticks queued during a sweep must be discarded")

		time.Sleep(time.Minute)
		<-secondStarted
		cancel()
		synctest.Wait()
		<-done
		assert.Equal(t, int32(2), sweeps.Load())
	})
}
