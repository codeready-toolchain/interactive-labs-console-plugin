package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHelmReleaseName(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		bindings := []NamespaceBinding{
			{Key: "lab", Path: "namespaces.lab"},
			{Key: "gitea", Path: "namespaces.gitea"},
		}
		name1 := HelmReleaseName("user1", "abc123", "setup/helm", bindings)
		name2 := HelmReleaseName("user1", "abc123", "setup/helm", bindings)
		assert.Equal(t, name1, name2)
		assert.LessOrEqual(t, len(name1), 53, "must fit Helm 53-char limit")
		assert.Regexp(t, `^il-[0-9a-f]{12}$`, name1)
	})

	t.Run("binding order irrelevant", func(t *testing.T) {
		asc := []NamespaceBinding{
			{Key: "a", Path: "namespaces.a"},
			{Key: "b", Path: "namespaces.b"},
		}
		desc := []NamespaceBinding{
			{Key: "b", Path: "namespaces.b"},
			{Key: "a", Path: "namespaces.a"},
		}
		assert.Equal(t, HelmReleaseName("u", "ref1", "setup/helm", asc), HelmReleaseName("u", "ref1", "setup/helm", desc))
	})

	t.Run("identity field boundaries are unambiguous", func(t *testing.T) {
		for _, tc := range []struct {
			name                        string
			left, right                 [3]string
			leftBindings, rightBindings []NamespaceBinding
		}{
			{
				name: "chart path versus binding key",
				left: [3]string{"u", "ref", "charts/a|b"}, right: [3]string{"u", "ref", "charts/a"},
				leftBindings: []NamespaceBinding{{Key: "c"}}, rightBindings: []NamespaceBinding{{Key: "b"}, {Key: "c"}},
			},
			{
				name: "subject versus catalog ref",
				left: [3]string{"u|v", "ref", "charts/a"}, right: [3]string{"u", "v|ref", "charts/a"},
			},
			{
				name: "catalog ref versus chart path",
				left: [3]string{"u", "ref|charts", "a"}, right: [3]string{"u", "ref", "charts|a"},
			},
			{
				name: "one binding versus two bindings",
				left: [3]string{"u", "ref", "charts/a"}, right: [3]string{"u", "ref", "charts/a"},
				leftBindings: []NamespaceBinding{{Key: "b|c"}}, rightBindings: []NamespaceBinding{{Key: "b"}, {Key: "c"}},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// Given distinct tuples that produce identical delimiter-joined bytes,
				// when deriving public release names, then their identities remain distinct.
				assert.NotEqual(t,
					HelmReleaseName(tc.left[0], tc.left[1], tc.left[2], tc.leftBindings),
					HelmReleaseName(tc.right[0], tc.right[1], tc.right[2], tc.rightBindings))
			})
		}
	})

	t.Run("different users get different names", func(t *testing.T) {
		bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
		name1 := HelmReleaseName("alice", "ref1", "setup/helm", bindings)
		name2 := HelmReleaseName("bob", "ref1", "setup/helm", bindings)
		assert.NotEqual(t, name1, name2)
	})

	t.Run("different selected chart paths get different names", func(t *testing.T) {
		bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
		assert.NotEqual(t,
			HelmReleaseName("user1", "ref1", "shared/chart-a", bindings),
			HelmReleaseName("user1", "ref1", "shared/chart-b", bindings))
	})

	t.Run("different catalog refs get different names", func(t *testing.T) {
		bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
		name1 := HelmReleaseName("user1", "ref-repo-a", "setup/helm", bindings)
		name2 := HelmReleaseName("user1", "ref-repo-b", "setup/helm", bindings)
		assert.NotEqual(t, name1, name2)
	})
}
