package setup

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/chart"
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

		bindings := decodeBindings(t, schema)
		require.Len(t, bindings.Namespaces, 1)
		assert.Equal(t, "gitea", bindings.Namespaces[0].Key)
		assert.Equal(t, "namespaces.gitea", bindings.Namespaces[0].Path)

		require.Len(t, bindings.Passwords, 2)
		sort.Slice(bindings.Passwords, func(i, j int) bool { return bindings.Passwords[i].Key < bindings.Passwords[j].Key })
		assert.Equal(t, "giteaAdmin", bindings.Passwords[0].Key)
		assert.Equal(t, "passwords.giteaAdmin", bindings.Passwords[0].Path)
		assert.Equal(t, KindGeneratedPassword, bindings.Passwords[0].Kind)
		assert.Equal(t, "postgres", bindings.Passwords[1].Key)
		assert.Equal(t, "passwords.postgres", bindings.Passwords[1].Path)
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

		bindings := decodeBindings(t, schema)
		require.Len(t, bindings.Namespaces, 1)
		require.Len(t, bindings.RootURLs, 1)
		assert.Equal(t, "rootUrl", bindings.RootURLs[0].Key)
		assert.Equal(t, "gitea.rootUrl", bindings.RootURLs[0].Path)
		assert.Equal(t, "gitea", bindings.RootURLs[0].RouteName)
		assert.Equal(t, "gitea", bindings.RootURLs[0].NamespaceRef)
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

		bindings := decodeBindings(t, schema)
		require.Len(t, bindings.Namespaces, 1)
		assert.Equal(t, "datasphere", bindings.Namespaces[0].Key)
		assert.Equal(t, "datasphere.namespace", bindings.Namespaces[0].Path)
		require.Len(t, bindings.RootURLs, 1)
		assert.Equal(t, "datasphere.rootUrl", bindings.RootURLs[0].Path)
		assert.Equal(t, "datasphere", bindings.RootURLs[0].NamespaceRef)
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

		bindings := decodeBindings(t, schema)
		require.Len(t, bindings.Namespaces, 2)
		sort.Slice(bindings.Namespaces, func(i, j int) bool { return bindings.Namespaces[i].Key < bindings.Namespaces[j].Key })
		assert.Equal(t, "datasphere", bindings.Namespaces[0].Key)
		assert.Equal(t, "datasphere.namespace", bindings.Namespaces[0].Path)
		assert.Equal(t, "gitea", bindings.Namespaces[1].Key)
		assert.Equal(t, "gitea.namespace", bindings.Namespaces[1].Path)
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

		bindings := decodeBindings(t, schema)
		require.Len(t, bindings.Namespaces, 1)
		assert.Empty(t, bindings.Passwords)
	})
	t.Run("ignores unknown kinds and preserves unresolved references", func(t *testing.T) {
		// Given decoded, correctly typed annotations, not a namespace/reference policy.
		bindings := decodeBindings(t, []byte(`{"properties":{"future":{"x-interactive-labs":{"kind":"future-kind"}},"url":{"x-interactive-labs":{"kind":"root-url","namespaceRef":"missing.namespace"}}}}`))
		// Then unknown kinds are ignored and resolution remains provisioning's job.
		assert.Empty(t, bindings.Namespaces)
		assert.Empty(t, bindings.Passwords)
		assert.Equal(t, []RootURLBinding{{Key: "url", Path: "url", NamespaceRef: "missing"}}, bindings.RootURLs)
	})
}

func decodeBindings(t *testing.T, raw []byte) *SchemaBindings {
	t.Helper()
	schema, err := chart.DecodeSchema(raw)
	require.NoError(t, err)
	return ParseSchemaBindings(schema)
}

func TestNamespaceBindingPath(t *testing.T) {
	bindings := []NamespaceBinding{
		{Key: "gitea", Path: "namespaces.gitea"},
		{Key: "lab", Path: "namespaces.lab"},
	}
	assert.Equal(t, "namespaces.gitea", NamespaceBindingPath(bindings, "gitea"))
	assert.Equal(t, "namespaces.lab", NamespaceBindingPath(bindings, "lab"))
	assert.Empty(t, NamespaceBindingPath(bindings, "missing"))
}
