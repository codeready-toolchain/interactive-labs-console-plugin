package bundle_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

func writeFile(t *testing.T, file, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(contents), 0o600))
}

func moduleLab(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	lab := filepath.Join(root, "lab")
	writeFile(t, filepath.Join(lab, "lab.yaml"), "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Test\n  description: ' Keep description '\nspec: {}\n")
	writeFile(t, filepath.Join(lab, "modules", "020-apply", "content", "z.adoc"), "= Apply")
	writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", "pages", "b.adoc"), "= B")
	writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", "pages", "a.adoc"), "= A")
	writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", "_partials", "include.adoc"), "= Include only")
	writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", "pages", "_part.adoc"), "= Include only")
	return root, lab
}

func TestLoadFromDir(t *testing.T) {
	t.Run("ordered modules and recursive pages with lab-wide chart", func(t *testing.T) {
		root, lab := moduleLab(t)
		writeFile(t, filepath.Join(root, "setup", "helm", "Chart.yaml"), "apiVersion: v2\nname: test\nversion: 0.1.0")
		writeFile(t, filepath.Join(lab, "modules", "010-intro", "module.yaml"), "checkpoints:\n  - id: first\n    type: manual\n    pagePath: ./pages/a.adoc\n    label: First\n")
		writeFile(t, filepath.Join(lab, "modules", "020-apply", "module.yaml"), "checkpoints:\n  - id: first\n    type: manual\n    pagePath: z.adoc\n    label: First\n")

		result, err := bundle.LoadFromDir(lab, root)
		require.NoError(t, err)
		assert.Equal(t, "Test", result.Manifest.Metadata.Title)
		assert.Equal(t, " Keep description ", result.Manifest.Metadata.Description)
		require.Len(t, result.Modules, 2)
		assert.Equal(t, "010-intro", result.Modules[0].ID)
		assert.Equal(t, []string{"pages/a.adoc", "pages/b.adoc"}, result.Modules[0].Pages)
		assert.Equal(t, "020-apply", result.Modules[1].ID)
		assert.Equal(t, []string{"z.adoc"}, result.Modules[1].Pages)
		assert.True(t, result.HelmChartAvailable)
	})
	t.Run("single module without module.yaml or chart", func(t *testing.T) {
		root, lab := moduleLab(t)
		require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules", "020-apply")))
		result, err := bundle.LoadFromDir(lab, root)
		require.NoError(t, err)
		require.Len(t, result.Modules, 1)
		assert.Equal(t, "010-intro", result.Modules[0].ID)
		assert.False(t, result.HelmChartAvailable)
	})
	for name, change := range map[string]func(*testing.T, string){
		"missing modules": func(t *testing.T, lab string) { require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules"))) },
		"empty modules": func(t *testing.T, lab string) {
			require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules")))
			require.NoError(t, os.Mkdir(filepath.Join(lab, "modules"), 0o755))
		},
		"module root has a file": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "note.txt"), "not a directory")
		},
		"invalid module id": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "_bad", "content", "a.adoc"), "= Bad")
		},
		"missing content": func(t *testing.T, lab string) {
			require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules", "010-intro", "content")))
		},
		"empty content": func(t *testing.T, lab string) {
			require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules", "010-intro", "content")))
			require.NoError(t, os.Mkdir(filepath.Join(lab, "modules", "010-intro", "content"), 0o755))
		},
		"only include-only pages": func(t *testing.T, lab string) {
			require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules", "010-intro", "content", "pages")))
		},
		"backslash in page filename": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", `bad\page.adoc`), "= Bad")
		},
		"encoded traversal text in page filename": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", "%2e%2e.adoc"), "= Bad")
		},
		"control in page filename": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", "bad\npage.adoc"), "= Bad")
		},
		"backslash in content directory": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", `bad\dir`, "page.adoc"), "= Bad")
		},
		"module yaml invalid": func(t *testing.T, lab string) {
			writeFile(t, filepath.Join(lab, "modules", "010-intro", "module.yaml"), "checkpoints:\n  - id: first\n    type: manual\n    pagePath: ../020-apply/content/z.adoc\n    label: First\n")
		},
		"module yaml directory": func(t *testing.T, lab string) {
			require.NoError(t, os.Mkdir(filepath.Join(lab, "modules", "010-intro", "module.yaml"), 0o755))
		},
		"symlink module file": func(t *testing.T, lab string) {
			require.NoError(t, os.Symlink(filepath.Join(lab, "lab.yaml"), filepath.Join(lab, "modules", "010-intro", "content", "_include.adoc")))
		},
		"symlink module directory": func(t *testing.T, lab string) {
			require.NoError(t, os.Symlink(filepath.Join(lab, "modules", "010-intro"), filepath.Join(lab, "modules", "030-link")))
		},
		"symlink modules root": func(t *testing.T, lab string) {
			require.NoError(t, os.Rename(filepath.Join(lab, "modules"), filepath.Join(lab, "real-modules")))
			require.NoError(t, os.Symlink(filepath.Join(lab, "real-modules"), filepath.Join(lab, "modules")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, lab := moduleLab(t)
			change(t, lab)
			_, err := bundle.LoadFromDir(lab, root)
			require.ErrorIs(t, err, bundle.ErrLabInvalid)
		})
	}
	t.Run("page with hash and question mark remains navigable", func(t *testing.T) {
		root, lab := moduleLab(t)
		page := "page#topic?.adoc"
		writeFile(t, filepath.Join(lab, "modules", "010-intro", "content", page), "= Page")
		result, err := bundle.LoadFromSnapshot(root, "lab")
		require.NoError(t, err)
		assert.Contains(t, result.Modules[0].Pages, page)
		resource, err := bundle.ReadResource(root, filepath.Join("lab", "modules", "010-intro", "content", page))
		require.NoError(t, err)
		assert.Equal(t, []byte("= Page"), resource.Bytes)
	})
	t.Run("manifest symlink outside lab rejected", func(t *testing.T) {
		root, lab := moduleLab(t)
		require.NoError(t, os.Remove(filepath.Join(lab, "lab.yaml")))
		writeFile(t, filepath.Join(root, "outside.yaml"), "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Outside\n")
		require.NoError(t, os.Symlink(filepath.Join(root, "outside.yaml"), filepath.Join(lab, "lab.yaml")))
		_, err := bundle.LoadFromDir(lab, root)
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
	})
	t.Run("invalid manifest is lab invalid", func(t *testing.T) {
		root, lab := moduleLab(t)
		writeFile(t, filepath.Join(lab, "lab.yaml"), "apiVersion: invalid\nkind: Lab\nmetadata:\n  title: Test\n")
		_, err := bundle.LoadFromDir(lab, root)
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
	})
}

