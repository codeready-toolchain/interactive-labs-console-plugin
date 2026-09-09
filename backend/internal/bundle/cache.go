package bundle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type sourceKey struct {
	repository string
	branch     string
}

type cacheEntry struct {
	destination string
	revision    string
	done        chan struct{}
	err         error
}

// Cache holds immutable repository snapshots in its configured storage.
type Cache struct {
	mu              sync.Mutex
	root            string
	entries         map[sourceKey]*cacheEntry
	closed          bool
	cloning         sync.WaitGroup
	clone           func(context.Context, string, string, string) error
	getRepoRevision func(string) (string, error)
}

// NewCache creates a snapshot cache rooted at root.
func NewCache(root string) (*Cache, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot cache: %w", err)
	}
	return newCache(root, cloneSnapshot), nil
}

func newCache(root string, clone func(context.Context, string, string, string) error) *Cache {
	return &Cache{root: root, entries: make(map[sourceKey]*cacheEntry), clone: clone, getRepoRevision: snapshotRevision}
}

// Load obtains a validated lab from one immutable source snapshot.
func (c *Cache) Load(ctx context.Context, repository, branch, labPath string) (*Result, error) {
	entry, err := c.entry(ctx, repository, branch, labPath)
	if err != nil {
		return nil, err
	}
	return LoadFromSnapshot(entry.destination, labPath)
}

// CommitHash obtains the immutable snapshot commit hash without reading a resource.
func (c *Cache) CommitHash(ctx context.Context, repository, branch, labPath string) (string, error) {
	entry, err := c.entry(ctx, repository, branch, labPath)
	if err != nil {
		return "", err
	}
	return entry.revision, nil
}

// Read obtains a resource from the same immutable snapshot as Load.
func (c *Cache) Read(ctx context.Context, repository, branch, labPath, resourcePath string) (*Resource, error) {
	entry, err := c.entry(ctx, repository, branch, labPath)
	if err != nil {
		return nil, err
	}
	if _, err := LoadFromSnapshot(entry.destination, labPath); err != nil {
		return nil, err
	}
	return ReadResource(entry.destination, resourcePath)
}

func (c *Cache) entry(ctx context.Context, repository, branch, labPath string) (*cacheEntry, error) {
	if err := ValidateCloneArgs(repository, branch); err != nil {
		return nil, fmt.Errorf("%w: invalid source", ErrRepositoryUnavailable)
	}
	if err := ValidateLabPath(labPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}

	key := sourceKey{repository: repository, branch: branch}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: cache closed", ErrRepositoryUnavailable)
	}
	entry, found := c.entries[key]
	if !found {
		entry = &cacheEntry{
			destination: filepath.Join(c.root, snapshotID(repository, branch), "content"),
			done:        make(chan struct{}),
		}
		c.entries[key] = entry
		c.cloning.Add(1)
		go c.populate(key, entry)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.done:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return entry, nil
}

func (c *Cache) populate(key sourceKey, entry *cacheEntry) {
	defer c.cloning.Done()
	revision, err := c.createSnapshot(key, entry.destination)
	if err == nil {
		entry.revision = revision
	}
	c.finish(key, entry, err)
}

func (c *Cache) finish(key sourceKey, entry *cacheEntry, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry.err = err
	if err != nil && c.entries[key] == entry {
		delete(c.entries, key)
	}
	close(entry.done)
}

func (c *Cache) createSnapshot(key sourceKey, destination string) (string, error) {
	if info, err := os.Stat(destination); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%w: snapshot content is not a directory", ErrRepositoryUnavailable)
		}
		revision, resolveErr := c.resolveRevision(destination)
		if resolveErr != nil {
			return "", fmt.Errorf("%w: snapshot revision", ErrRepositoryUnavailable)
		}
		return revision, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("%w: inspect snapshot", ErrRepositoryUnavailable)
	}

	entryDir := filepath.Dir(destination)
	if err := os.MkdirAll(entryDir, 0o700); err != nil {
		return "", fmt.Errorf("%w: create snapshot directory", ErrRepositoryUnavailable)
	}
	// create a temp dir to clone into and then atomically rename it to the expected destination
	temporary, err := os.MkdirTemp(entryDir, ".content-")
	if err == nil {
		err = c.clone(context.Background(), key.repository, key.branch, temporary)
	}
	var revision string
	if err == nil {
		revision, err = c.resolveRevision(temporary)
	}
	if err == nil {
		err = os.Rename(temporary, destination)
	}
	if err == nil {
		return revision, nil
	}

	_ = os.RemoveAll(temporary)
	return "", fmt.Errorf("%w: clone failed", ErrRepositoryUnavailable)
}

func (c *Cache) resolveRevision(destination string) (string, error) {
	revision, err := c.getRepoRevision(destination)
	if err != nil || !isCommitSHA(revision) {
		return "", errors.New("invalid git revision")
	}
	return revision, nil
}

// Close waits for in-flight clones and preserves the cache root and snapshots.
func (c *Cache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.cloning.Wait()
	return nil
}

func snapshotID(repository, branch string) string {
	hasher := sha256.New()
	hasher.Write([]byte(repository))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(branch))
	// this approach borrowed from sha256.Sum256()
	var sum [sha256.Size]byte
	hasher.Sum(sum[:0])
	return fmt.Sprintf("%x", sum)
}

func snapshotRevision(destination string) (string, error) {
	cmd := exec.CommandContext(context.TODO(), "git", "-C", destination, "rev-parse", "HEAD") // #nosec G204 -- destination is cache controlled
	cmd.Env = gitEnv(nil)
	revision, err := cmd.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(revision))
	if !isCommitSHA(value) {
		return "", errors.New("invalid git revision")
	}
	return value, nil
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

func cloneArgs(repository, branch, destination string) []string {
	return []string{"clone", "--depth", "1", "--single-branch", "--branch", branch, "--", repository, destination}
}

func gitEnv(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base)+1)
	for _, value := range base {
		if !strings.HasPrefix(value, "GIT_TERMINAL_PROMPT=") {
			out = append(out, value)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

func cloneSnapshot(ctx context.Context, repository, branch, destination string) error {
	cmd := exec.CommandContext(ctx, "git", cloneArgs(repository, branch, destination)...) // #nosec G204 -- these args are controlled and safe
	cmd.Env = gitEnv(nil)
	if _, err := cmd.CombinedOutput(); err != nil {
		return err
	}
	return nil
}
