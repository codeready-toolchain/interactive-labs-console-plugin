package bundle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observingContext struct {
	context.Context
	waiting chan<- struct{}
	once    sync.Once
}

func (c *observingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func writeSnapshotLab(root, labPath string) error {
	lab := filepath.Join(root, filepath.FromSlash(labPath))
	if err := os.MkdirAll(filepath.Join(lab, "content"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(lab, "lab.yaml"),
		[]byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: test
  displayName: Test
spec: {}`), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(lab, "content", "intro.adoc"), []byte("= Test"), 0o600)
}

func testCache(root string, clone func(context.Context, string, string, string) error) *Cache {
	cache := newCache(root, clone)
	cache.getRepoRevision = func(string) (string, error) {
		return "0000000000000000000000000000000000000000", nil
	}
	return cache
}

func TestCache(t *testing.T) {
	t.Run("reuses successful snapshot by repository and branch", func(t *testing.T) {
		root := t.TempDir()
		var mu sync.Mutex
		clones := 0
		cache := testCache(root, func(_ context.Context, _, _, destination string) error {
			mu.Lock()
			clones++
			mu.Unlock()
			if err := writeSnapshotLab(destination, "labs/one"); err != nil {
				return err
			}
			return writeSnapshotLab(destination, "labs/two")
		})

		for _, labPath := range []string{"labs/one", "labs/two", "labs/one"} {
			result, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", labPath)
			require.NoError(t, err)
			assert.Contains(t, result.Pages, "intro.adoc")
		}
		mu.Lock()
		assert.Equal(t, 1, clones)
		mu.Unlock()
	})

	t.Run("caches revision in memory without snapshot metadata", func(t *testing.T) {
		const (
			repository = "https://example.test/labs.git"
			branch     = "main"
			commitHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		)
		clones, revisions := 0, 0
		cache := newCache(t.TempDir(), func(_ context.Context, _, _, destination string) error {
			clones++
			return writeSnapshotLab(destination, "lab")
		})
		cache.getRepoRevision = func(string) (string, error) {
			revisions++
			return commitHash, nil
		}

		for range 2 {
			resource, err := cache.Read(context.Background(), repository, branch, "lab", "lab/content/intro.adoc")
			require.NoError(t, err)
			assert.Equal(t, []byte("= Test"), resource.Bytes)
		}
		assert.Equal(t, 1, clones)
		assert.Equal(t, 1, revisions)
		_, err := os.Stat(filepath.Join(cache.root, snapshotID(repository, branch), "snapshot.json"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("resolves revision once for existing checkout", func(t *testing.T) {
		const (
			repository = "https://example.test/labs.git"
			branch     = "main"
			commitHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		)
		root := t.TempDir()
		destination := filepath.Join(root, snapshotID(repository, branch), "content")
		require.NoError(t, writeSnapshotLab(destination, "lab"))

		revisions := 0
		cache := newCache(root, func(context.Context, string, string, string) error {
			return errors.New("must not clone")
		})
		cache.getRepoRevision = func(gotDestination string) (string, error) {
			assert.Equal(t, destination, gotDestination)
			revisions++
			return commitHash, nil
		}

		_, err := cache.Load(context.Background(), repository, branch, "lab")
		require.NoError(t, err)
		for range 2 {
			resource, err := cache.Read(context.Background(), repository, branch, "lab", "lab/content/intro.adoc")
			require.NoError(t, err)
			assert.Equal(t, []byte("= Test"), resource.Bytes)
		}
		assert.Equal(t, 1, revisions)
	})

	t.Run("separates repository and branch", func(t *testing.T) {
		clones := 0
		cache := testCache(t.TempDir(), func(_ context.Context, _, _, destination string) error {
			clones++
			return writeSnapshotLab(destination, "lab")
		})
		for _, source := range [][2]string{{"https://example.test/one.git", "main"}, {"https://example.test/two.git", "main"}, {"https://example.test/one.git", "next"}} {
			_, err := cache.Load(context.Background(), source[0], source[1], "lab")
			require.NoError(t, err)
		}
		assert.Equal(t, 3, clones)
	})

	t.Run("concurrent same source clones once", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		var mu sync.Mutex
		clones := 0
		cache := testCache(t.TempDir(), func(_ context.Context, _, _, destination string) error {
			mu.Lock()
			clones++
			mu.Unlock()
			close(started)
			<-release
			return writeSnapshotLab(destination, "lab")
		})

		firstDone := make(chan error, 1)
		go func() {
			_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
			firstDone <- err
		}()
		<-started
		secondWaiting := make(chan struct{})
		secondDone := make(chan error, 1)
		go func() {
			ctx := &observingContext{Context: context.Background(), waiting: secondWaiting}
			_, err := cache.Load(ctx, "https://example.test/labs.git", "main", "lab")
			secondDone <- err
		}()
		<-secondWaiting
		close(release)
		require.NoError(t, <-firstDone)
		require.NoError(t, <-secondDone)
		mu.Lock()
		assert.Equal(t, 1, clones)
		mu.Unlock()
	})

	t.Run("waiter can cancel while clone in progress", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		cloneErr := errors.New("clone failed")
		cache := newCache(t.TempDir(), func(context.Context, string, string, string) error {
			close(started)
			<-release
			return cloneErr
		})

		firstDone := make(chan error, 1)
		go func() {
			_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
			firstDone <- err
		}()
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := cache.Load(ctx, "https://example.test/labs.git", "main", "lab")
		require.ErrorIs(t, err, context.Canceled)
		close(release)
		require.ErrorIs(t, <-firstDone, ErrRepositoryUnavailable)
	})

	t.Run("uses hashed entry with content snapshot", func(t *testing.T) {
		root := t.TempDir()
		const repository = "https://example.test/labs.git"
		const branch = "main"
		cache := testCache(root, func(_ context.Context, _, _, destination string) error {
			return writeSnapshotLab(destination, "lab")
		})

		_, err := cache.Load(context.Background(), repository, branch, "lab")
		require.NoError(t, err)

		sum := sha256.Sum256([]byte("https://example.test/labs.git|main"))
		entry := filepath.Join(root, fmt.Sprintf("%x", sum))
		_, err = os.Stat(filepath.Join(entry, "content"))
		require.NoError(t, err)
		assert.NotContains(t, entry, repository)
	})

	t.Run("clones into reserved empty temporary directory", func(t *testing.T) {
		cache := testCache(t.TempDir(), func(_ context.Context, _, _, destination string) error {
			entries, err := os.ReadDir(destination)
			require.NoError(t, err)
			assert.Empty(t, entries)
			return writeSnapshotLab(destination, "lab")
		})

		_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
		require.NoError(t, err)
	})

	t.Run("finish failed entry evicts and notifies waiters", func(t *testing.T) {
		cache := newCache(t.TempDir(), nil)
		key := sourceKey{repository: "https://example.test/labs.git", branch: "main"}
		entry := &cacheEntry{done: make(chan struct{})}
		cache.entries[key] = entry
		cloneErr := errors.New("clone failed")

		cache.finish(key, entry, cloneErr)

		select {
		case <-entry.done:
		default:
			assert.FailNow(t, "finish did not notify waiters")
		}
		require.ErrorIs(t, entry.err, cloneErr)
		_, found := cache.entries[key]
		assert.False(t, found)
	})

	t.Run("reuses existing content snapshot", func(t *testing.T) {
		root := t.TempDir()
		const repository = "https://example.test/labs.git"
		const branch = "main"
		sum := sha256.Sum256([]byte("https://example.test/labs.git|main"))
		require.NoError(t, writeSnapshotLab(filepath.Join(root, fmt.Sprintf("%x", sum), "content"), "lab"))

		cloned := false
		cache := testCache(root, func(context.Context, string, string, string) error {
			cloned = true
			return errors.New("must not clone")
		})
		_, err := cache.Load(context.Background(), repository, branch, "lab")
		require.NoError(t, err)
		assert.False(t, cloned)
	})

	t.Run("retries failed clone after removing temporary sibling", func(t *testing.T) {
		root := t.TempDir()
		attempts := 0
		cache := testCache(root, func(_ context.Context, _, _, destination string) error {
			attempts++
			if err := writeSnapshotLab(destination, "lab"); err != nil {
				return err
			}
			if attempts == 1 {
				return errors.New("remote unavailable")
			}
			return nil
		})

		_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
		require.ErrorIs(t, err, ErrRepositoryUnavailable)
		sum := sha256.Sum256([]byte("https://example.test/labs.git|main"))
		entry := filepath.Join(root, fmt.Sprintf("%x", sum))
		entries, err := os.ReadDir(entry)
		require.NoError(t, err)
		assert.Empty(t, entries)

		_, err = cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
		require.NoError(t, err)
		assert.Equal(t, 2, attempts)
	})

	t.Run("publishes content only after clone completes", func(t *testing.T) {
		root := t.TempDir()
		started := make(chan struct{})
		release := make(chan struct{})
		cache := testCache(root, func(_ context.Context, _, _, destination string) error {
			if err := writeSnapshotLab(destination, "lab"); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})

		done := make(chan error, 1)
		go func() {
			_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
			done <- err
		}()
		<-started
		sum := sha256.Sum256([]byte(`{"branch":"main","repository":"https://example.test/labs.git"}`))
		_, err := os.Stat(filepath.Join(root, fmt.Sprintf("%x", sum), "content"))
		require.ErrorIs(t, err, os.ErrNotExist)
		close(release)
		require.NoError(t, <-done)
	})

	t.Run("close waits and preserves successful content", func(t *testing.T) {
		root := t.TempDir()
		started := make(chan struct{})
		release := make(chan struct{})
		cache := testCache(root, func(_ context.Context, _, _, destination string) error {
			close(started)
			<-release
			return writeSnapshotLab(destination, "lab")
		})
		loadDone := make(chan error, 1)
		go func() {
			_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
			loadDone <- err
		}()
		<-started
		closeDone := make(chan error, 1)
		go func() { closeDone <- cache.Close() }()
		require.Eventually(t, func() bool {
			cache.mu.Lock()
			defer cache.mu.Unlock()
			return cache.closed
		}, time.Second, time.Millisecond)
		select {
		case err := <-closeDone:
			t.Fatalf("Close returned before clone completed: %v", err)
		default:
		}
		close(release)
		require.NoError(t, <-loadDone)
		require.NoError(t, <-closeDone)
		sum := sha256.Sum256([]byte("https://example.test/labs.git|main"))
		_, err := os.Stat(filepath.Join(root, fmt.Sprintf("%x", sum), "content"))
		require.NoError(t, err)
	})

	t.Run("rejects unsafe lab path before clone", func(t *testing.T) {
		called := false
		cache := testCache(t.TempDir(), func(context.Context, string, string, string) error { called = true; return nil })
		_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "../lab")
		require.ErrorIs(t, err, ErrLabInvalid)
		assert.False(t, called)
	})
}

func TestCloneSnapshotUsesNonInteractiveShallowSingleBranchClone(t *testing.T) {
	args := cloneArgs("https://example.test/labs.git", "main", "/tmp/destination")
	assert.Equal(t, []string{"clone", "--depth", "1", "--single-branch", "--branch", "main", "--", "https://example.test/labs.git", "/tmp/destination"}, args)
	env := gitEnv([]string{"GIT_TERMINAL_PROMPT=1", "OTHER=value"})
	assert.Equal(t, []string{"OTHER=value", "GIT_TERMINAL_PROMPT=0"}, env)
}