func TestValidateLabPathRejectsUnsupportedSegments(t *testing.T) {
	for _, labPath := range []string{`lab\other`, "lab/%2e/other", "lab/%2E%2E/other", "lab/with\nnewline", "lab/with\x00nul"} { //nolint: misspell
		t.Run(labPath, func(t *testing.T) {
			require.Error(t, bundle.ValidateLabPath(labPath))
		})
	}
	for _, labPath := range []string{".", "lab/", "lab/section#topic?"} {
		t.Run("valid "+labPath, func(t *testing.T) {
			require.NoError(t, bundle.ValidateLabPath(labPath))
		})
	}
}

func TestValidateCloneArgs(t *testing.T) {
	t.Run("invalid scheme", func(t *testing.T) {
		require.Error(t, bundle.ValidateCloneArgs("file:///etc/passwd", "main"))
	})
	t.Run("invalid branch", func(t *testing.T) {
		require.Error(t, bundle.ValidateCloneArgs("https://github.com/org/repo", "main; rm -rf /"))
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

func TestLoadFromSnapshot_RejectsSymlinkedLabPathInsideRepository(t *testing.T) {
	root, lab := moduleLab(t)
	require.True(t, filepath.IsAbs(root))
	_, err := bundle.LoadFromSnapshot(root, "lab")
	require.NoError(t, err)
	for name, target := range map[string]string{
		"lab alias":    lab,
		"parent alias": root,
	} {
		t.Run(name, func(t *testing.T) {
			alias := filepath.Join(root, name)
			require.NoError(t, os.Symlink(target, alias))
			labPath := name
			if name == "parent alias" {
				labPath = filepath.Join(name, "lab")
			}
			_, err := bundle.LoadFromSnapshot(root, labPath)
			require.ErrorIs(t, err, bundle.ErrLabInvalid)
		})
	}
}

func TestReadResource_LoadsArbitrarySizeAndBytes(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte{0xff}, 10*1024*1024+1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "binary"), content, 0o600))

	resource, err := bundle.ReadResource(root, "binary")

	require.NoError(t, err)
	assert.Equal(t, content, resource.Bytes)
}

