package bundle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const refreshDeadline = 10 * time.Minute

// RefreshEntry identifies one catalog lab whose source snapshot needs refreshing.
type RefreshEntry struct {
	CatalogRef string
	Repository string
	Branch     string
	LabPath    string
}

// RepositoryRefreshRequest identifies one repository branch snapshot to refresh.
type RepositoryRefreshRequest struct {
	Repository string
	Branch     string
}

// LoadEntries loads configured lab sources for one refresh sweep. I.e. it should reload
// the sources every time it is called.
type LoadEntries func(context.Context) ([]RefreshEntry, error)

// RepositoryRefresher refreshes one repository branch snapshot.
type RepositoryRefresher func(context.Context, RepositoryRefreshRequest) error

// RefreshFailureReporter reports an unsuccessful source refresh for one catalog entry.
type RefreshFailureReporter func(context.Context, RefreshEntry, string)

type refreshGroup struct {
	request RepositoryRefreshRequest
	entries []RefreshEntry
}

// RefreshCoordinator refreshes configured source snapshots periodically and on request.
type RefreshCoordinator struct {
	entries     LoadEntries
	refresh     RepositoryRefresher
	report      RefreshFailureReporter
	interval    time.Duration
	concurrency int
	requests    chan struct{}
	ready       atomic.Bool
}

// NewRefreshCoordinator creates a coordinator for periodic and requested source refreshes.
func NewRefreshCoordinator(entries LoadEntries, refresh RepositoryRefresher, report RefreshFailureReporter, interval time.Duration, concurrency int) *RefreshCoordinator {
	if concurrency < 1 {
		concurrency = 1
	}
	return &RefreshCoordinator{
		entries: entries, refresh: refresh, report: report,
		interval: interval, concurrency: concurrency, requests: make(chan struct{}, 1),
	}
}

// IsReady reports whether at least one full refresh sweep has completed.
func (c *RefreshCoordinator) IsReady() bool {
	return c.ready.Load()
}

// RequestRefresh nonblockingly queues a sweep, coalescing pending requests into one.
func (c *RefreshCoordinator) RequestRefresh() {
	select {
	case c.requests <- struct{}{}:
	default:
	}
}

// Refresh performs one grouped source refresh sweep.
func (c *RefreshCoordinator) Refresh(ctx context.Context) error {
	entries, err := c.entries(ctx)
	if err != nil {
		return err
	}
	groups := make(map[string]*refreshGroup)
	var ordered []*refreshGroup
	for _, entry := range entries {
		key := entry.Repository + "\x00" + entry.Branch
		group := groups[key]
		if group == nil {
			group = &refreshGroup{request: RepositoryRefreshRequest{Repository: entry.Repository, Branch: entry.Branch}}
			groups[key], ordered = group, append(ordered, group)
		}
		group.entries = append(group.entries, entry)
	}

	sem := make(chan struct{}, c.concurrency)
	var workers sync.WaitGroup
	for _, group := range ordered {
		workers.Add(1)
		go func(group *refreshGroup) {
			defer workers.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			sourceCtx, cancel := context.WithTimeout(ctx, refreshDeadline)
			defer cancel()
			if err := c.refresh(sourceCtx, group.request); err != nil {
				if ctx.Err() != nil {
					return
				}
				for _, entry := range group.entries {
					if c.report != nil {
						c.report(ctx, entry, refreshFailureReason(err))
					}
				}
			}
		}(group)
	}
	workers.Wait()
	return nil
}

// Run performs an immediate sweep then refreshes at interval or on request without overlapping sweeps.
func (c *RefreshCoordinator) Run(ctx context.Context, onError ...func(error)) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil && len(onError) > 0 && onError[0] != nil {
			onError[0](err)
		}
		c.ready.Store(true)
		discardReadyTicks(ticker.C)

		select {
		case <-ctx.Done():
			return
		case <-c.requests:
		case <-ticker.C:
			// The next sweep also covers any already-pending catalog change.
			select {
			case <-c.requests:
			default:
			}
		}
	}
}

func refreshFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "SourceRefreshTimedOut"
	case errors.Is(err, ErrRemoteReferenceLookupFailed):
		return "RemoteReferenceLookupFailed"
	case errors.Is(err, ErrHeadResolutionFailed):
		return "HeadResolutionFailed"
	default:
		return "CloneFailed"
	}
}

func discardReadyTicks(ticks <-chan time.Time) {
	for {
		select {
		case <-ticks:
		default:
			return
		}
	}
}
