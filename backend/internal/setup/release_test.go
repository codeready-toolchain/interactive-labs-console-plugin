package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHelmReleaseName(t *testing.T) {
	bindings := []SchemaBinding{{Key: "lab", Path: "namespaces.lab", Kind: KindNamespaceName}, {Key: "gitea", Path: "namespaces.gitea", Kind: KindNamespaceName}}
	t.Run("deterministic", func(t *testing.T) {
		// Given the same identity tuple, when deriving names, then names match and fit Helm.
		name := HelmReleaseName("user1", "abc123", "setup/helm", bindings)
		assert.Equal(t, name, HelmReleaseName("user1", "abc123", "setup/helm", bindings))
		assert.LessOrEqual(t, len(name), 53)
		assert.Regexp(t, `^il-[0-9a-f]{12}$`, name)
	})
	t.Run("binding order irrelevant", func(t *testing.T) {
		// Given identical namespace bindings in reverse order, then identity is stable.
		reversed := []SchemaBinding{bindings[1], bindings[0]}
		assert.Equal(t, HelmReleaseName("u", "ref", "setup/helm", bindings), HelmReleaseName("u", "ref", "setup/helm", reversed))
	})
	t.Run("only namespace bindings affect identity", func(t *testing.T) {
		// Given additional generated or future attributes, when naming, then namespaces alone identify the release.
		mixed := append(append([]SchemaBinding{}, bindings...), SchemaBinding{Key: "secret", Kind: KindGeneratedPassword}, SchemaBinding{Key: "url", Kind: KindRootURL}, SchemaBinding{Key: "future", Kind: "future-kind"})
		assert.Equal(t, HelmReleaseName("u", "ref", "setup/helm", bindings), HelmReleaseName("u", "ref", "setup/helm", mixed))
	})
	t.Run("identity field boundaries are unambiguous", func(t *testing.T) {
		for _, tc := range []struct {
			name                        string
			left, right                 [3]string
			leftBindings, rightBindings []SchemaBinding
		}{
			{name: "chart path versus binding key", left: [3]string{"u", "ref", "charts/a|b"}, right: [3]string{"u", "ref", "charts/a"}, leftBindings: []SchemaBinding{{Key: "c", Kind: KindNamespaceName}}, rightBindings: []SchemaBinding{{Key: "b", Kind: KindNamespaceName}, {Key: "c", Kind: KindNamespaceName}}},
			{name: "subject versus catalog ref", left: [3]string{"u|v", "ref", "charts/a"}, right: [3]string{"u", "v|ref", "charts/a"}},
			{name: "catalog ref versus chart path", left: [3]string{"u", "ref|charts", "a"}, right: [3]string{"u", "ref", "charts|a"}},
			{name: "one binding versus two bindings", left: [3]string{"u", "ref", "charts/a"}, right: [3]string{"u", "ref", "charts/a"}, leftBindings: []SchemaBinding{{Key: "b|c", Kind: KindNamespaceName}}, rightBindings: []SchemaBinding{{Key: "b", Kind: KindNamespaceName}, {Key: "c", Kind: KindNamespaceName}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// Given distinct tuples with identical delimiter-joined bytes, then identities differ.
				assert.NotEqual(t, HelmReleaseName(tc.left[0], tc.left[1], tc.left[2], tc.leftBindings), HelmReleaseName(tc.right[0], tc.right[1], tc.right[2], tc.rightBindings))
			})
		}
	})
	t.Run("users catalog refs and selected charts remain separate", func(t *testing.T) {
		// Given one changed identity field, when naming, then no release is shared.
		name := HelmReleaseName("alice", "ref", "shared/chart-a", bindings)
		assert.NotEqual(t, name, HelmReleaseName("bob", "ref", "shared/chart-a", bindings))
		assert.NotEqual(t, name, HelmReleaseName("alice", "other-ref", "shared/chart-a", bindings))
		assert.NotEqual(t, name, HelmReleaseName("alice", "ref", "shared/chart-b", bindings))
	})
}
