package chart_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/chart"
)

func TestDecodeSchema(t *testing.T) {
	t.Run("decodes supported structure without provisioning policy", func(t *testing.T) {
		// Given correctly typed, possibly unknown annotations and unresolved references.
		schema, err := chart.DecodeSchema([]byte(`{"type":"object","properties":{"future":{"x-interactive-labs":{"kind":"future-kind"}},"app":{"properties":{"url":{"x-interactive-labs":{"kind":"root-url","routeName":"app","namespaceRef":"missing"}}}}},"unknown":42}`))
		// Then only structure is validated; provisioning interprets annotation values.
		require.NoError(t, err)
		assert.Equal(t, "future-kind", schema.Properties["future"].InteractiveLabs.Kind)
		assert.Equal(t, "missing", schema.Properties["app"].Properties["url"].InteractiveLabs.NamespaceRef)
	})
	t.Run("empty object is valid content contract", func(t *testing.T) {
		schema, err := chart.DecodeSchema([]byte("{}"))
		require.NoError(t, err)
		require.NotNil(t, schema)
		assert.Empty(t, schema.Properties)
	})
	for name, raw := range map[string]string{
		"malformed": "{", "null": "null", "array": "[]", "boolean": "true", "number": "42",
		"type": `{"type":42}`, "properties": `{"properties":[]}`, "property node": `{"properties":{"app":42}}`,
		"annotation object": `{"properties":{"app":{"x-interactive-labs":42}}}`,
		"kind":              `{"properties":{"app":{"x-interactive-labs":{"kind":42}}}}`,
		"route name":        `{"properties":{"app":{"x-interactive-labs":{"routeName":42}}}}`,
		"namespace ref":     `{"properties":{"app":{"x-interactive-labs":{"namespaceRef":42}}}}`,
		"nested annotation": `{"properties":{"app":{"properties":{"url":{"x-interactive-labs":{"kind":42}}}}}}`,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			schema, err := chart.DecodeSchema([]byte(raw))
			require.Error(t, err)
			assert.Nil(t, schema)
		})
	}
}

func TestLoadChart(t *testing.T) {
	t.Run("returns absolute selected chart and optional decoded schema only", func(t *testing.T) {
		// Given a lab selecting shared chart files inside its approved snapshot.
		root := t.TempDir()
		lab := filepath.Join(root, "lab")
		chartDir := filepath.Join(root, "shared/chart")
		require.NoError(t, os.MkdirAll(lab, 0o755))
		require.NoError(t, os.MkdirAll(chartDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: test\nversion: 1.0.0\n"), 0o600))
		// When loading without schema, chart selection has no setup requirements.
		loadedChart, err := chart.LoadChart(root, lab, "../shared/./chart/")
		require.NoError(t, err)
		assert.Equal(t, chartDir, loadedChart.Dir)
		assert.Nil(t, loadedChart.Schema)
		// Then adding an empty schema remains valid and is decoded once for consumers.
		require.NoError(t, os.WriteFile(filepath.Join(chartDir, "values.schema.json"), []byte("{}"), 0o600))
		loadedChart, err = chart.LoadChart(root, lab, "../shared/chart")
		require.NoError(t, err)
		require.NotNil(t, loadedChart.Schema)
		assert.Empty(t, loadedChart.Schema.Properties)
	})
}
