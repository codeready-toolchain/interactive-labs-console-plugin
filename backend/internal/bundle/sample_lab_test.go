package bundle_test

import (
	"bytes"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

func TestSampleLab(t *testing.T) {
	repositoryRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	sampleRoot := filepath.Join(repositoryRoot, "examples", "sample-lab")

	for _, tc := range []struct {
		name, root, labPath string
		chartAvailable      bool
	}{
		{"nested in plugin repository", repositoryRoot, "examples/sample-lab", false},
		{"published at standalone repository root", sampleRoot, ".", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given the sample in either supported repository layout.
			// When loading through the public bundle API.
			result, err := bundle.LoadFromSnapshot(tc.root, tc.labPath)
			require.NoError(t, err)

			// Then partials stay out of navigation and only root-level setup is active.
			assert.Equal(t, "Interactive Labs sample", result.Manifest.Metadata.Title)
			assert.Equal(t, []bundle.Module{{ID: "010-intro", Pages: []string{"010-start.adoc", "020-resources.adoc"}}}, result.Modules)
			assert.Equal(t, tc.chartAvailable, result.HelmChartAvailable)

			image, err := bundle.ReadResource(tc.root, filepath.ToSlash(filepath.Join(tc.labPath, "modules/010-intro/content/images/flow.png")))
			require.NoError(t, err)
			_, err = png.Decode(bytes.NewReader(image.Bytes))
			require.NoError(t, err)

			if tc.chartAvailable {
				bindings, err := setup.ParseSchemaBindings(result.ValuesSchema)
				require.NoError(t, err)
				assert.Equal(t, []setup.SchemaBinding{{Key: "demo", Path: "namespaces.demo", Kind: setup.KindNamespaceName}}, setup.BindingsByKind(bindings, setup.KindNamespaceName))
				assert.Equal(t, []setup.SchemaBinding{{Key: "password", Path: "demo.password", Kind: setup.KindGeneratedPassword}}, setup.BindingsByKind(bindings, setup.KindGeneratedPassword))
				assert.Equal(t, []setup.SchemaBinding{{Key: "rootUrl", Path: "demo.rootUrl", Kind: setup.KindRootURL, RouteName: "sample", NamespaceRef: "demo"}}, setup.BindingsByKind(bindings, setup.KindRootURL))
			}
		})
	}

	t.Run("Helm schema and templates", func(t *testing.T) {
		// Given the illustrative chart and representative setup values.
		chart, err := loader.Load(filepath.Join(sampleRoot, "setup", "helm"))
		require.NoError(t, err)
		values, err := setup.BuildValues(map[string]string{
			"namespaces.demo": "sample-demo",
			"demo.password":   "example-only",
			"demo.rootUrl":    "sample-sample-demo.apps.example.test",
		})
		require.NoError(t, err)
		release := chartutil.ReleaseOptions{Name: "sample", Namespace: "learner-dev", IsInstall: true}

		// When Helm validates the schema and renders without contacting a cluster.
		renderValues, err := chartutil.ToRenderValues(chart, values, release, chartutil.DefaultCapabilities)
		require.NoError(t, err)
		rendered, err := engine.Render(chart, renderValues)
		require.NoError(t, err)

		// Then both resources use the provisioned namespace and generated values.
		resources := rendered["interactive-labs-sample/templates/resources.yaml"]
		assert.Contains(t, resources, "kind: ConfigMap")
		assert.Contains(t, resources, "kind: Secret")
		assert.Contains(t, resources, "namespace: \"sample-demo\"")
		assert.Contains(t, resources, "root-url: \"sample-sample-demo.apps.example.test\"")
		assert.Contains(t, resources, "password: \"example-only\"")
		_, err = chartutil.ToRenderValues(chart, nil, release, chartutil.DefaultCapabilities)
		require.ErrorContains(t, err, "minLength")
	})
}
