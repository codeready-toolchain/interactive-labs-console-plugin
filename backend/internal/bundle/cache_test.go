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

func writeSnapshotLab(root, labPath string) error {
	lab := filepath.Join(root, filepath.FromSlash(labPath))
	if err := os.MkdirAll(filepath.Join(lab, "content"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(lab, "lab.yaml"), []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: test
  displayName: Test
spec: {}`), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(lab, "content", "intro.adoc"), []byte("= Test"), 0o644)
}

func TestCache_ReusesSuccessfulSnapshotByRepositoryAndBranch(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	clones := 0
	cache := newCache(root, func(_ context.Context, _, _, destination string) error {
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
}

func TestCache_SeparatesRepositoryAndBranch(t *testing.T) {
	clones := 0
	cache := newCache(t.TempDir(), func(_ context.Context, _, _, destination string) error {
		clones++
		return writeSnapshotLab(destination, "lab")
	})
	for _, source := range [][2]string{{"https://example.test/one.git", "main"}, {"https://example.test/two.git", "main"}, {"https://example.test/one.git", "next"}} {
		_, err := cache.Load(context.Background(), source[0], source[1], "lab")
		require.NoError(t, err)
	}
	assert.Equal(t, 3, clones)
}

func TestCache_ConcurrentSameSourceClonesOnceAndWaiterCanCancel(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	clones := 0
	cache := newCache(root, func(_ context.Context, _, _, destination string) error {
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.Load(ctx, "https://example.test/labs.git", "main", "lab")
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	require.NoError(t, <-firstDone)
	mu.Lock()
	assert.Equal(t, 1, clones)
	mu.Unlock()
}

func TestCache_UsesHashedEntryWithContentSnapshot(t *testing.T) {
	root := t.TempDir()
	const repository = "https://example.test/labs.git"
	const branch = "main"
	cache := newCache(root, func(_ context.Context, _, _, destination string) error {
		return writeSnapshotLab(destination, "lab")
	})

	_, err := cache.Load(context.Background(), repository, branch, "lab")
	require.NoError(t, err)

	sum := sha256.Sum256([]byte(`{"branch":"main","repository":"https://example.test/labs.git"}`))
	entry := filepath.Join(root, fmt.Sprintf("%x", sum))
	_, err = os.Stat(filepath.Join(entry, "content"))
	require.NoError(t, err)
	assert.NotContains(t, entry, repository)
}

func TestCache_ClonesIntoReservedEmptyTemporaryDirectory(t *testing.T) {
	cache := newCache(t.TempDir(), func(_ context.Context, _, _, destination string) error {
		entries, err := os.ReadDir(destination)
		require.NoError(t, err)
		assert.Empty(t, entries)
		return writeSnapshotLab(destination, "lab")
	})

	_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
	require.NoError(t, err)
}

func TestCache_FinishFailedEntryEvictsAndNotifiesWaiters(t *testing.T) {
	cache := newCache(t.TempDir(), nil)
	key := sourceKey{repository: "https://example.test/labs.git", branch: "main"}
	entry := &cacheEntry{done: make(chan struct{})}
	cache.entries[key] = entry
	cloneErr := errors.New("clone failed")

	cache.finish(key, entry, cloneErr)

	select {
	case <-entry.done:
	default:
		t.Fatal("finish did not notify waiters")
	}
	assert.ErrorIs(t, entry.err, cloneErr)
	_, found := cache.entries[key]
	assert.False(t, found)
}

func TestCache_ReusesExistingContentSnapshot(t *testing.T) {
	root := t.TempDir()
	const repository = "https://example.test/labs.git"
	const branch = "main"
	sum := sha256.Sum256([]byte(`{"branch":"main","repository":"https://example.test/labs.git"}`))
	require.NoError(t, writeSnapshotLab(filepath.Join(root, fmt.Sprintf("%x", sum), "content"), "lab"))

	cloned := false
	cache := newCache(root, func(context.Context, string, string, string) error {
		cloned = true
		return errors.New("must not clone")
	})
	_, err := cache.Load(context.Background(), repository, branch, "lab")
	require.NoError(t, err)
	assert.False(t, cloned)
}

func TestCache_RetriesFailedCloneAfterRemovingTemporarySibling(t *testing.T) {
	root := t.TempDir()
	attempts := 0
	cache := newCache(root, func(_ context.Context, _, _, destination string) error {
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
	sum := sha256.Sum256([]byte(`{"branch":"main","repository":"https://example.test/labs.git"}`))
	entry := filepath.Join(root, fmt.Sprintf("%x", sum))
	entries, err := os.ReadDir(entry)
	require.NoError(t, err)
	assert.Empty(t, entries)

	_, err = cache.Load(context.Background(), "https://example.test/labs.git", "main", "lab")
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestCache_PublishesContentOnlyAfterCloneCompletes(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	cache := newCache(root, func(_ context.Context, _, _, destination string) error {
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
	assert.ErrorIs(t, err, os.ErrNotExist)
	close(release)
	require.NoError(t, <-done)
}

func TestCache_CloseWaitsAndPreservesSuccessfulContent(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	cache := newCache(root, func(_ context.Context, _, _, destination string) error {
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
	sum := sha256.Sum256([]byte(`{"branch":"main","repository":"https://example.test/labs.git"}`))
	_, err := os.Stat(filepath.Join(root, fmt.Sprintf("%x", sum), "content"))
	require.NoError(t, err)
}

func TestCache_RejectsUnsafeLabPathBeforeClone(t *testing.T) {
	called := false
	cache := newCache(t.TempDir(), func(context.Context, string, string, string) error { called = true; return nil })
	_, err := cache.Load(context.Background(), "https://example.test/labs.git", "main", "../lab")
	require.ErrorIs(t, err, ErrLabInvalid)
	assert.False(t, called)
}

func TestCloneSnapshotUsesNonInteractiveShallowSingleBranchClone(t *testing.T) {
	args := cloneArgs("https://example.test/labs.git", "main", "/tmp/destination")
	assert.Equal(t, []string{"clone", "--depth", "1", "--single-branch", "--branch", "main", "--", "https://example.test/labs.git", "/tmp/destination"}, args)
	env := gitEnv([]string{"GIT_TERMINAL_PROMPT=1", "OTHER=value"})
	assert.Equal(t, []string{"OTHER=value", "GIT_TERMINAL_PROMPT=0"}, env)
}
