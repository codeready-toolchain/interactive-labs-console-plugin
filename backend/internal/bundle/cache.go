package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	done        chan struct{}
	err         error
}

// Cache holds immutable repository snapshots in its configured storage.
type Cache struct {
	mu      sync.Mutex
	root    string
	entries map[sourceKey]*cacheEntry
	closed  bool
	cloning sync.WaitGroup
	clone   func(context.Context, string, string, string) error
}

// NewCache creates a snapshot cache rooted at root.
func NewCache(root string) (*Cache, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot cache: %w", err)
	}
	return newCache(root, cloneSnapshot), nil
}

func newCache(root string, clone func(context.Context, string, string, string) error) *Cache {
	return &Cache{root: root, entries: make(map[sourceKey]*cacheEntry), clone: clone}
}

// Load obtains a validated lab from one immutable source snapshot.
func (c *Cache) Load(ctx context.Context, repository, branch, labPath string) (*Result, error) {
	if err := ValidateCloneArgs(repository, branch); err != nil {
		return nil, fmt.Errorf("%w: invalid source", ErrRepositoryUnavailable)
	}
	if err := ValidateLabPath(labPath); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLabInvalid, err)
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

	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	return LoadFromSnapshot(entry.destination, labPath)
}

func (c *Cache) populate(key sourceKey, entry *cacheEntry) {
	defer c.cloning.Done()
	defer close(entry.done)

	if info, err := os.Stat(entry.destination); err == nil {
		if !info.IsDir() {
			entry.err = fmt.Errorf("%w: snapshot content is not a directory", ErrRepositoryUnavailable)
		}
		return
	} else if !os.IsNotExist(err) {
		entry.err = fmt.Errorf("%w: inspect snapshot", ErrRepositoryUnavailable)
		return
	}

	entryDir := filepath.Dir(entry.destination)
	if err := os.MkdirAll(entryDir, 0o700); err != nil {
		entry.err = fmt.Errorf("%w: create snapshot directory", ErrRepositoryUnavailable)
		return
	}
	temporary, err := os.MkdirTemp(entryDir, ".content-")
	if err == nil {
		err = os.Remove(temporary)
	}
	if err == nil {
		err = c.clone(context.Background(), key.repository, key.branch, temporary)
	}
	if err == nil {
		err = os.Rename(temporary, entry.destination)
	}
	if err == nil {
		return
	}

	_ = os.RemoveAll(temporary)
	entry.err = fmt.Errorf("%w: clone failed", ErrRepositoryUnavailable)
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
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
	type canonical struct {
		Branch     string `json:"branch"`
		Repository string `json:"repository"`
	}
	b, _ := json.Marshal(canonical{Branch: branch, Repository: repository})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
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
	cmd := exec.CommandContext(ctx, "git", cloneArgs(repository, branch, destination)...)
	cmd.Env = gitEnv(nil)
	if _, err := cmd.CombinedOutput(); err != nil {
		return err
	}
	return nil
}