const validChart = "apiVersion: v2\nname: test\nversion: 1.0.0\n"

func chartOverride(t *testing.T, lab, override string) {
	t.Helper()
	writeFile(t, filepath.Join(lab, "lab.yaml"), fmt.Sprintf("apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Test\nspec:\n  setupDir: %q\n", override))
}

func TestPrepareSetup(t *testing.T) {
	t.Run("content loading excludes chart and setup policy", func(t *testing.T) {
		// Given valid lab content with an invalid selected chart.
		root, lab := moduleLab(t)
		writeFile(t, filepath.Join(lab, "setup/helm/Chart.yaml"), "name: broken\nversion: invalid\n")
		// When the provisioning consumer loads content through its minimal seam.
		m, modules, err := bundle.LoadContentFromSnapshot(root, "lab")
		// Then content loading stays independent; browser loading enforces chart contract.
		require.NoError(t, err)
		assert.Equal(t, "Test", m.Metadata.Title)
		require.Len(t, modules, 2)
		_, err = bundle.LoadFromSnapshot(root, "lab")
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
		_, err = setup.Prepare(root, "lab", "user1", "ref1")
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
	})
	t.Run("prepares selected chart bindings and final identity without cluster work", func(t *testing.T) {
		root, lab := moduleLab(t)
		chartOverride(t, lab, "../shared/chart")
		writeFile(t, filepath.Join(root, "shared/chart/Chart.yaml"), validChart)
		writeFile(t, filepath.Join(root, "shared/chart/values.schema.json"), `{"properties":{"lab":{"x-interactive-labs":{"kind":"namespace-name"}},"password":{"x-interactive-labs":{"kind":"generated-password"}},"url":{"x-interactive-labs":{"kind":"root-url","routeName":"app","namespaceRef":"lab"}}}}`)

		plan, err := setup.Prepare(root, "lab", "user1", "ref1")

		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, "shared/chart"), plan.ChartDir)
		assert.True(t, filepath.IsAbs(plan.ChartDir))
		assert.Equal(t, []setup.SchemaBinding{{Key: "lab", Path: "lab", Kind: setup.KindNamespaceName}}, setup.BindingsByKind(plan.Bindings, setup.KindNamespaceName))
		assert.Equal(t, []setup.SchemaBinding{{Key: "password", Path: "password", Kind: setup.KindGeneratedPassword}}, setup.BindingsByKind(plan.Bindings, setup.KindGeneratedPassword))
		assert.Equal(t, []setup.SchemaBinding{{Key: "url", Path: "url", Kind: setup.KindRootURL, RouteName: "app", NamespaceRef: "lab"}}, setup.BindingsByKind(plan.Bindings, setup.KindRootURL))
		assert.Equal(t, setup.HelmReleaseName("user1", "ref1", "shared/chart", plan.Bindings), plan.ReleaseName)
		cwd, err := os.Getwd()
		require.NoError(t, err)
		relativeRoot, err := filepath.Rel(cwd, root)
		require.NoError(t, err)
		relativePlan, err := setup.Prepare(relativeRoot, "lab", "user1", "ref1")
		require.NoError(t, err)
		assert.Equal(t, plan.ChartDir, relativePlan.ChartDir, "snapshot spelling must not change absolute chart directory")
		assert.Equal(t, plan.ReleaseName, relativePlan.ReleaseName, "snapshot spelling must not change identity")
		assert.ElementsMatch(t, plan.Bindings, relativePlan.Bindings)
	})
	for _, tc := range []struct {
		name, chart, schema string
		wantErr             error
	}{
		{name: "content only", wantErr: setup.ErrChartNotFound},
		{name: "optional bundle schema required for setup", chart: "setup/helm", wantErr: setup.ErrChartNotFound},
		{name: "namespace binding required", chart: "setup/helm", schema: "{}", wantErr: setup.ErrNamespaceBindingsMissing},
		{name: "invalid schema", chart: "setup/helm", schema: "{", wantErr: bundle.ErrLabInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := moduleLab(t)
			if tc.chart != "" {
				writeFile(t, filepath.Join(root, tc.chart, "Chart.yaml"), validChart)
			}
			if tc.schema != "" {
				writeFile(t, filepath.Join(root, tc.chart, "values.schema.json"), tc.schema)
			}
			if !errors.Is(tc.wantErr, bundle.ErrLabInvalid) {
				result, err := bundle.LoadFromSnapshot(root, "lab")
				require.NoError(t, err, "setup requirements must not invalidate bundle content")
				assert.Equal(t, tc.chart != "", result.HelmChartAvailable)
			}
			plan, err := setup.Prepare(root, "lab", "user1", "ref1")
			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, plan)
		})
	}
	for _, tc := range []struct {
		name, labPath string
		change        func(*testing.T, string, string)
		wantErr       error
	}{
		{name: "snapshot missing", labPath: "lab", change: func(t *testing.T, root, _ string) { require.NoError(t, os.RemoveAll(root)) }, wantErr: bundle.ErrRepositoryUnavailable},
		{name: "lab missing", labPath: "missing", wantErr: bundle.ErrManifestMissing},
		{name: "traversing lab", labPath: "../lab", wantErr: bundle.ErrLabInvalid},
		{name: "symlink lab", labPath: "alias", change: func(t *testing.T, root, lab string) {
			require.NoError(t, os.Symlink(lab, filepath.Join(root, "alias")))
		}, wantErr: bundle.ErrLabInvalid},
		{name: "invalid content", labPath: "lab", change: func(t *testing.T, _, lab string) { require.NoError(t, os.RemoveAll(filepath.Join(lab, "modules"))) }, wantErr: bundle.ErrLabInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, lab := moduleLab(t)
			if tc.change != nil {
				tc.change(t, root, lab)
			}
			plan, err := setup.Prepare(root, tc.labPath, "user1", "ref1")
			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, plan)
		})
	}
}

