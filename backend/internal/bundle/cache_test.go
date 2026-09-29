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

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRepository = "https://example.test/labs.git"
	testBranch     = "main"
	oldRevision    = "1111111111111111111111111111111111111111"
	newRevision    = "2222222222222222222222222222222222222222"
)

type fakeGitClient struct {
	mu            sync.Mutex
	refs          map[sourceKey]string
	heads         map[string]string
	contents      map[sourceKey]string
	cloneErr      map[sourceKey]error
	remoteErr     map[sourceKey]error
	cloneCalls    map[sourceKey]int
	headCalls     int
	cloneCheck    func(string) error
	cloneStarted  chan struct{}
	cloneWait     <-chan struct{}
	cloneFinished chan struct{}
}

func newFakeGit() *fakeGitClient {
	return &fakeGitClient{
		refs:       map[sourceKey]string{},
		heads:      map[string]string{},
		contents:   map[sourceKey]string{},
		cloneErr:   map[sourceKey]error{},
		remoteErr:  map[sourceKey]error{},
		cloneCalls: map[sourceKey]int{},
	}
}

func (f *fakeGitClient) RemoteRef(_ context.Context, repository, branch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sourceKey{repository, branch}
	if err := f.remoteErr[key]; err != nil {
		return "", err
	}
	return f.refs[key], nil
}

func (f *fakeGitClient) Clone(ctx context.Context, repository, branch, destination string) error {
	key := sourceKey{repository, branch}
	f.mu.Lock()
	f.cloneCalls[key]++
	err, revision, content := f.cloneErr[key], f.refs[key], f.contents[key]
	check, started, wait := f.cloneCheck, f.cloneStarted, f.cloneWait
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(destination); err != nil {
			return err
		}
	}
	if err := writeSnapshotLab(destination, "lab", content); err != nil {
		return err
	}
	if err := writeSnapshotLab(destination, "other", content); err != nil {
		return err
	}
	f.mu.Lock()
	f.heads[destination] = revision
	finished := f.cloneFinished
	f.mu.Unlock()
	if finished != nil {
		close(finished)
	}
	return nil
}

func (f *fakeGitClient) Head(destination string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	revision, ok := f.heads[destination]
	if !ok {
		return "", errors.New("no HEAD")
	}
	return revision, nil
}

func (f *fakeGitClient) cloneCount(repository, branch string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cloneCalls[sourceKey{repository, branch}]
}

func (f *fakeGitClient) headCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headCalls
}

