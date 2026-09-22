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
		name1 := HelmReleaseName("user1", "abc123", bindings)
		name2 := HelmReleaseName("user1", "abc123", bindings)
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
		assert.Equal(t, HelmReleaseName("u", "ref1", asc), HelmReleaseName("u", "ref1", desc))
	})

	t.Run("different users get different names", func(t *testing.T) {
		bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
		name1 := HelmReleaseName("alice", "ref1", bindings)
		name2 := HelmReleaseName("bob", "ref1", bindings)
		assert.NotEqual(t, name1, name2)
	})

	t.Run("different catalog refs get different names", func(t *testing.T) {
		bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
		name1 := HelmReleaseName("user1", "ref-repo-a", bindings)
		name2 := HelmReleaseName("user1", "ref-repo-b", bindings)
		assert.NotEqual(t, name1, name2)
	})
}
