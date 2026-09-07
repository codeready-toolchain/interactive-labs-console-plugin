// Package bundle loads a lab bundle from a cloned repository directory.
package bundle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

var (
	// ErrRepositoryUnavailable means an approved source could not be obtained.
	ErrRepositoryUnavailable = errors.New("repository unavailable")
	// ErrManifestMissing means the configured lab has no lab.yaml.
	ErrManifestMissing = errors.New("manifest missing")
	// ErrLabInvalid means lab content cannot safely form a valid bundle.
	ErrLabInvalid = errors.New("lab invalid")
	// ErrResourceNotFound means a requested repository resource does not exist.
	ErrResourceNotFound = errors.New("resource not found")
	// ErrResourceInvalid means a requested repository resource is unsafe or unsupported.
	ErrResourceInvalid = errors.New("resource invalid")
	// ErrResourceUnsupported means a contained resource cannot be safely served.
	ErrResourceUnsupported = errors.New("resource unsupported")
)

// Resource is one regular file from an approved repository snapshot.
type Resource struct {
	Bytes []byte
}

// validBranchRE allows branch names that cannot contain shell-special characters.
// Allows: alphanumeric, dash, dot, underscore, forward-slash.
var validBranchRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// Result is the in-memory lab bundle loaded from disk.
type Result struct {
	Manifest           *manifest.LabManifest
	Pages              []string // sorted navigable relative page paths
	HelmChartAvailable bool
}

// ValidateCloneArgs validates repository URL and branch before shell execution.
// Returns a non-nil error if either argument is unsafe.
func ValidateCloneArgs(repoURL, branch string) error {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("repository URL must use https scheme: %q", repoURL)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("repository URL must be a plain https URL: %q", repoURL)
	}
	if !validBranchRE.MatchString(branch) || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") || strings.Contains(branch, "..") {
		return fmt.Errorf("branch contains invalid characters: %q", branch)
	}
	return nil
}

// ValidateLabPath requires a non-empty repository-relative, non-traversing path.
func ValidateLabPath(labPath string) error {
	if labPath == "" || path.IsAbs(labPath) {
		return errors.New("lab path must be repository-relative")
	}
	if slices.Contains(strings.Split(labPath, "/"), "..") {
		return errors.New("lab path must not traverse")
	}
	return nil
}

// ValidateResourcePath permits only a repository-relative non-URL path.
func ValidateResourcePath(resourcePath string) error {
	parsed, err := url.Parse(resourcePath)
	if resourcePath == "" || err != nil || parsed.IsAbs() || parsed.Host != "" || path.IsAbs(resourcePath) || strings.Contains(resourcePath, "\\") || slices.Contains(strings.Split(resourcePath, "/"), "..") {
		return ErrResourceInvalid
	}
	return nil
}

// ReadResource resolves and reads a regular file below repositoryRoot.
func ReadResource(repositoryRoot, resourcePath string) (*Resource, error) {
	if err := ValidateResourcePath(resourcePath); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: snapshot", ErrRepositoryUnavailable)
	}
	if err := rejectSymlinkPath(root, resourcePath); err != nil {
		return nil, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(resourcePath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: target", ErrResourceNotFound)
		}
		return nil, fmt.Errorf("%w: resolve target", ErrResourceInvalid)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: target escapes repository", ErrResourceInvalid)
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: target", ErrResourceNotFound)
		}
		return nil, fmt.Errorf("%w: stat target", ErrResourceInvalid)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: target is not a regular file", ErrResourceUnsupported)
	}

	content, err := os.ReadFile(target) //nolint:gosec // target is canonical and contained above.
	if err != nil {
		return nil, fmt.Errorf("%w: read target", ErrResourceInvalid)
	}
	return &Resource{Bytes: content}, nil
}

func rejectSymlinkPath(root, resourcePath string) error {
	current := root
	for _, segment := range strings.Split(resourcePath, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: target", ErrResourceNotFound)
			}
			return fmt.Errorf("%w: inspect target", ErrResourceInvalid)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink target", ErrResourceInvalid)
		}
	}
	return nil
}

// CloneAndLoad shallow-clones one repository for callers outside Cache.
func CloneAndLoad(ctx context.Context, repoURL, branch, labPath string) (*Result, error) {
	if err := ValidateCloneArgs(repoURL, branch); err != nil {
		return nil, err
	}
	if err := ValidateLabPath(labPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}
	tmpDir, err := os.MkdirTemp("", "ilab-clone-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create temp dir", ErrRepositoryUnavailable)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.CommandContext(ctx, "git", cloneArgs(repoURL, branch, tmpDir)...) // #nosec: G204
	cmd.Env = gitEnv(nil)
	if _, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w: clone failed", ErrRepositoryUnavailable)
	}
	return LoadFromSnapshot(tmpDir, labPath)
}

// LoadFromSnapshot validates labPath before reading it from repositoryRoot.
func LoadFromSnapshot(repositoryRoot, labPath string) (*Result, error) {
	if err := ValidateLabPath(labPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}
	root, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: repository snapshot unavailable", ErrRepositoryUnavailable)
	}
	labDir, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(labPath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: lab path", ErrManifestMissing)
		}
		return nil, fmt.Errorf("%w: resolve lab path", ErrLabInvalid)
	}
	rel, err := filepath.Rel(root, labDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: lab path escapes repository", ErrLabInvalid)
	}
	return LoadFromDir(labDir)
}

// LoadFromDir loads a lab bundle from an already-populated labPath on disk.
// Exported for filesystem-focused tests.
func LoadFromDir(labDir string) (*Result, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(labDir, "lab.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: lab.yaml", ErrManifestMissing)
		}
		return nil, fmt.Errorf("%w: read lab.yaml", ErrLabInvalid)
	}
	m, labErr := manifest.ValidateManifest(manifestBytes)
	if labErr != nil {
		return nil, fmt.Errorf("%w: manifest", ErrLabInvalid)
	}

	var contentPaths []string
	contentDir := filepath.Join(labDir, "content")
	if err := filepath.WalkDir(contentDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".adoc") {
			return nil
		}
		rel, err := filepath.Rel(contentDir, p)
		if err != nil {
			return err
		}
		contentPaths = append(contentPaths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("%w: read content", ErrLabInvalid)
	}
	if labErr := manifest.ValidateBundlePaths(contentPaths); labErr != nil {
		return nil, fmt.Errorf("%w: content", ErrLabInvalid)
	}

	pages := make([]string, 0, len(contentPaths))
	for _, page := range contentPaths {
		if !isIncludeOnly(page) {
			pages = append(pages, page)
		}
	}
	slices.Sort(pages)

	_, helmErr := os.Stat(filepath.Join(labDir, "setup", "helm", "Chart.yaml"))
	return &Result{Manifest: m, Pages: pages, HelmChartAvailable: helmErr == nil}, nil
}

func isIncludeOnly(page string) bool {
	return strings.HasPrefix(page, "_") || strings.Contains(page, "/_")
}
