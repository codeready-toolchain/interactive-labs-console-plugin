package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
)

func catalogCM(yamlContent string) *corev1.ConfigMap {
	return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": yamlContent}}
}

// TestReadCatalog_MissingID is removed: id field is no longer required (catalogRef is derived).
// Duplicate ref detection is covered by TestReadCatalog/duplicate ref.

func TestReadCatalog(t *testing.T) {
	t.Run("valid entry", func(t *testing.T) {
		cm := catalogCM(`- displayName: "My Lab"
  description: "Optional"
  repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"`)

		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		e := entries[0]
		assert.Len(t, e.CatalogRef, 64, "catalogRef must be 64-char hex")
		assert.Equal(t, "https://github.com/redhat-scholars/my-lab", e.Repository)
		assert.Equal(t, "main", e.Branch)
		assert.Equal(t, "lab/", e.LabPath)
	})

	t.Run("empty sequence", func(t *testing.T) {
		cm := catalogCM("[]")
		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("empty document", func(t *testing.T) {
		cm := catalogCM("")
		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("null value", func(t *testing.T) {
		cm := catalogCM("null")
		_, err := catalog.Read(cm)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "configuration_error")
	})

	t.Run("missing catalog YAML", func(t *testing.T) {
		cm := &corev1.ConfigMap{Data: nil}
		_, err := catalog.Read(cm)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "configuration_error")
	})

	t.Run("invalid YAML", func(t *testing.T) {
		cm := catalogCM(": invalid: yaml: {{{")
		_, err := catalog.Read(cm)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "configuration_error")
	})

	t.Run("order preserved", func(t *testing.T) {
		cm := catalogCM(`- displayName: "Lab Z"
  repository: "https://github.com/redhat-scholars/lab-z"
  branch: "main"
  labPath: "."
- displayName: "Lab A"
  repository: "https://github.com/redhat-scholars/lab-a"
  branch: "main"
  labPath: "."
- displayName: "Lab M"
  repository: "https://github.com/redhat-scholars/lab-m"
  branch: "main"
  labPath: "."`)

		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		require.Len(t, entries, 3)
		assert.Equal(t, "https://github.com/redhat-scholars/lab-z", entries[0].Repository)
		assert.Equal(t, "https://github.com/redhat-scholars/lab-a", entries[1].Repository)
		assert.Equal(t, "https://github.com/redhat-scholars/lab-m", entries[2].Repository)
	})

	t.Run("display fields are optional and ignored", func(t *testing.T) {
		without := catalogCM(`- repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"`)
		with := catalogCM(`- displayName: "Legacy Name"
  description: "Legacy description"
  repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"`)

		withoutEntries, err := catalog.Read(without)
		require.NoError(t, err)
		withEntries, err := catalog.Read(with)
		require.NoError(t, err)
		assert.Equal(t, withoutEntries, withEntries)
	})

	t.Run("missing required source field returns error", func(t *testing.T) {
		for _, content := range []string{
			`- branch: main
  labPath: lab`,
			`- repository: https://github.com/redhat/lab
  labPath: lab`,
			`- repository: https://github.com/redhat/lab
  branch: main`,
		} {
			_, err := catalog.Read(catalogCM(content))
			require.ErrorContains(t, err, "configuration_error")
		}
	})

	t.Run("rejects unsafe source locator", func(t *testing.T) {
		for _, content := range []string{
			`- repository: http://github.com/redhat/lab
  branch: main
  labPath: lab`,
			`- repository: https://github.com/redhat/lab
  branch: ../main
  labPath: lab`,
			`- repository: https://github.com/redhat/lab
  branch: main
  labPath: /lab`,
			`- repository: https://github.com/redhat/lab
  branch: main
  labPath: ../lab`,
			`- repository: https://github.com/redhat/lab
  branch: main
  labPath: lab/../other`,
			`- repository: https://github.com/redhat/lab
  branch: main
  labPath: lab/../../etc`,
		} {
			_, err := catalog.Read(catalogCM(content))
			require.ErrorContains(t, err, "configuration_error")
		}
	})

	t.Run("no YAML id required", func(t *testing.T) {
		cm := catalogCM(`- displayName: "My Lab"
  repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"`)
		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.NotEmpty(t, entries[0].CatalogRef)
		assert.Len(t, entries[0].CatalogRef, 64)
	})

	t.Run("duplicate ref", func(t *testing.T) {
		cm := catalogCM(`- displayName: "Lab A"
  repository: "https://github.com/redhat-scholars/lab"
  branch: "main"
  labPath: "."
- displayName: "Lab B"
  repository: "https://github.com/redhat-scholars/lab"
  branch: "main"
  labPath: "."`)
		_, err := catalog.Read(cm)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "configuration_error")
		assert.Contains(t, err.Error(), "duplicate")
	})

	t.Run("attribute overrides", func(t *testing.T) {
		cm := catalogCM(`- displayName: "My Lab"
  repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"
  attributeOverrides:
    key1: val1
    key2: val2`)
		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, map[string]string{"key1": "val1", "key2": "val2"}, entries[0].AttributeOverrides)
	})

	t.Run("multiple entries", func(t *testing.T) {
		cm := catalogCM(`- displayName: "Lab A"
  repository: "https://github.com/redhat-scholars/lab-a"
  branch: "main"
  labPath: "."
- displayName: "Lab B"
  repository: "https://github.com/redhat-scholars/lab-b"
  branch: "stable"
  labPath: "content/"`)

		entries, err := catalog.Read(cm)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
	})
}

func TestDeriveCatalogRef(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		ref1 := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
		ref2 := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
		assert.Equal(t, ref1, ref2)
		assert.Len(t, ref1, 64, "expected 64-char hex SHA-256")
	})

	t.Run("excludes display fields", func(t *testing.T) {
		cm1 := catalogCM(`- displayName: "Lab A"
  description: "Desc A"
  repository: "https://github.com/redhat/lab"
  branch: "main"
  labPath: "lab/"`)
		cm2 := catalogCM(`- displayName: "Lab B"
  description: "Desc B"
  repository: "https://github.com/redhat/lab"
  branch: "main"
  labPath: "lab/"`)
		e1, err1 := catalog.Read(cm1)
		e2, err2 := catalog.Read(cm2)
		require.NoError(t, err1)
		require.NoError(t, err2)
		assert.Equal(t, e1[0].CatalogRef, e2[0].CatalogRef, "displayName and description must not affect catalogRef")
	})

	t.Run("pointer changes", func(t *testing.T) {
		base := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
		assert.NotEqual(t, base, catalog.DeriveRef("https://github.com/redhat/other", "main", "lab/", nil), "repository must affect ref")
		assert.NotEqual(t, base, catalog.DeriveRef("https://github.com/redhat/lab", "dev", "lab/", nil), "branch must affect ref")
		assert.NotEqual(t, base, catalog.DeriveRef("https://github.com/redhat/lab", "main", "other/", nil), "labPath must affect ref")
	})

	t.Run("override changes ref", func(t *testing.T) {
		without := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
		with := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", map[string]string{"k": "v"})
		assert.NotEqual(t, without, with, "attributeOverrides must affect ref")
	})
}
