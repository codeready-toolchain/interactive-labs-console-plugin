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

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/chart"
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
	segments := strings.Split(resourcePath, "/")
	for i, segment := range segments {
		current = filepath.Join(current, segment)
		rel, err := filepath.Rel(root, current)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: target escapes repository", ErrResourceInvalid)
		}
		info, err := os.Lstat(current) // filepath.Rel above bounds every segment before inspection.
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: target", ErrResourceNotFound)
			}
			return fmt.Errorf("%w: inspect target", ErrResourceInvalid)
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(segments)-1 && !info.IsDir()) {
			return fmt.Errorf("%w: unsafe target", ErrResourceInvalid)
		}
	}
	return nil
}

// LoadFromSnapshot validates labPath before reading it from repositoryRoot.
func LoadFromSnapshot(repositoryRoot, labPath string) (*Result, error) {
	root, labDir, err := resolveSnapshotLab(repositoryRoot, labPath)
	if err != nil {
		return nil, err
	}
	return LoadFromDir(labDir, root)
}

// LoadContentFromSnapshot loads validated manifest and modules without chart or
// provisioning policy. Consumers compose it with chart.LoadChart;
// browser/catalog consumers must use LoadFromSnapshot to validate the whole bundle.
func LoadContentFromSnapshot(repositoryRoot, labPath string) (*manifest.LabManifest, []Module, error) {
	_, labDir, err := resolveSnapshotLab(repositoryRoot, labPath)
	if err != nil {
		return nil, nil, err
	}
	return loadContent(labDir)
}

func resolveSnapshotLab(repositoryRoot, labPath string) (string, string, error) {
	if err := ValidateLabPath(labPath); err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}
	root, err := filepath.Abs(repositoryRoot)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return "", "", fmt.Errorf("%w: repository snapshot unavailable", ErrRepositoryUnavailable)
	}
	if err = rejectSymlinkPath(root, labPath); err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return "", "", fmt.Errorf("%w: lab path", ErrManifestMissing)
		}
		return "", "", fmt.Errorf("%w: lab path", ErrLabInvalid)
	}
	labDir, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(labPath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: lab path", ErrManifestMissing)
		}
		return "", "", fmt.Errorf("%w: resolve lab path", ErrLabInvalid)
	}
	rel, err := filepath.Rel(root, labDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: lab path escapes repository", ErrLabInvalid)
	}
	return root, labDir, nil
}

// LoadFromDir loads a lab bundle from an already-populated labPath on disk.
// repoRoot bounds all selected chart paths, including lab-relative parent traversal.
// Exported for filesystem-focused tests.
func LoadFromDir(labDir string, repoRoot string) (*Result, error) {
	m, modules, err := loadContent(labDir)
	if err != nil {
		return nil, err
	}
	chart, err := chart.LoadChart(repoRoot, labDir, m.Spec.SetupDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLabInvalid, err)
	}
	return &Result{Manifest: m, Modules: modules, HelmChartAvailable: chart.Dir != ""}, nil
}

func loadContent(labDir string) (*manifest.LabManifest, []Module, error) {
	m, err := loadManifest(labDir)
	if err != nil {
		return nil, nil, err
	}
	modules, err := loadModules(labDir)
	if err != nil {
		return nil, nil, err
	}
	return m, modules, nil
}

func loadManifest(labDir string) (*manifest.LabManifest, error) {
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

	return m, nil
}

func loadModules(labDir string) ([]Module, error) {
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
		contentInfo, statErr := os.Lstat(contentDir)
		if statErr != nil || !contentInfo.IsDir() {
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
			var file fs.FileInfo
			file, err = d.Info()
			if err != nil || !file.Mode().IsRegular() {
				return errors.New("non-regular module file")
			}
			var rel string
			rel, err = filepath.Rel(contentDir, p)
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
		moduleYAML, readErr := os.ReadFile(filepath.Join(moduleDir, "module.yaml"))
		if readErr == nil {
			if _, labErr := manifest.ValidateModule(moduleYAML, pages); labErr != nil {
				return nil, fmt.Errorf("%w: invalid module.yaml", ErrLabInvalid)
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: read module.yaml", ErrLabInvalid)
		}
		modules = append(modules, Module{ID: entry.Name(), Pages: pages})
	}
	slices.SortFunc(modules, func(a, b Module) int { return strings.Compare(a.ID, b.ID) })

	return modules, nil
}

func isIncludeOnly(page string) bool {
	return strings.HasPrefix(page, "_") || strings.Contains(page, "/_")
}
