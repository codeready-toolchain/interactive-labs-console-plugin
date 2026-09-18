package setup

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNamespaceBindings_RealWorldSchema(t *testing.T) {
	schema := []byte(`{
		"properties": {
			"namespaces": {
				"type": "object",
				"properties": {
					"gitea": {
						"type": "string",
						"x-interactive-labs": { "kind": "namespace-name" }
					},
					"datasphere": {
						"type": "string",
						"x-interactive-labs": { "kind": "namespace-name" }
					},
					"lab": {
						"type": "string",
						"x-interactive-labs": { "kind": "namespace-name" }
					}
				}
			}
		}
	}`)

	bindings, err := ParseNamespaceBindings(schema)
	require.NoError(t, err)
	require.Len(t, bindings, 3)

	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Key < bindings[j].Key })
	assert.Equal(t, NamespaceBinding{Key: "datasphere", Path: "namespaces.datasphere"}, bindings[0])
	assert.Equal(t, NamespaceBinding{Key: "gitea", Path: "namespaces.gitea"}, bindings[1])
	assert.Equal(t, NamespaceBinding{Key: "lab", Path: "namespaces.lab"}, bindings[2])
}

func TestParseNamespaceBindings_NoMatchingProperties(t *testing.T) {
	schema := []byte(`{
		"properties": {
			"replicas": { "type": "integer" }
		}
	}`)

	bindings, err := ParseNamespaceBindings(schema)
	require.NoError(t, err)
	assert.Empty(t, bindings)
}

func TestParseNamespaceBindings_TopLevelProperty(t *testing.T) {
	schema := []byte(`{
		"properties": {
			"myNamespace": {
				"type": "string",
				"x-interactive-labs": { "kind": "namespace-name" }
			}
		}
	}`)

	bindings, err := ParseNamespaceBindings(schema)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, NamespaceBinding{Key: "myNamespace", Path: "myNamespace"}, bindings[0])
}

func TestParseNamespaceBindings_IgnoresOtherKinds(t *testing.T) {
	schema := []byte(`{
		"properties": {
			"ns": {
				"type": "string",
				"x-interactive-labs": { "kind": "namespace-name" }
			},
			"url": {
				"type": "string",
				"x-interactive-labs": { "kind": "cluster-url" }
			}
		}
	}`)

	bindings, err := ParseNamespaceBindings(schema)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, "ns", bindings[0].Key)
}

func TestParseNamespaceBindings_InvalidJSON(t *testing.T) {
	_, err := ParseNamespaceBindings([]byte(`{invalid`))
	require.Error(t, err)
}

func TestParseNamespaceBindings_EmptySchema(t *testing.T) {
	bindings, err := ParseNamespaceBindings([]byte(`{}`))
	require.NoError(t, err)
	assert.Empty(t, bindings)
}
