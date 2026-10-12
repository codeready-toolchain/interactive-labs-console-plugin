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
	}{
		{"nested in plugin repository", repositoryRoot, "examples/sample-lab"},
		{"published at standalone repository root", sampleRoot, "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("bundle metadata and navigation", func(t *testing.T) {
				// Given the sample repository root and lab path for this layout.
				root, labPath := tc.root, tc.labPath

				// When loading through the public bundle API.
				result, err := bundle.LoadFromSnapshot(root, labPath)

				// Then metadata identifies the sample and partials stay out of navigation.
				require.NoError(t, err)
				assert.Equal(t, "Interactive Labs sample", result.Manifest.Metadata.Title)
				assert.Equal(t, []bundle.Module{{ID: "010-intro", Pages: []string{"010-start.adoc", "020-resources.adoc"}}}, result.Modules)
			})

			t.Run("chart discovery", func(t *testing.T) {
				// Given the sample repository root and lab path for this layout.
				root, labPath := tc.root, tc.labPath

				// When loading through the public bundle API.
				result, err := bundle.LoadFromSnapshot(root, labPath)

				// Then the lab-local chart is available in either layout.
				require.NoError(t, err)
				assert.True(t, result.HelmChartAvailable)
			})

			t.Run("PNG resource decoding", func(t *testing.T) {
				// Given the sample image path within this repository layout.
				imagePath := filepath.ToSlash(filepath.Join(tc.labPath, "modules/010-intro/content/images/flow.png"))

				// When reading the resource through the public API and decoding its bytes.
				image, err := bundle.ReadResource(tc.root, imagePath)
				require.NoError(t, err)
				_, err = png.Decode(bytes.NewReader(image.Bytes))

				// Then the resource is a valid PNG.
				require.NoError(t, err)
			})

			t.Run("setup selected chart and bindings", func(t *testing.T) {
				// Given the sample layout and representative subject and catalog identities.
				root, labPath := tc.root, tc.labPath
				subject, catalogRef := "learner", "sample"

				// When preparing setup through the public API.
				plan, err := setup.Prepare(root, labPath, subject, catalogRef)

				// Then setup selects the lab-local chart and its namespace/generated-value bindings.
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(sampleRoot, "setup", "helm"), plan.ChartDir)
				assert.Equal(t, []setup.NamespaceBinding{{Key: "demo", Path: "namespaces.demo"}}, plan.Bindings.Namespaces)
				assert.Equal(t, []setup.ValueBinding{{Key: "password", Path: "demo.password", Kind: setup.KindGeneratedPassword}}, plan.Bindings.Passwords)
				assert.Equal(t, []setup.RootURLBinding{{Key: "rootUrl", Path: "demo.rootUrl", RouteName: "sample", NamespaceRef: "demo"}}, plan.Bindings.RootURLs)
			})
		})
	}

	t.Run("valid Helm rendering", func(t *testing.T) {
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
	})

	t.Run("missing values schema rejection", func(t *testing.T) {
		// Given the illustrative chart and release identity without setup values.
		chart, err := loader.Load(filepath.Join(sampleRoot, "setup", "helm"))
		require.NoError(t, err)
		release := chartutil.ReleaseOptions{Name: "sample", Namespace: "learner-dev", IsInstall: true}

		// When Helm validates missing values against the chart schema.
		_, err = chartutil.ToRenderValues(chart, nil, release, chartutil.DefaultCapabilities)

		// Then required string values fail minimum-length validation.
		require.ErrorContains(t, err, "minLength")
	})
}
