// Package bundle loads a lab bundle from a cloned repository directory.
package bundle

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

// validBranchRE allows branch names that cannot contain shell-special characters.
// Allows: alphanumeric, dash, dot, underscore, forward-slash.
var validBranchRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// Result is the in-memory lab bundle loaded from disk.
type Result struct {
	Manifest           *manifest.LabManifest
	Pages              map[string]string // pagePath → AsciiDoc source
	HelmChartAvailable bool
}

// ValidateCloneArgs validates repository URL and branch before shell execution.
// Returns a non-nil error if either argument is unsafe.
func ValidateCloneArgs(repoURL, branch string) error {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("repository URL must use https scheme: %q", repoURL)
	}
	if u.Host == "" {
		return fmt.Errorf("repository URL must have a host: %q", repoURL)
	}
	if !validBranchRE.MatchString(branch) {
		return fmt.Errorf("branch contains invalid characters: %q", branch)
	}
	return nil
}

// CloneAndLoad shallow-clones the repository at repoURL and returns a loaded bundle.
// Uses exec.Command with a fixed argument vector — never a shell string.
// The caller must ensure repoURL and branch have been validated with ValidateCloneArgs.
func CloneAndLoad(repoURL, branch, labPath string) (*Result, error) {
	if err := ValidateCloneArgs(repoURL, branch); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "ilab-clone-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	// ponytail: no shell — args passed as a slice, never concatenated into a string
	cmd := exec.Command("git", "clone",
		"--depth", "1",
		"--branch", branch,
		"--", repoURL, tmpDir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %w: %s", err, out)
	}

	return LoadFromDir(filepath.Join(tmpDir, filepath.FromSlash(labPath)))
}

// LoadFromDir loads a lab bundle from an already-populated labPath on disk.
// Exported for testing without a real git remote.
func LoadFromDir(labDir string) (*Result, error) {
	// Parse manifest
	manifestBytes, err := os.ReadFile(filepath.Join(labDir, "lab.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read lab.yaml: %w", err)
	}
	m, labErr := manifest.ValidateManifest(manifestBytes)
	if labErr != nil {
		return nil, fmt.Errorf("invalid manifest: %s", labErr.Message)
	}

	// Load all .adoc files under content/
	pages := map[string]string{}
	contentDir := filepath.Join(labDir, "content")
	err = filepath.WalkDir(contentDir, func(p string, d fs.DirEntry, walkErr error) error {
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
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		pages[filepath.ToSlash(rel)] = string(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk content dir: %w", err)
	}

	// Check for Helm chart
	chartFile := filepath.Join(labDir, "setup", "helm", "Chart.yaml")
	_, helmErr := os.Stat(chartFile)
	helmAvailable := helmErr == nil

	return &Result{
		Manifest:           m,
		Pages:              pages,
		HelmChartAvailable: helmAvailable,
	}, nil
}
