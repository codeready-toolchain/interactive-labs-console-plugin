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
	for _, labPath := range []string{`lab\other`, "lab/%2e/other", "lab/%2E%2E/other", "lab/with\nnewline", "lab/with\x00nul"} {
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
