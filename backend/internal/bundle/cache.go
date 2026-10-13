package bundle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
)

var (
	// ErrRemoteReferenceLookupFailed means the configured remote branch could not be resolved.
	ErrRemoteReferenceLookupFailed = errors.New("remote reference lookup failed")
	// ErrCloneFailed means a staged source clone failed.
	ErrCloneFailed = errors.New("clone failed")
	// ErrHeadResolutionFailed means a staged clone did not yield a valid HEAD.
	ErrHeadResolutionFailed = errors.New("head resolution failed")
)

type sourceKey struct {
	repository string
	branch     string
}

type cacheEntry struct {
	mu                sync.RWMutex
	checkoutDirectory string
	revision          *string
}

// hide behind an interface for testing purposes
type gitClient interface {
	RemoteRef(context.Context, string, string) (string, error)
	Clone(context.Context, string, string, string) error
	Head(string) (string, error)
}

type goGitClient struct{}

func (goGitClient) RemoteRef(ctx context.Context, repository, branch string) (string, error) {
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{URLs: []string{repository}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{})
	if err != nil {
		return "", err
	}
	wanted := plumbing.NewBranchReferenceName(branch)
	for _, ref := range refs {
		if ref.Name() == wanted {
			return ref.Hash().String(), nil
		}
	}
	return "", errors.New("branch ref not found")
}

func (goGitClient) Clone(ctx context.Context, repository, branch, destination string) error {
	_, err := git.PlainCloneContext(ctx, destination, false, &git.CloneOptions{
		URL:           repository,
		Depth:         1,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
	})
	return err
}

func (goGitClient) Head(destination string) (string, error) {
	repository, err := git.PlainOpen(destination)
	if err != nil {
		return "", err
	}
	head, err := repository.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// Cache holds immutable repository snapshots in its configured storage.
type Cache struct {
	mu      sync.Mutex
	root    string
	entries map[sourceKey]*cacheEntry
	closed  bool
	// tracks work in progress, used in Close to wait for all operations to complete
	work sync.WaitGroup
	git  gitClient
}

// NewCache creates a snapshot cache rooted at root.
func NewCache(root string) (*Cache, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot cache: %w", err)
	}
	return newCache(root, goGitClient{}), nil
}

func newCache(root string, client gitClient) *Cache {
	return &Cache{root: root, entries: make(map[sourceKey]*cacheEntry), git: client}
}

// Refresh stages and atomically publishes one repository branch snapshot.
func (c *Cache) Refresh(ctx context.Context, request RepositoryRefreshRequest) error {
	if err := ValidateCloneArgs(request.Repository, request.Branch); err != nil {
		return fmt.Errorf("%w: invalid source", ErrRepositoryUnavailable)
	}
	if err := c.beginWork(); err != nil {
		return err
	}
	defer c.work.Done()
	entry, err := c.entry(request.Repository, request.Branch)
	if err != nil {
		return err
	}

	entry.mu.RLock()
	localRevision := entry.revision
	entry.mu.RUnlock()

	remoteRevision, err := c.git.RemoteRef(ctx, request.Repository, request.Branch)
	if err != nil {
		if ctxErr := ctx.Err(); errors.Is(err, ctxErr) {
			return ctxErr
		}
		return fmt.Errorf("%w: %w", ErrRepositoryUnavailable, ErrRemoteReferenceLookupFailed)
	}
	if !isCommitSHA(remoteRevision) {
		return fmt.Errorf("%w: %w", ErrRepositoryUnavailable, ErrRemoteReferenceLookupFailed)
	}
	if localRevision != nil && *localRevision == remoteRevision {
		return nil
	}

	entryDir := filepath.Dir(entry.checkoutDirectory)
	if err = os.MkdirAll(entryDir, 0o700); err != nil {
		return fmt.Errorf("%w: create snapshot directory", ErrRepositoryUnavailable)
	}
	staging, err := os.MkdirTemp(entryDir, ".staging-")
	if err != nil {
		return fmt.Errorf("%w: create staging directory", ErrRepositoryUnavailable)
	}
	defer os.RemoveAll(staging)

	if err = c.git.Clone(ctx, request.Repository, request.Branch, staging); err != nil {
		if errors.Is(err, ctx.Err()) {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %w", ErrRepositoryUnavailable, ErrCloneFailed)
	}
	revision, err := c.git.Head(staging)
	if err != nil || !isCommitSHA(revision) {
		return fmt.Errorf("%w: %w", ErrRepositoryUnavailable, ErrHeadResolutionFailed)
	}
	if localRevision != nil && *localRevision == revision {
		return nil
	}
	if err = c.publish(entry, staging, revision); err != nil {
		return fmt.Errorf("%w: publish snapshot", ErrRepositoryUnavailable)
	}
	return nil
}

func (c *Cache) beginWork() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("%w: cache closed", ErrRepositoryUnavailable)
	}
	c.work.Add(1)
	return nil
}

