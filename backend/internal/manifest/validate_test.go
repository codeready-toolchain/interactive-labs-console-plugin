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
  title: "My Lab"
  description: " Description kept "
spec:
  attributes:
    audience: developers`)
}

func TestValidateManifest(t *testing.T) {
	t.Run("valid metadata and attributes", func(t *testing.T) {
		m, labErr := manifest.ValidateManifest(validManifestYAML())
		require.Nil(t, labErr)
		assert.Equal(t, "My Lab", m.Metadata.Title)
		assert.Equal(t, " Description kept ", m.Metadata.Description)
		assert.Equal(t, map[string]string{"audience": "developers"}, m.Spec.Attributes)
	})

	t.Run("spec is optional", func(t *testing.T) {
		_, labErr := manifest.ValidateManifest([]byte("apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\n"))
		require.Nil(t, labErr)
	})

	for name, raw := range map[string]string{
		"missing title":       "apiVersion: v1alpha1\nkind: Lab\nmetadata: {}\n",
		"blank title":         "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: ' \t '\n",
		"old name":            "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\n  name: old\n",
		"old displayName":     "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\n  displayName: old\n",
		"root checkpoints":    "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\nspec:\n  checkpoints: []\n",
		"unknown root":        "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\nother: true\n",
		"unknown metadata":    "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\n  extra: yes\n",
		"unknown spec":        "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\nspec:\n  extra: yes\n",
		"nonstring attribute": "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\nspec:\n  attributes:\n    key: [a]\n",
		"nonmap attributes":   "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Lab\nspec:\n  attributes: []\n",
		"empty input":         "",
	} {
		t.Run(name, func(t *testing.T) {
			_, labErr := manifest.ValidateManifest([]byte(raw))
			require.NotNil(t, labErr)
			assert.Equal(t, "content_error", labErr.ErrorType)
		})
	}
	t.Run("unsupported api version remains typed", func(t *testing.T) {
		_, labErr := manifest.ValidateManifest([]byte("apiVersion: v2\nkind: Lab\nmetadata:\n  title: Lab\n"))
		require.NotNil(t, labErr)
		assert.Equal(t, "manifest_version_unsupported", labErr.ErrorType)
	})
}

func TestValidateModule(t *testing.T) {
	pages := []string{"010-start.adoc", "pages/020-next.adoc"}
	t.Run("valid and normalized pagePath", func(t *testing.T) {
		m, labErr := manifest.ValidateModule([]byte(`checkpoints:
  - id: first
    type: manual
    pagePath: ./pages/020-next.adoc
    label: " Keep spaces "`), pages)
		require.Nil(t, labErr)
		require.Len(t, m.Checkpoints, 1)
		assert.Equal(t, "pages/020-next.adoc", m.Checkpoints[0].PagePath)
		assert.Equal(t, " Keep spaces ", m.Checkpoints[0].Label)
	})
	t.Run("empty checkpoints allowed", func(t *testing.T) {
		m, labErr := manifest.ValidateModule([]byte("checkpoints: []\n"), pages)
		require.Nil(t, labErr)
		assert.Empty(t, m.Checkpoints)
	})
	for name, raw := range map[string]string{
		"missing checkpoints":      "{}",
		"null checkpoints":         "checkpoints: null",
		"not an array":             "checkpoints: {}",
		"extra key":                "checkpoints: []\ntitle: Other",
		"unknown checkpoint field": "checkpoints:\n  - id: first\n    type: manual\n    pagePath: 010-start.adoc\n    label: First\n    extra: no",
		"blank id":                 "checkpoints:\n  - id: ' '\n    type: manual\n    pagePath: 010-start.adoc\n    label: First",
		"blank label":              "checkpoints:\n  - id: first\n    type: manual\n    pagePath: 010-start.adoc\n    label: ' '\n",
		"blank path":               "checkpoints:\n  - id: first\n    type: manual\n    pagePath: ' '\n    label: First",
		"unsupported type":         "checkpoints:\n  - id: first\n    type: automated\n    pagePath: 010-start.adoc\n    label: First",
		"duplicate id":             "checkpoints:\n  - id: first\n    type: manual\n    pagePath: 010-start.adoc\n    label: First\n  - id: first\n    type: manual\n    pagePath: pages/020-next.adoc\n    label: Again",
		"include-only":             "checkpoints:\n  - id: first\n    type: manual\n    pagePath: _parts/partial.adoc\n    label: First",
		"missing page":             "checkpoints:\n  - id: first\n    type: manual\n    pagePath: absent.adoc\n    label: First",
		"traversal":                "checkpoints:\n  - id: first\n    type: manual\n    pagePath: pages/../010-start.adoc\n    label: First",
		"cross module":             "checkpoints:\n  - id: first\n    type: manual\n    pagePath: ../other/content/010-start.adoc\n    label: First",
		"absolute":                 "checkpoints:\n  - id: first\n    type: manual\n    pagePath: /010-start.adoc\n    label: First",
		"backslash":                "checkpoints:\n  - id: first\n    type: manual\n    pagePath: pages\\020-next.adoc\n    label: First",
	} {
		t.Run(name, func(t *testing.T) {
			_, labErr := manifest.ValidateModule([]byte(raw), pages)
			require.NotNil(t, labErr)
			assert.Equal(t, "content_error", labErr.ErrorType)
		})
	}
}

func TestValidateBundlePaths(t *testing.T) {
	t.Run("path traversal", func(t *testing.T) {
		err := manifest.ValidateBundlePaths([]string{"010-intro.adoc", "../../etc/passwd"})
		require.NotNil(t, err)
		assert.Equal(t, "path_traversal", err.ErrorType)
	})
	t.Run("no navigable paths", func(t *testing.T) {
		err := manifest.ValidateBundlePaths([]string{"_shared/notice.adoc"})
		require.NotNil(t, err)
		assert.Equal(t, "content_error", err.ErrorType)
	})
	t.Run("valid paths", func(t *testing.T) {
		require.Nil(t, manifest.ValidateBundlePaths([]string{"010-intro.adoc", "_shared/notice.adoc", "020-install.adoc"}))
	})
}
