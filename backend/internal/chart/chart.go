// Package chart validates content shared by bundle loading and setup.
package chart

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"helm.sh/helm/v3/pkg/chart/loader"
)

var encodedPathText = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)

// Chart is validated chart content, with no provisioning requirements or identity.
// Dir is empty for a content-only lab; Schema is nil when its optional file is absent.
type Chart struct {
	Dir    string // absolute selected chart directory
	Schema *Schema
}

// LoadChart selects and validates a chart within an approved repository snapshot.
// Nonempty override is lab-relative and authoritative; otherwise local setup/helm
// wins, with root setup/helm used only if the local directory is absent.
func LoadChart(repositoryRoot, labDir, override string) (*Chart, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve chart snapshot: %w", err)
	}
	labDir, err = filepath.Abs(labDir)
	if err != nil {
		return nil, fmt.Errorf("resolve chart lab: %w", err)
	}
	helmDir, err := resolveChartDir(root, labDir, override)
	if err != nil {
		return nil, fmt.Errorf("chart not resolved: %w", err)
	}
	chart := &Chart{Dir: helmDir}
	if helmDir == "" {
		return chart, nil
	}
	// Helm follows symlinks, even outside the chart. Check every entry before loading,
	// including entries Helm would skip through .helmignore.
	err = filepath.WalkDir(helmDir, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in chart")
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("unsafe chart tree: %w", err)
	}
	if _, err = loader.Load(helmDir); err != nil {
		return nil, fmt.Errorf("invalid Helm chart: %w", err)
	}
	schema, err := os.ReadFile(filepath.Join(helmDir, "values.schema.json"))
	if errors.Is(err, os.ErrNotExist) {
		return chart, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read values schema: %w", err)
	}
	chart.Schema, err = DecodeSchema(schema)
	if err != nil {
		return nil, err
	}
	return chart, nil
}

func resolveChartDir(root, labDir, override string) (string, error) {
	labPath, err := filepath.Rel(root, labDir)
	if err != nil {
		return "", err
	}
	candidates := []string{filepath.ToSlash(labPath) + "/setup/helm", "setup/helm"}
	if override != "" {
		parsed, err := url.Parse(override)
		if err != nil || parsed.IsAbs() || parsed.Host != "" || path.IsAbs(override) ||
			strings.IndexFunc(override, func(r rune) bool { return r < 32 || r == '\\' }) >= 0 || encodedPathText.MatchString(override) {
			return "", errors.New("unsafe chart override")
		}
		// Keep raw segments until after checking symlinks: alias/../chart must not
		// erase a symlink from the security check.
		candidates = []string{filepath.ToSlash(labPath) + "/" + override}
	}
	for _, candidate := range candidates {
		if err := rejectSymlinkPath(root, candidate); err != nil {
			if override == "" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		dir := filepath.Join(root, filepath.FromSlash(candidate))
		info, err := os.Lstat(dir) // rejectSymlinkPath bounds every segment and rejects symlinks.
		if err != nil || !info.IsDir() {
			return "", errors.New("chart target is not a directory")
		}
		return dir, nil
	}
	return "", nil
}

func rejectSymlinkPath(root, relativePath string) error {
	current := root
	segments := strings.Split(relativePath, "/")
	for i, segment := range segments {
		current = filepath.Join(current, segment)
		rel, err := filepath.Rel(root, current)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("chart target escapes repository")
		}
		info, err := os.Lstat(current) // filepath.Rel bounds every segment before inspection.
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(segments)-1 && !info.IsDir()) {
			return errors.New("unsafe chart target")
		}
	}
	return nil
}
