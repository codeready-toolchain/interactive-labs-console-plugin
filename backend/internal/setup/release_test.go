package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHelmReleaseName_Deterministic(t *testing.T) {
	bindings := []NamespaceBinding{
		{Key: "lab", Path: "namespaces.lab"},
		{Key: "gitea", Path: "namespaces.gitea"},
	}
	name1 := HelmReleaseName("user1", "labs/intro/", "main", bindings)
	name2 := HelmReleaseName("user1", "labs/intro/", "main", bindings)
	assert.Equal(t, name1, name2)
	assert.LessOrEqual(t, len(name1), 53, "must fit Helm 53-char limit")
	assert.Regexp(t, `^il-[0-9a-f]{12}$`, name1)
}

func TestHelmReleaseName_BindingOrderIrrelevant(t *testing.T) {
	asc := []NamespaceBinding{
		{Key: "a", Path: "namespaces.a"},
		{Key: "b", Path: "namespaces.b"},
	}
	desc := []NamespaceBinding{
		{Key: "b", Path: "namespaces.b"},
		{Key: "a", Path: "namespaces.a"},
	}
	assert.Equal(t, HelmReleaseName("u", "p", "br", asc), HelmReleaseName("u", "p", "br", desc))
}

func TestHelmReleaseName_DifferentUsersGetDifferentNames(t *testing.T) {
	bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
	name1 := HelmReleaseName("alice", "labs/intro/", "main", bindings)
	name2 := HelmReleaseName("bob", "labs/intro/", "main", bindings)
	assert.NotEqual(t, name1, name2)
}

func TestHelmReleaseName_DifferentBranchesGetDifferentNames(t *testing.T) {
	bindings := []NamespaceBinding{{Key: "lab", Path: "namespaces.lab"}}
	name1 := HelmReleaseName("user1", "labs/intro/", "main", bindings)
	name2 := HelmReleaseName("user1", "labs/intro/", "develop", bindings)
	assert.NotEqual(t, name1, name2)
}
