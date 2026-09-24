// Package bundle loads a lab bundle from a cloned repository directory.
package bundle

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

var (
	validModuleID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	encodedPathText = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)

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
	Bytes    []byte
	Revision string
}

// validBranchRE allows branch names that cannot contain shell-special characters.
// Allows: alphanumeric, dash, dot, underscore, forward-slash.
var validBranchRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// Module is one lab module with navigable paths relative to its content/ directory.
type Module struct {
	ID    string
	Pages []string
}

// Result is the in-memory lab bundle loaded from disk.
type Result struct {
	Manifest           *manifest.LabManifest
	Modules            []Module // sorted by literal module ID
	HelmChartAvailable bool
	HelmChartDir       string // absolute path to setup/helm/ if available, empty otherwise
	ValuesSchema       []byte // raw contents of setup/helm/values.schema.json, nil if absent
}

// ValidateCloneArgs validates an approved repository URL and branch.
// Returns a non-nil error if either argument is unsafe.
func ValidateCloneArgs(repoURL, branch string) error {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("repository URL must use https scheme: %q", repoURL)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("repository URL must be a plain https URL: %q", repoURL)
	}
	// '-' is disallowed to be the first char of the branch name by git apparently, so let's do this check early, too
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
	if hasUnsupportedPathText(labPath) {
		return errors.New("lab path contains unsupported characters")
	}
	return nil
}

func hasUnsupportedPathText(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == '\\' }) >= 0 || encodedPathText.MatchString(value)
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
	if err = rejectSymlinkPath(root, resourcePath); err != nil {
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

	content, err := os.ReadFile(target)
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

// LoadFromSnapshot validates labPath before reading it from repositoryRoot.
func LoadFromSnapshot(repositoryRoot, labPath string) (*Result, error) {
	if err := ValidateLabPath(labPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}
	root, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: repository snapshot unavailable", ErrRepositoryUnavailable)
	}
	if err := rejectSymlinkPath(root, labPath); err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return nil, fmt.Errorf("%w: lab path", ErrManifestMissing)
		}
		return nil, fmt.Errorf("%w: lab path", ErrLabInvalid)
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
	return LoadFromDir(labDir, root)
}

// LoadFromDir loads a lab bundle from an already-populated labPath on disk.
// repoRoot is the repository root directory where setup/helm/ is expected.
// Exported for filesystem-focused tests.
func LoadFromDir(labDir string, repoRoot string) (*Result, error) {
	manifestPath := filepath.Join(labDir, "lab.yaml")
	manifestInfo, err := os.Lstat(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: lab.yaml", ErrManifestMissing)
	}
	if err != nil || !manifestInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: invalid lab.yaml", ErrLabInvalid)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
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

	modulesDir := filepath.Join(labDir, "modules")
	info, err := os.Lstat(modulesDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: modules directory", ErrLabInvalid)
	}
	entries, err := os.ReadDir(modulesDir)
	if err != nil || len(entries) == 0 {
		return nil, fmt.Errorf("%w: no modules", ErrLabInvalid)
	}
	modules := make([]Module, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validModuleID.MatchString(entry.Name()) {
			return nil, fmt.Errorf("%w: invalid module entry", ErrLabInvalid)
		}
		moduleDir := filepath.Join(modulesDir, entry.Name())
		contentDir := filepath.Join(moduleDir, "content")
		contentInfo, err := os.Lstat(contentDir)
		if err != nil || !contentInfo.IsDir() {
			return nil, fmt.Errorf("%w: module content directory", ErrLabInvalid)
		}
		var contentPaths []string
		err = filepath.WalkDir(moduleDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return errors.New("symlink in module")
			}
			if d.IsDir() {
				return nil
			}
			file, err := d.Info()
			if err != nil || !file.Mode().IsRegular() {
				return errors.New("non-regular module file")
			}
			rel, err := filepath.Rel(contentDir, p)
			if err != nil {
				return err
			}
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && strings.HasSuffix(p, ".adoc") {
				page := filepath.ToSlash(rel)
				if hasUnsupportedPathText(page) {
					return errors.New("unsupported page path")
				}
				contentPaths = append(contentPaths, page)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%w: read module content", ErrLabInvalid)
		}
		if labErr := manifest.ValidateBundlePaths(contentPaths); labErr != nil {
			return nil, fmt.Errorf("%w: module has no navigable pages", ErrLabInvalid)
		}
		pages := make([]string, 0, len(contentPaths))
		for _, page := range contentPaths {
			if !isIncludeOnly(page) {
				pages = append(pages, page)
			}
		}
		slices.Sort(pages)
		moduleYAML, err := os.ReadFile(filepath.Join(moduleDir, "module.yaml"))
		if err == nil {
			if _, labErr := manifest.ValidateModule(moduleYAML, pages); labErr != nil {
				return nil, fmt.Errorf("%w: invalid module.yaml", ErrLabInvalid)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: read module.yaml", ErrLabInvalid)
		}
		modules = append(modules, Module{ID: entry.Name(), Pages: pages})
	}
	slices.SortFunc(modules, func(a, b Module) int { return strings.Compare(a.ID, b.ID) })

	helmDir := filepath.Join(repoRoot, "setup", "helm")
	_, helmErr := os.Stat(filepath.Join(helmDir, "Chart.yaml"))
	helmAvailable := helmErr == nil

	result := &Result{Manifest: m, Modules: modules, HelmChartAvailable: helmAvailable}
	if helmAvailable {
		result.HelmChartDir = helmDir
		if schema, err := os.ReadFile(filepath.Join(helmDir, "values.schema.json")); err == nil {
			result.ValuesSchema = schema
		}
	}
	return result, nil
}

func isIncludeOnly(page string) bool {
	return strings.HasPrefix(page, "_") || strings.Contains(page, "/_")
}