func writeSnapshotLab(root, labPath, content string) error {
	lab := filepath.Join(root, filepath.FromSlash(labPath))
	if err := os.MkdirAll(filepath.Join(lab, "modules", "010-intro", "content"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(lab, "lab.yaml"), []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  title: Test
spec: {}`), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(lab, "modules", "010-intro", "content", "intro.adoc"), []byte(content), 0o600)
}

func refresh(cache *Cache, repository, branch string) error {
	return cache.Refresh(context.Background(), RepositoryRefreshRequest{Repository: repository, Branch: branch})
}

func TestCache(t *testing.T) {
	t.Run("reuses successful snapshot by repository and branch", func(t *testing.T) {
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Test"
		cache := newCache(t.TempDir(), client)

		require.NoError(t, refresh(cache, testRepository, testBranch))
		for _, labPath := range []string{"lab", "other", "lab"} {
			result, err := cache.LoadBundle(context.Background(), testRepository, testBranch, labPath)
			require.NoError(t, err)
			assert.Equal(t, []Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}}, result.Modules)
		}
		assert.Equal(t, 1, client.cloneCount(testRepository, testBranch))
	})

	t.Run("caches revision in memory without snapshot metadata", func(t *testing.T) {
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Test"
		cache := newCache(t.TempDir(), client)

		require.NoError(t, refresh(cache, testRepository, testBranch))
		client.refs[key], client.contents[key] = newRevision, "= New"
		require.NoError(t, refresh(cache, testRepository, testBranch))
		result, err := cache.LoadBundle(context.Background(), testRepository, testBranch, "lab")
		require.NoError(t, err)
		assert.Contains(t, result.Modules[0].Pages, "intro.adoc")
		for range 2 {
			var resource *Resource
			resource, err = cache.ReadResource(context.Background(), testRepository, testBranch, "lab/modules/010-intro/content/intro.adoc")
			require.NoError(t, err)
			assert.Equal(t, []byte("= New"), resource.Bytes)
			assert.Equal(t, newRevision, resource.Revision)
		}
		assert.Equal(t, 2, client.cloneCount(testRepository, testBranch))
		assert.Equal(t, 3, client.headCount())
		_, err = os.Stat(filepath.Join(cache.root, snapshotID(testRepository, testBranch), "snapshot.json"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("resolves revision once for existing checkout", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, snapshotID(testRepository, testBranch), "content")
		require.NoError(t, writeSnapshotLab(destination, "lab", "= Test"))
		client := newFakeGit()
		client.heads[destination] = oldRevision
		cache := newCache(root, client)

		_, err := cache.LoadBundle(context.Background(), testRepository, testBranch, "lab")
		require.NoError(t, err)
		for range 2 {
			resource, err := cache.ReadResource(context.Background(), testRepository, testBranch, "lab/modules/010-intro/content/intro.adoc")
			require.NoError(t, err)
			assert.Equal(t, []byte("= Test"), resource.Bytes)
			assert.Equal(t, oldRevision, resource.Revision)
		}
		assert.Zero(t, client.cloneCount(testRepository, testBranch))
		assert.Equal(t, 1, client.headCount())
	})

	t.Run("separates repository and branch", func(t *testing.T) {
		client := newFakeGit()
		sources := [][2]string{{"https://example.test/one.git", "main"}, {"https://example.test/two.git", "main"}, {"https://example.test/one.git", "next"}}
		for index, source := range sources {
			key := sourceKey{source[0], source[1]}
			client.refs[key] = fmt.Sprintf("%040d", index+1)
			client.contents[key] = "= Test"
		}
		cache := newCache(t.TempDir(), client)

		for _, source := range sources {
			require.NoError(t, refresh(cache, source[0], source[1]))
		}
		for _, source := range sources {
			assert.Equal(t, 1, client.cloneCount(source[0], source[1]))
		}
	})

	t.Run("uses hashed entry with content snapshot", func(t *testing.T) {
		root := t.TempDir()
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Test"
		cache := newCache(root, client)

		require.NoError(t, refresh(cache, testRepository, testBranch))

		sum := sha256.Sum256([]byte(testRepository + "|" + testBranch))
		entry := filepath.Join(root, fmt.Sprintf("%x", sum))
		_, err := os.Stat(filepath.Join(entry, "content"))
		require.NoError(t, err)
		assert.NotContains(t, entry, testRepository)
	})

	t.Run("clones into reserved empty temporary directory", func(t *testing.T) {
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Test"
		client.cloneCheck = func(destination string) error {
			entries, err := os.ReadDir(destination)
			require.NoError(t, err)
			assert.Empty(t, entries)
			return nil
		}
		cache := newCache(t.TempDir(), client)

		require.NoError(t, refresh(cache, testRepository, testBranch))
	})

	t.Run("reuses existing content snapshot", func(t *testing.T) {
		root := t.TempDir()
		destination := filepath.Join(root, snapshotID(testRepository, testBranch), "content")
		require.NoError(t, writeSnapshotLab(destination, "lab", "= Test"))
		client := newFakeGit()
		client.heads[destination] = oldRevision
		cache := newCache(root, client)

		_, err := cache.LoadBundle(context.Background(), testRepository, testBranch, "lab")
		require.NoError(t, err)
		assert.Zero(t, client.cloneCount(testRepository, testBranch))
	})

	t.Run("retries failed clone after removing temporary sibling", func(t *testing.T) {
		root := t.TempDir()
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key], client.cloneErr[key] = oldRevision, "= Test", errors.New("remote unavailable")
		cache := newCache(root, client)

		err := refresh(cache, testRepository, testBranch)
		require.ErrorIs(t, err, ErrCloneFailed)
		entries, err := os.ReadDir(filepath.Join(root, snapshotID(testRepository, testBranch)))
		require.NoError(t, err)
		assert.Empty(t, entries)

		delete(client.cloneErr, key)
		require.NoError(t, refresh(cache, testRepository, testBranch))
		assert.Equal(t, 2, client.cloneCount(testRepository, testBranch))
	})

	t.Run("publishes content only after clone completes", func(t *testing.T) {
		root := t.TempDir()
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Test"
		client.cloneStarted = make(chan struct{})
		release := make(chan struct{})
		client.cloneWait = release
		cache := newCache(root, client)

		done := make(chan error, 1)
		go func() { done <- refresh(cache, testRepository, testBranch) }()
		<-client.cloneStarted
		_, err := os.Stat(filepath.Join(root, snapshotID(testRepository, testBranch), "content"))
		require.ErrorIs(t, err, os.ErrNotExist)
		close(release)
		require.NoError(t, <-done)
	})

	t.Run("close waits and preserves successful content", func(t *testing.T) {
		root := t.TempDir()
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Test"
		client.cloneStarted = make(chan struct{})
		release := make(chan struct{})
		client.cloneWait = release
		cache := newCache(root, client)

		refreshDone := make(chan error, 1)
		go func() { refreshDone <- refresh(cache, testRepository, testBranch) }()
		<-client.cloneStarted
		closeDone := make(chan error, 1)
		go func() { closeDone <- cache.Close() }()
		require.Eventually(t, func() bool {
			cache.mu.Lock()
			defer cache.mu.Unlock()
			return cache.closed
		}, time.Second, time.Millisecond)
		select {
		case <-closeDone:
			require.FailNow(t, "Close returned before refresh completed")
		default:
		}
		close(release)
		require.NoError(t, <-refreshDone)
		require.NoError(t, <-closeDone)
		require.ErrorIs(t, refresh(cache, testRepository, testBranch), ErrRepositoryUnavailable)
		_, err := os.Stat(filepath.Join(root, snapshotID(testRepository, testBranch), "content"))
		require.NoError(t, err)
	})

	t.Run("rejects unsafe lab path before clone", func(t *testing.T) {
		client := newFakeGit()
		cache := newCache(t.TempDir(), client)

		_, err := cache.LoadBundle(context.Background(), testRepository, testBranch, "../lab")
		require.ErrorIs(t, err, ErrLabInvalid)
		assert.Zero(t, client.cloneCount(testRepository, testBranch))
		assert.Empty(t, cache.entries)
	})

	t.Run("read resource does not require valid lab bundle", func(t *testing.T) {
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Old"
		cache := newCache(t.TempDir(), client)
		require.NoError(t, refresh(cache, testRepository, testBranch))

		entry, err := cache.entry(testRepository, testBranch)
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(entry.checkoutDirectory, "lab", "lab.yaml")))
		resource, err := cache.ReadResource(context.Background(), testRepository, testBranch, "lab/modules/010-intro/content/intro.adoc")
		require.NoError(t, err)
		assert.Equal(t, []byte("= Old"), resource.Bytes)
	})

	t.Run("refresh acquisition failure retains last known good generation", func(t *testing.T) {
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Old"
		cache := newCache(t.TempDir(), client)
		require.NoError(t, refresh(cache, testRepository, testBranch))

		client.refs[key], client.cloneErr[key] = newRevision, errors.New("raw git output must not escape")
		err := refresh(cache, testRepository, testBranch)
		require.ErrorIs(t, err, ErrCloneFailed)
		resource, err := cache.ReadResource(context.Background(), testRepository, testBranch, "lab/modules/010-intro/content/intro.adoc")
		require.NoError(t, err)
		assert.Equal(t, []byte("= Old"), resource.Bytes)
		assert.Equal(t, oldRevision, resource.Revision)
	})

	t.Run("failed initial source does not demand clone again", func(t *testing.T) {
		client := newFakeGit()
		client.remoteErr[sourceKey{testRepository, testBranch}] = errors.New("raw remote output")
		cache := newCache(t.TempDir(), client)

		err := refresh(cache, testRepository, testBranch)
		require.ErrorIs(t, err, ErrRemoteReferenceLookupFailed)
		_, err = cache.LoadBundle(context.Background(), testRepository, testBranch, "lab")
		require.ErrorIs(t, err, ErrRepositoryUnavailable)
		assert.Zero(t, client.cloneCount(testRepository, testBranch))
	})

	t.Run("refresh returns cancellation from remote reference lookup", func(t *testing.T) {
		for _, expected := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(expected.Error(), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if errors.Is(expected, context.DeadlineExceeded) {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				}
				defer cancel()
				if errors.Is(expected, context.Canceled) {
					cancel()
				}
				require.ErrorIs(t, ctx.Err(), expected)

				client := newFakeGit()
				client.remoteErr[sourceKey{testRepository, testBranch}] = ctx.Err()

				err := newCache(t.TempDir(), client).Refresh(ctx, RepositoryRefreshRequest{Repository: testRepository, Branch: testBranch})
				require.ErrorIs(t, err, expected)
				require.NotErrorIs(t, err, ErrRepositoryUnavailable)
			})
		}
	})

	t.Run("refresh preserves remote reference failure when context is canceled concurrently", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := newFakeGit()
		client.remoteErr[sourceKey{testRepository, testBranch}] = errors.New("raw remote output")

		err := newCache(t.TempDir(), client).Refresh(ctx, RepositoryRefreshRequest{Repository: testRepository, Branch: testBranch})
		require.ErrorIs(t, err, ErrRemoteReferenceLookupFailed)
		require.NotErrorIs(t, err, context.Canceled)
	})

	t.Run("refresh returns cancellation from clone", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key] = oldRevision
		client.cloneStarted = make(chan struct{})
		client.cloneWait = make(chan struct{})

		result := make(chan error, 1)
		go func() {
			result <- newCache(t.TempDir(), client).Refresh(ctx, RepositoryRefreshRequest{Repository: testRepository, Branch: testBranch})
		}()
		<-client.cloneStarted
		cancel()
		err := <-result
		require.ErrorIs(t, err, context.Canceled)
		require.NotErrorIs(t, err, ErrRepositoryUnavailable)
	})

	t.Run("read operations reject invalid source before creating entry", func(t *testing.T) {
		cache := newCache(t.TempDir(), newFakeGit())
		_, err := cache.LoadBundle(context.Background(), "file:///untrusted", testBranch, "lab")
		require.ErrorIs(t, err, ErrRepositoryUnavailable)
		assert.Empty(t, cache.entries)

		cache = newCache(t.TempDir(), newFakeGit())
		_, err = cache.ReadResource(context.Background(), testRepository, "-untrusted", "lab/modules/010-intro/content/intro.adoc")
		require.ErrorIs(t, err, ErrRepositoryUnavailable)
		assert.Empty(t, cache.entries)
	})

	t.Run("read concurrent with publish returns complete generation", func(t *testing.T) {
		client := newFakeGit()
		key := sourceKey{testRepository, testBranch}
		client.refs[key], client.contents[key] = oldRevision, "= Old"
		cache := newCache(t.TempDir(), client)
		require.NoError(t, refresh(cache, testRepository, testBranch))

		entry, err := cache.entry(testRepository, testBranch)
		require.NoError(t, err)
		entry.mu.RLock()
		client.cloneFinished = make(chan struct{})
		client.refs[key], client.contents[key] = newRevision, "= New"
		refreshDone := make(chan error, 1)
		go func() { refreshDone <- refresh(cache, testRepository, testBranch) }()
		<-client.cloneFinished
		readerDone := make(chan struct {
			resource *Resource
			err      error
		}, 1)
		go func() {
			resource, err := cache.ReadResource(context.Background(), testRepository, testBranch, "lab/modules/010-intro/content/intro.adoc")
			readerDone <- struct {
				resource *Resource
				err      error
			}{resource, err}
		}()
		entry.mu.RUnlock()
		require.NoError(t, <-refreshDone)

		result := <-readerDone
		require.NoError(t, result.err)
		expected := map[string][]byte{oldRevision: []byte("= Old"), newRevision: []byte("= New")}
		require.Contains(t, expected, result.resource.Revision)
		require.Equal(t, expected[result.resource.Revision], result.resource.Bytes)
	})
}

func TestGoGitClientUsesShallowExplicitSingleBranchCloneAndHead(t *testing.T) {
	remote := t.TempDir()
	repository, err := git.PlainInit(remote, false)
	require.NoError(t, err)
	worktree, err := repository.Worktree()
	require.NoError(t, err)
	commit := func(content string) plumbing.Hash {
		file := filepath.Join(remote, "page.adoc")
		require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
		_, err = worktree.Add("page.adoc")
		require.NoError(t, err)
		hash, cerr := worktree.Commit(content, &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.test", When: time.Now()}})
		require.NoError(t, cerr)
		return hash
	}
	first := commit("first")
	main := plumbing.NewBranchReferenceName("main")
	require.NoError(t, repository.Storer.SetReference(plumbing.NewHashReference(main, first)))
	require.NoError(t, repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, main)))
	second := commit("second")
	require.NoError(t, repository.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("other"), first)))
	require.NoError(t, repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("other"))))

	destination := filepath.Join(t.TempDir(), "clone")
	client := goGitClient{}
	require.NoError(t, client.Clone(context.Background(), remote, "main", destination))
	head, err := client.Head(destination)
	require.NoError(t, err)
	require.Equal(t, second.String(), head)

	clone, err := git.PlainOpen(destination)
	require.NoError(t, err)
	_, err = clone.CommitObject(first)
	require.ErrorIs(t, err, plumbing.ErrObjectNotFound)
	_, err = clone.Reference(plumbing.NewRemoteReferenceName("origin", "other"), true)
	require.ErrorIs(t, err, plumbing.ErrReferenceNotFound)
}
