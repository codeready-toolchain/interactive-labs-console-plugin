package bundle_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
)

// TestLoadFromDir loads from a pre-populated temp directory (no real git required).
func TestLoadFromDir_ValidStructure(t *testing.T) {
	dir := t.TempDir()
	labPath := filepath.Join(dir, "lab")

	// Create lab directory structure
	require.NoError(t, os.MkdirAll(filepath.Join(labPath, "content"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(labPath, "setup", "helm"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(labPath, "lab.yaml"), []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: lab1
  displayName: "Lab 1"
spec: {}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(labPath, "content", "010-intro.adoc"), []byte("= Intro\n\nHello."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(labPath, "content", "_shared.adoc"), []byte("include"), 0o644))
	// Minimal helm chart
	require.NoError(t, os.WriteFile(filepath.Join(labPath, "setup", "helm", "Chart.yaml"), []byte("apiVersion: v2\nname: lab1\nversion: 0.1.0"), 0o644))

	result, err := bundle.LoadFromDir(labPath)
	require.NoError(t, err)
	assert.Equal(t, "lab1", result.Manifest.Metadata.Name)
	assert.Contains(t, result.Pages, "010-intro.adoc")
	assert.Contains(t, result.Pages, "_shared.adoc")
	assert.True(t, result.HelmChartAvailable)
}

func TestLoadFromDir_NoHelmChart(t *testing.T) {
	dir := t.TempDir()
	labPath := filepath.Join(dir, "lab")

	require.NoError(t, os.MkdirAll(filepath.Join(labPath, "content"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(labPath, "lab.yaml"), []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: lab2
  displayName: "Lab 2"
spec: {}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(labPath, "content", "010-step.adoc"), []byte("= Step"), 0o644))

	result, err := bundle.LoadFromDir(labPath)
	require.NoError(t, err)
	assert.False(t, result.HelmChartAvailable)
}

func TestValidateCloneArgs_InvalidScheme(t *testing.T) {
	err := bundle.ValidateCloneArgs("file:///etc/passwd", "main")
	require.Error(t, err)
}

func TestValidateCloneArgs_InvalidBranch(t *testing.T) {
	err := bundle.ValidateCloneArgs("https://github.com/org/repo", "main; rm -rf /")
	require.Error(t, err)
}

func TestValidateCloneArgs_Valid(t *testing.T) {
	require.NoError(t, bundle.ValidateCloneArgs("https://github.com/redhat-scholars/lab", "main"))
}
