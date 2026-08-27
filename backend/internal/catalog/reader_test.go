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

func TestReadCatalog_ValidEntry(t *testing.T) {
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
	assert.Equal(t, "My Lab", e.DisplayName)
	assert.Equal(t, "https://github.com/redhat-scholars/my-lab", e.Repository)
	assert.Equal(t, "main", e.Branch)
	assert.Equal(t, "lab/", e.LabPath)
	assert.Equal(t, "Optional", e.Description)
}

func TestReadCatalog_EmptySequence(t *testing.T) {
	cm := catalogCM("[]")
	entries, err := catalog.Read(cm)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestReadCatalog_EmptyDocument(t *testing.T) {
	cm := catalogCM("")
	entries, err := catalog.Read(cm)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestReadCatalog_NullValue(t *testing.T) {
	cm := catalogCM("null")
	_, err := catalog.Read(cm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration_error")
}

func TestReadCatalog_MissingCatalogYAML(t *testing.T) {
	cm := &corev1.ConfigMap{Data: nil}
	_, err := catalog.Read(cm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration_error")
}

func TestReadCatalog_InvalidYAML(t *testing.T) {
	cm := catalogCM(": invalid: yaml: {{{")
	_, err := catalog.Read(cm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration_error")
}

// TestReadCatalog_MissingID is removed: id field is no longer required (catalogRef is derived).
// Duplicate ref detection is covered by TestReadCatalog_DuplicateRef.

func TestReadCatalog_OrderPreserved(t *testing.T) {
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
	assert.Equal(t, "Lab Z", entries[0].DisplayName)
	assert.Equal(t, "Lab A", entries[1].DisplayName)
	assert.Equal(t, "Lab M", entries[2].DisplayName)
}

func TestReadCatalog_MissingDisplayName_ReturnsError(t *testing.T) {
	cm := catalogCM(`- repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"`)
	_, err := catalog.Read(cm)
	require.Error(t, err)
}

func TestReadCatalog_MissingRepository_ReturnsError(t *testing.T) {
	cm := catalogCM(`- displayName: "Bad Lab"
  branch: "main"
  labPath: "lab/"`)
	_, err := catalog.Read(cm)
	require.Error(t, err)
}

// --- New tests for catalogRef derivation (must be RED before implementation) ---

func TestDeriveCatalogRef_Deterministic(t *testing.T) {
	ref1 := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
	ref2 := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
	assert.Equal(t, ref1, ref2)
	assert.Len(t, ref1, 64, "expected 64-char hex SHA-256")
}

func TestDeriveCatalogRef_ExcludesDisplayFields(t *testing.T) {
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
}

func TestDeriveCatalogRef_PointerChanges(t *testing.T) {
	base := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
	assert.NotEqual(t, base, catalog.DeriveRef("https://github.com/redhat/other", "main", "lab/", nil), "repository must affect ref")
	assert.NotEqual(t, base, catalog.DeriveRef("https://github.com/redhat/lab", "dev", "lab/", nil), "branch must affect ref")
	assert.NotEqual(t, base, catalog.DeriveRef("https://github.com/redhat/lab", "main", "other/", nil), "labPath must affect ref")
}

func TestDeriveCatalogRef_OverrideChangesRef(t *testing.T) {
	without := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", nil)
	with := catalog.DeriveRef("https://github.com/redhat/lab", "main", "lab/", map[string]string{"k": "v"})
	assert.NotEqual(t, without, with, "attributeOverrides must affect ref")
}

func TestReadCatalog_NoYAMLIdRequired(t *testing.T) {
	cm := catalogCM(`- displayName: "My Lab"
  repository: "https://github.com/redhat-scholars/my-lab"
  branch: "main"
  labPath: "lab/"`)
	entries, err := catalog.Read(cm)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.NotEmpty(t, entries[0].CatalogRef)
	assert.Len(t, entries[0].CatalogRef, 64)
}

func TestReadCatalog_DuplicateRef(t *testing.T) {
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
}

func TestReadCatalog_AttributeOverrides(t *testing.T) {
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
}

func TestReadCatalog_MultipleEntries(t *testing.T) {
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
}
