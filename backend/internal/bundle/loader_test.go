package bundle_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
)

func TestLoadFromDir(t *testing.T) {
	t.Run("valid structure", func(t *testing.T) {
		repoRoot := t.TempDir()
		labPath := filepath.Join(repoRoot, "lab")

		require.NoError(t, os.MkdirAll(filepath.Join(labPath, "content"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(labPath, "lab.yaml"), []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: lab1
  displayName: "Lab 1"
  description: "Manifest description"
spec: {}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(labPath, "content", "010-intro.adoc"), []byte("= Intro\n\nHello."), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(labPath, "content", "_shared.adoc"), []byte("include"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "setup", "helm"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "setup", "helm", "Chart.yaml"), []byte("apiVersion: v2\nname: lab1\nversion: 0.1.0"), 0o600))

		result, err := bundle.LoadFromDir(labPath, repoRoot)
		require.NoError(t, err)
		assert.Equal(t, "lab1", result.Manifest.Metadata.Name)
		assert.Equal(t, "Lab 1", result.Manifest.Metadata.DisplayName)
		assert.Equal(t, "Manifest description", result.Manifest.Metadata.Description)
		assert.Equal(t, []string{"010-intro.adoc"}, result.Pages)
		assert.True(t, result.HelmChartAvailable)
	})

	t.Run("no helm chart", func(t *testing.T) {
		repoRoot := t.TempDir()
		labPath := filepath.Join(repoRoot, "lab")

		require.NoError(t, os.MkdirAll(filepath.Join(labPath, "content"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(labPath, "lab.yaml"), []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: lab2
  displayName: "Lab 2"
spec: {}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(labPath, "content", "010-step.adoc"), []byte("= Step"), 0o600))

		result, err := bundle.LoadFromDir(labPath, repoRoot)
		require.NoError(t, err)
		assert.False(t, result.HelmChartAvailable)
	})

	t.Run("invalid content is lab invalid", func(t *testing.T) {
		lab := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(lab, "content"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lab, "lab.yaml"), []byte(`apiVersion: invalid
kind: Lab
metadata:
  name: lab
  displayName: Lab
spec: {}`), 0o600))
		_, err := bundle.LoadFromDir(lab, lab)
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
	})
}

func TestValidateCloneArgs(t *testing.T) {
	t.Run("invalid scheme", func(t *testing.T) {
		err := bundle.ValidateCloneArgs("file:///etc/passwd", "main")
		require.Error(t, err)
	})

	t.Run("invalid branch", func(t *testing.T) {
		err := bundle.ValidateCloneArgs("https://github.com/org/repo", "main; rm -rf /")
		require.Error(t, err)
	})

	t.Run("rejects unsafe git ref", func(t *testing.T) {
		require.Error(t, bundle.ValidateCloneArgs("https://github.com/org/repo", "../main"))
	})

	t.Run("rejects credentials", func(t *testing.T) {
		require.Error(t, bundle.ValidateCloneArgs("https://token@github.com/org/repo", "main"))
	})

	t.Run("valid", func(t *testing.T) {
		require.NoError(t, bundle.ValidateCloneArgs("https://github.com/redhat-scholars/lab", "main"))
	})
}

func TestLoadFromSnapshot_ClassifiesContentAndContainsLabPath(t *testing.T) {
	root := t.TempDir()
	_, err := bundle.LoadFromSnapshot(root, "missing")
	require.ErrorIs(t, err, bundle.ErrManifestMissing)

	escaped := filepath.Join(t.TempDir(), "lab")
	require.NoError(t, os.MkdirAll(filepath.Join(escaped, "content"), 0o755))
	require.NoError(t, os.Symlink(escaped, filepath.Join(root, "outside")))
	_, err = bundle.LoadFromSnapshot(root, "outside")
	require.ErrorIs(t, err, bundle.ErrLabInvalid)
}

func TestReadResource_LoadsArbitrarySizeAndBytes(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte{0xff}, 10*1024*1024+1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "binary"), content, 0o600))

	resource, err := bundle.ReadResource(root, "binary")

	require.NoError(t, err)
	assert.Equal(t, content, resource.Bytes)
}
