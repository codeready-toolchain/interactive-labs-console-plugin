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
	for _, tc := range []struct{ name, root, labPath string }{
		{"nested in plugin repository", repositoryRoot, "examples/sample-lab"},
		{"published at standalone repository root", sampleRoot, "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("bundle metadata and navigation", func(t *testing.T) {
				// Given either sample layout, when loading, then partials stay out of navigation.
				result, err := bundle.LoadFromSnapshot(tc.root, tc.labPath)
				require.NoError(t, err)
				assert.Equal(t, "Interactive Labs sample", result.Manifest.Metadata.Title)
				assert.Equal(t, []bundle.Module{{ID: "010-intro", Pages: []string{"010-start.adoc", "020-resources.adoc"}}}, result.Modules)
			})
			t.Run("chart discovery", func(t *testing.T) {
				// Given either layout, when loading, then the lab-local chart is available.
				result, err := bundle.LoadFromSnapshot(tc.root, tc.labPath)
				require.NoError(t, err)
				assert.True(t, result.HelmChartAvailable)
			})
			t.Run("PNG resource decoding", func(t *testing.T) {
				// Given the sample image path, when reading, then resource bytes decode as PNG.
				image, err := bundle.ReadResource(tc.root, filepath.ToSlash(filepath.Join(tc.labPath, "modules/010-intro/content/images/flow.png")))
				require.NoError(t, err)
				_, err = png.Decode(bytes.NewReader(image.Bytes))
				require.NoError(t, err)
			})
			t.Run("setup selected chart and bindings", func(t *testing.T) {
				// Given either layout, when preparing, then the local chart has flat typed bindings.
				plan, err := setup.Prepare(tc.root, tc.labPath, "learner", "sample")
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(sampleRoot, "setup", "helm"), plan.ChartDir)
				assert.Equal(t, []setup.SchemaBinding{{Key: "demo", Path: "namespaces.demo", Kind: setup.KindNamespaceName}}, setup.BindingsByKind(plan.Bindings, setup.KindNamespaceName))
				assert.Equal(t, []setup.SchemaBinding{{Key: "password", Path: "demo.password", Kind: setup.KindGeneratedPassword}}, setup.BindingsByKind(plan.Bindings, setup.KindGeneratedPassword))
				assert.Equal(t, []setup.SchemaBinding{{Key: "rootUrl", Path: "demo.rootUrl", Kind: setup.KindRootURL, RouteName: "sample", NamespaceRef: "demo"}}, setup.BindingsByKind(plan.Bindings, setup.KindRootURL))
			})
		})
	}
	t.Run("valid Helm rendering", func(t *testing.T) {
		// Given representative setup values, when rendering offline, then both resources use them.
		chart, err := loader.Load(filepath.Join(sampleRoot, "setup", "helm"))
		require.NoError(t, err)
		values, err := setup.BuildValues(map[string]string{"namespaces.demo": "sample-demo", "demo.password": "example-only", "demo.rootUrl": "sample-sample-demo.apps.example.test"})
		require.NoError(t, err)
		release := chartutil.ReleaseOptions{Name: "sample", Namespace: "learner-dev", IsInstall: true}
		renderValues, err := chartutil.ToRenderValues(chart, values, release, chartutil.DefaultCapabilities)
		require.NoError(t, err)
		rendered, err := engine.Render(chart, renderValues)
		require.NoError(t, err)
		resources := rendered["interactive-labs-sample/templates/resources.yaml"]
		assert.Contains(t, resources, "kind: ConfigMap")
		assert.Contains(t, resources, "kind: Secret")
		assert.Contains(t, resources, "namespace: \"sample-demo\"")
		assert.Contains(t, resources, "root-url: \"sample-sample-demo.apps.example.test\"")
		assert.Contains(t, resources, "password: \"example-only\"")
	})
	t.Run("missing values schema rejection", func(t *testing.T) {
		// Given no setup values, when Helm validates, then required strings fail minimum length.
		chart, err := loader.Load(filepath.Join(sampleRoot, "setup", "helm"))
		require.NoError(t, err)
		_, err = chartutil.ToRenderValues(chart, nil, chartutil.ReleaseOptions{Name: "sample", Namespace: "learner-dev", IsInstall: true}, chartutil.DefaultCapabilities)
		require.ErrorContains(t, err, "minLength")
	})
}