func TestChartSelection(t *testing.T) {
	for _, tc := range []struct {
		name, override, selected string
		charts                   []string
	}{
		{name: "content only"},
		{name: "root fallback", charts: []string{"setup/helm"}, selected: "setup/helm"},
		{name: "lab local wins", charts: []string{"setup/helm", "lab/setup/helm"}, selected: "lab/setup/helm"},
		{name: "explicit wins", override: "custom", charts: []string{"setup/helm", "lab/setup/helm", "lab/custom"}, selected: "lab/custom"},
		{name: "safe parent traversal", override: "../shared/chart", charts: []string{"shared/chart"}, selected: "shared/chart"},
		{name: "equivalent spelling", override: "./../shared/./chart/", charts: []string{"shared/chart"}, selected: "shared/chart"},
		{name: "empty override uses defaults", override: "", charts: []string{"lab/setup/helm"}, selected: "lab/setup/helm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given valid module content and distinct chart/schema pairs.
			root, lab := moduleLab(t)
			chartOverride(t, lab, tc.override)
			for _, chart := range tc.charts {
				writeFile(t, filepath.Join(root, chart, "Chart.yaml"), validChart)
				writeFile(t, filepath.Join(root, chart, "values.schema.json"), fmt.Sprintf(`{"properties":{%q:{"x-interactive-labs":{"kind":"namespace-name"}}}}`, chart))
			}
			// When loading through the shared snapshot boundary.
			result, err := bundle.LoadFromSnapshot(root, "lab")
			// Then browser gets availability, while setup gets only its prepared data.
			require.NoError(t, err)
			assert.Equal(t, tc.selected != "", result.HelmChartAvailable)
			plan, err := setup.Prepare(root, "lab", "user1", "ref1")
			if tc.selected == "" {
				require.ErrorIs(t, err, setup.ErrChartNotFound)
				assert.Nil(t, plan)
			} else {
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(root, tc.selected), plan.ChartDir)
				assert.Equal(t, []setup.SchemaBinding{{Key: tc.selected, Path: tc.selected, Kind: setup.KindNamespaceName}}, setup.BindingsByKind(plan.Bindings, setup.KindNamespaceName))
				assert.Equal(t, setup.HelmReleaseName("user1", "ref1", tc.selected, plan.Bindings), plan.ReleaseName)
			}
		})
	}
	t.Run("lab at repository root", func(t *testing.T) {
		_, root := moduleLab(t)
		writeFile(t, filepath.Join(root, "setup/helm/Chart.yaml"), validChart)
		writeFile(t, filepath.Join(root, "setup/helm/values.schema.json"), `{"properties":{"lab":{"x-interactive-labs":{"kind":"namespace-name"}}}}`)
		result, err := bundle.LoadFromSnapshot(root, ".")
		require.NoError(t, err)
		assert.True(t, result.HelmChartAvailable)
		plan, err := setup.Prepare(root, ".", "user1", "ref1")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, "setup/helm"), plan.ChartDir)
		assert.Equal(t, setup.HelmReleaseName("user1", "ref1", "setup/helm", plan.Bindings), plan.ReleaseName)
	})
	t.Run("schema is optional", func(t *testing.T) {
		root, _ := moduleLab(t)
		writeFile(t, filepath.Join(root, "setup/helm/Chart.yaml"), validChart)
		result, err := bundle.LoadFromSnapshot(root, "lab")
		require.NoError(t, err)
		assert.True(t, result.HelmChartAvailable)
		plan, err := setup.Prepare(root, "lab", "user1", "ref1")
		require.ErrorIs(t, err, setup.ErrChartNotFound)
		assert.Nil(t, plan)
	})
	for _, tc := range []struct {
		name, override string
		change         func(*testing.T, string, string)
	}{
		{name: "explicit missing", override: "missing"},
		{name: "explicit points directly at chart not setup parent", override: "setup", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup/helm/Chart.yaml"), validChart)
		}},
		{name: "invalid explicit does not use either default", override: "custom", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup/helm/Chart.yaml"), validChart)
			writeFile(t, filepath.Join(lab, "custom/Chart.yaml"), "name: test\nversion: bad\n")
		}},
		{name: "missing explicit does not use local default", override: "missing", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup/helm/Chart.yaml"), validChart)
		}},
		{name: "repository escape", override: "../../outside"},
		{name: "absolute", override: "/tmp/chart"},
		{name: "URL", override: "https://example.test/chart"},
		{name: "backslash", override: `..\shared`},
		{name: "encoded traversal", override: "../%2e%2e/chart"},
		{name: "control", override: "bad\npath"},
		{name: "selected local directory empty", change: func(t *testing.T, _, lab string) {
			require.NoError(t, os.MkdirAll(filepath.Join(lab, "setup/helm"), 0o755))
		}},
		{name: "selected local is file", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup/helm"), "not a directory")
		}},
		{name: "selected local parent is file", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup"), "not a directory")
		}},
		{name: "selected local malformed chart", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup/helm/Chart.yaml"), "name: test\nversion: not-semver\n")
		}},
		{name: "selected local malformed values", change: func(t *testing.T, _, lab string) {
			writeFile(t, filepath.Join(lab, "setup/helm/Chart.yaml"), validChart)
			writeFile(t, filepath.Join(lab, "setup/helm/values.yaml"), "[")
		}},
		{name: "malformed schema", change: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "setup/helm/values.schema.json"), "{")
		}},
		{name: "nonobject schema", change: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "setup/helm/values.schema.json"), "null")
		}},
		{name: "invalid binding schema", change: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "setup/helm/values.schema.json"), `{"properties":[]}`)
		}},
		{name: "mistyped annotation", change: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "setup/helm/values.schema.json"), `{"properties":{"lab":{"x-interactive-labs":{"kind":42}}}}`)
		}},
		{name: "nested mistyped route name", change: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "setup/helm/values.schema.json"), `{"properties":{"app":{"properties":{"url":{"x-interactive-labs":{"kind":"root-url","routeName":42}}}}}}`)
		}},
		{name: "schema directory", change: func(t *testing.T, root, _ string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "setup/helm/values.schema.json"), 0o755))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a valid fallback, invalid selected configuration must not use it.
			root, lab := moduleLab(t)
			writeFile(t, filepath.Join(root, "setup/helm/Chart.yaml"), validChart)
			chartOverride(t, lab, tc.override)
			if tc.change != nil {
				tc.change(t, root, lab)
			}
			_, err := bundle.LoadFromSnapshot(root, "lab")
			require.ErrorIs(t, err, bundle.ErrLabInvalid)
			_, err = setup.Prepare(root, "lab", "user1", "ref1")
			require.ErrorIs(t, err, bundle.ErrLabInvalid)
		})
	}
	t.Run("invalid root chart is not content only", func(t *testing.T) {
		root, _ := moduleLab(t)
		writeFile(t, filepath.Join(root, "setup/helm/Chart.yaml"), "name: test\nversion: bad\n")
		_, err := bundle.LoadFromSnapshot(root, "lab")
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
		_, err = setup.Prepare(root, "lab", "user1", "ref1")
		require.ErrorIs(t, err, bundle.ErrLabInvalid)
	})
}

func TestChartSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name, link, target, override string
	}{
		{name: "implicit root directory", link: "setup/helm", target: "shared/chart"},
		{name: "implicit local directory", link: "lab/setup/helm", target: "shared/chart"},
		{name: "explicit directory", link: "lab/custom", target: "shared/chart", override: "custom"},
		{name: "explicit parent", link: "alias", target: "shared", override: "../alias/chart"},
		{name: "canceled symlink segment", link: "alias", target: "shared", override: "../alias/../shared/chart"},
		{name: "chart metadata", link: "shared/chart/Chart.yaml", target: "metadata.yaml", override: "../shared/chart"},
		{name: "schema", link: "shared/chart/values.schema.json", target: "schema.json", override: "../shared/chart"},
		{name: "template file", link: "shared/chart/templates/link.yaml", target: "metadata.yaml", override: "../shared/chart"},
		{name: "template directory", link: "shared/chart/templates", target: "shared/templates", override: "../shared/chart"},
		{name: "subchart directory", link: "shared/chart/charts/dependency", target: "shared/templates", override: "../shared/chart"},
		{name: "ignored link", link: "shared/chart/ignored", target: "metadata.yaml", override: "../shared/chart"},
		{name: "dangling link", link: "shared/chart/dangling", target: "missing", override: "../shared/chart"},
		{name: "outside repository", link: "shared/chart/outside", override: "../shared/chart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, lab := moduleLab(t)
			writeFile(t, filepath.Join(root, "shared/chart/Chart.yaml"), validChart)
			writeFile(t, filepath.Join(root, "shared/chart/.helmignore"), "ignored\n")
			writeFile(t, filepath.Join(root, "shared/templates/file.yaml"), "kind: ConfigMap\n")
			writeFile(t, filepath.Join(root, "metadata.yaml"), validChart)
			writeFile(t, filepath.Join(root, "schema.json"), "{}")
			chartOverride(t, lab, tc.override)
			link := filepath.Join(root, tc.link)
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
			if tc.link == "shared/chart/Chart.yaml" {
				require.NoError(t, os.Remove(link))
			}
			target := filepath.Join(root, tc.target)
			if tc.target == "" {
				target = filepath.Join(t.TempDir(), "secret")
				writeFile(t, target, "outside data")
			}
			require.NoError(t, os.Symlink(target, link))
			_, err := bundle.LoadFromSnapshot(root, "lab")
			require.ErrorIs(t, err, bundle.ErrLabInvalid)
			_, err = setup.Prepare(root, "lab", "user1", "ref1")
			require.ErrorIs(t, err, bundle.ErrLabInvalid)
		})
	}
}
