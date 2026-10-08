package setup

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSchemaBindings(t *testing.T) {
	t.Run("mixed kinds", func(t *testing.T) {
		schema := []byte(`{
			"properties": {
				"namespaces": {
					"type": "object",
					"properties": {
						"gitea": {
							"type": "string",
							"x-interactive-labs": { "kind": "namespace-name" }
						}
					}
				},
				"passwords": {
					"type": "object",
					"properties": {
						"postgres": {
							"type": "string",
							"x-interactive-labs": { "kind": "generated-password" }
						},
						"giteaAdmin": {
							"type": "string",
							"x-interactive-labs": { "kind": "generated-password" }
						}
					}
				}
			}
		}`)

		bindings, err := ParseSchemaBindings(schema)
		require.NoError(t, err)
		nsBindings := BindingsByKind(bindings, KindNamespaceName)
		require.Len(t, nsBindings, 1)
		assert.Equal(t, "gitea", nsBindings[0].Key)
		assert.Equal(t, "namespaces.gitea", nsBindings[0].Path)

		pwBindings := BindingsByKind(bindings, KindGeneratedPassword)
		require.Len(t, pwBindings, 2)
		sort.Slice(pwBindings, func(i, j int) bool { return pwBindings[i].Key < pwBindings[j].Key })
		assert.Equal(t, "giteaAdmin", pwBindings[0].Key)
		assert.Equal(t, "passwords.giteaAdmin", pwBindings[0].Path)
		assert.Equal(t, KindGeneratedPassword, pwBindings[0].Kind)
		assert.Equal(t, "postgres", pwBindings[1].Key)
		assert.Equal(t, "passwords.postgres", pwBindings[1].Path)
	})

	t.Run("with root URL", func(t *testing.T) {
		schema := []byte(`{
			"properties": {
				"namespaces": {
					"type": "object",
					"properties": {
						"gitea": {
							"type": "string",
							"x-interactive-labs": { "kind": "namespace-name" }
						}
					}
				},
				"gitea": {
					"type": "object",
					"properties": {
						"rootUrl": {
							"type": "string",
							"x-interactive-labs": { "kind": "root-url", "routeName": "gitea", "namespaceRef": "gitea" }
						}
					}
				}
			}
		}`)

		bindings, err := ParseSchemaBindings(schema)
		require.NoError(t, err)
		nsBindings := BindingsByKind(bindings, KindNamespaceName)
		require.Len(t, nsBindings, 1)
		urlBindings := BindingsByKind(bindings, KindRootURL)
		require.Len(t, urlBindings, 1)
		assert.Equal(t, "rootUrl", urlBindings[0].Key)
		assert.Equal(t, "gitea.rootUrl", urlBindings[0].Path)
		assert.Equal(t, "gitea", urlBindings[0].RouteName)
		assert.Equal(t, "gitea", urlBindings[0].NamespaceRef)
	})

	t.Run("strips .namespace suffix from namespaceRef", func(t *testing.T) {
		schema := []byte(`{
			"properties": {
				"datasphere": {
					"type": "object",
					"properties": {
						"namespace": {
							"type": "string",
							"x-interactive-labs": { "kind": "namespace-name" }
						},
						"rootUrl": {
							"type": "string",
							"x-interactive-labs": { "kind": "root-url", "routeName": "datasphere", "namespaceRef": "datasphere.namespace" }
						}
					}
				}
			}
		}`)

		bindings, err := ParseSchemaBindings(schema)
		require.NoError(t, err)
		nsBindings := BindingsByKind(bindings, KindNamespaceName)
		require.Len(t, nsBindings, 1)
		assert.Equal(t, "datasphere", nsBindings[0].Key)
		assert.Equal(t, "datasphere.namespace", nsBindings[0].Path)
		urlBindings := BindingsByKind(bindings, KindRootURL)
		require.Len(t, urlBindings, 1)
		assert.Equal(t, "datasphere.rootUrl", urlBindings[0].Path)
		assert.Equal(t, "datasphere", urlBindings[0].NamespaceRef)
	})

	t.Run("strips .namespace suffix from key", func(t *testing.T) {
		schema := []byte(`{
			"properties": {
				"gitea": {
					"type": "object",
					"properties": {
						"namespace": {
							"type": "string",
							"x-interactive-labs": { "kind": "namespace-name" }
						}
					}
				},
				"datasphere": {
					"type": "object",
					"properties": {
						"namespace": {
							"type": "string",
							"x-interactive-labs": { "kind": "namespace-name" }
						}
					}
				}
			}
		}`)

		bindings, err := ParseSchemaBindings(schema)
		require.NoError(t, err)
		nsBindings := BindingsByKind(bindings, KindNamespaceName)
		require.Len(t, nsBindings, 2)
		sort.Slice(nsBindings, func(i, j int) bool { return nsBindings[i].Key < nsBindings[j].Key })
		assert.Equal(t, "datasphere", nsBindings[0].Key)
		assert.Equal(t, "datasphere.namespace", nsBindings[0].Path)
		assert.Equal(t, "gitea", nsBindings[1].Key)
		assert.Equal(t, "gitea.namespace", nsBindings[1].Path)
	})

	t.Run("no passwords", func(t *testing.T) {
		schema := []byte(`{
			"properties": {
				"ns": {
					"type": "string",
					"x-interactive-labs": { "kind": "namespace-name" }
				}
			}
		}`)

		bindings, err := ParseSchemaBindings(schema)
		require.NoError(t, err)
		nsBindings := BindingsByKind(bindings, KindNamespaceName)
		require.Len(t, nsBindings, 1)
		assert.Empty(t, BindingsByKind(bindings, KindGeneratedPassword))
	})
}

func TestBindingPath(t *testing.T) {
	bindings := []SchemaBinding{
		{Key: "gitea", Path: "namespaces.gitea", Kind: KindNamespaceName},
		{Key: "lab", Path: "namespaces.lab", Kind: KindNamespaceName},
	}
	assert.Equal(t, "namespaces.gitea", BindingPath(bindings, "gitea"))
	assert.Equal(t, "namespaces.lab", BindingPath(bindings, "lab"))
	assert.Empty(t, BindingPath(bindings, "missing"))
}