func (c *Cache) entry(repository, branch string) (*cacheEntry, error) {
	key := sourceKey{repository: repository, branch: branch}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("%w: cache closed", ErrRepositoryUnavailable)
	}
	if entry := c.entries[key]; entry != nil {
		return entry, nil
	}
	entry := &cacheEntry{checkoutDirectory: filepath.Join(c.root, snapshotID(repository, branch), "content")}
	if revision, err := c.git.Head(entry.checkoutDirectory); err == nil && isCommitSHA(revision) {
		entry.revision = &revision
	}
	c.entries[key] = entry
	return entry, nil
}

func (c *Cache) publish(entry *cacheEntry, staging, revision string) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()

	old := entry.checkoutDirectory + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(entry.checkoutDirectory, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, entry.checkoutDirectory); err != nil {
		if restoreErr := os.Rename(old, entry.checkoutDirectory); restoreErr != nil && !os.IsNotExist(restoreErr) {
			return fmt.Errorf("publish snapshot error: %w", restoreErr)
		}
		return err
	}
	entry.revision = &revision
	return os.RemoveAll(old)
}

// LoadBundle obtains a validated lab bundle from one immutable source snapshot.
func (c *Cache) LoadBundle(_ context.Context, repository, branch, labPath string) (*Result, error) {
	if err := ValidateCloneArgs(repository, branch); err != nil {
		return nil, fmt.Errorf("%w: invalid source", ErrRepositoryUnavailable)
	}
	if err := ValidateLabPath(labPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}
	entry, err := c.entry(repository, branch)
	if err != nil {
		return nil, err
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	if entry.revision == nil {
		return nil, ErrRepositoryUnavailable
	}
	result, err := LoadFromSnapshot(entry.checkoutDirectory, labPath)
	if err != nil {
		return nil, err
	}
	result.Revision = *entry.revision
	return result, nil
}

// ReadResource obtains a resource and its revision from one immutable source snapshot.
func (c *Cache) ReadResource(_ context.Context, repository, branch, resourcePath string) (*Resource, error) {
	if err := ValidateCloneArgs(repository, branch); err != nil {
		return nil, fmt.Errorf("%w: invalid source", ErrRepositoryUnavailable)
	}
	entry, err := c.entry(repository, branch)
	if err != nil {
		return nil, err
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	if entry.revision == nil {
		return nil, ErrRepositoryUnavailable
	}
	resource, err := ReadResource(entry.checkoutDirectory, resourcePath)
	if err != nil {
		return nil, err
	}
	resource.Revision = *entry.revision
	return resource, nil
}

// Close prevents new work, waits for in-flight refreshes, and preserves snapshots.
func (c *Cache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.work.Wait()
	return nil
}

func snapshotID(repository, branch string) string {
	sum := sha256.Sum256([]byte(repository + "|" + branch))
	return fmt.Sprintf("%x", sum)
}

func isCommitSHA(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	for _, value := range revision {
		if ('0' > value || value > '9') && ('a' > value || value > 'f') {
			return false
		}
	}
	return true
}
