package manifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

func validManifestYAML() []byte {
	return []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  name: lab1
  displayName: "My Lab"
spec: {}`)
}

func TestValidateManifest(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		m, labErr := manifest.ValidateManifest(validManifestYAML())
		require.Nil(t, labErr)
		assert.Equal(t, "lab1", m.Metadata.Name)
		assert.Equal(t, "My Lab", m.Metadata.DisplayName)
	})

	t.Run("wrong API version", func(t *testing.T) {
		raw := []byte(`apiVersion: v2
kind: Lab
metadata:
  name: lab1
  displayName: "X"
spec: {}`)

		_, labErr := manifest.ValidateManifest(raw)
		require.NotNil(t, labErr)
		assert.Equal(t, "manifest_version_unsupported", labErr.ErrorType)
	})

	t.Run("missing API version", func(t *testing.T) {
		raw := []byte(`kind: Lab
metadata:
  name: lab1
  displayName: "X"
spec: {}`)

		_, labErr := manifest.ValidateManifest(raw)
		require.NotNil(t, labErr)
	})

	t.Run("missing metadata name", func(t *testing.T) {
		raw := []byte(`apiVersion: v1alpha1
kind: Lab
metadata:
  displayName: "X"
spec: {}`)

		_, labErr := manifest.ValidateManifest(raw)
		require.NotNil(t, labErr)
		assert.Equal(t, "manifest metadata.name is required", labErr.Message)
	})
}

func TestValidateBundlePaths(t *testing.T) {
	t.Run("path traversal", func(t *testing.T) {
		keys := []string{"010-intro.adoc", "../../etc/passwd"}
		err := manifest.ValidateBundlePaths(keys)
		require.NotNil(t, err)
		assert.Equal(t, "path_traversal", err.ErrorType)
	})

	t.Run("no navigable paths", func(t *testing.T) {
		keys := []string{"_shared/notice.adoc"}
		err := manifest.ValidateBundlePaths(keys)
		require.NotNil(t, err)
		assert.Equal(t, "content_error", err.ErrorType)
	})

	t.Run("valid paths", func(t *testing.T) {
		keys := []string{"010-intro.adoc", "_shared/notice.adoc", "020-install.adoc"}
		require.Nil(t, manifest.ValidateBundlePaths(keys))
	})
}
