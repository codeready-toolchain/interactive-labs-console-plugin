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
		// Given namespace and password annotations in a decoded chart schema.
		bindings := decodeBindings(t, []byte(`{"properties":{"namespaces":{"properties":{"gitea":{"x-interactive-labs":{"kind":"namespace-name"}}}},"passwords":{"properties":{"postgres":{"x-interactive-labs":{"kind":"generated-password"}},"giteaAdmin":{"x-interactive-labs":{"kind":"generated-password"}}}}}}`))
		// When selecting supported kinds, then paths and leaf keys are preserved.
		ns := BindingsByKind(bindings, KindNamespaceName)
		require.Len(t, ns, 1)
		assert.Equal(t, SchemaBinding{Key: "gitea", Path: "namespaces.gitea", Kind: KindNamespaceName}, ns[0])
		pw := BindingsByKind(bindings, KindGeneratedPassword)
		sort.Slice(pw, func(i, j int) bool { return pw[i].Key < pw[j].Key })
		assert.Equal(t, []SchemaBinding{{Key: "giteaAdmin", Path: "passwords.giteaAdmin", Kind: KindGeneratedPassword}, {Key: "postgres", Path: "passwords.postgres", Kind: KindGeneratedPassword}}, pw)
	})
	t.Run("with root URL", func(t *testing.T) {
		// Given a namespace and a route referring to its leaf key.
		bindings := decodeBindings(t, []byte(`{"properties":{"namespaces":{"properties":{"gitea":{"x-interactive-labs":{"kind":"namespace-name"}}}},"gitea":{"properties":{"rootUrl":{"x-interactive-labs":{"kind":"root-url","routeName":"gitea","namespaceRef":"gitea"}}}}}}`))
		// Then interpretation leaves the reference for provisioning to resolve.
		assert.Len(t, BindingsByKind(bindings, KindNamespaceName), 1)
		assert.Equal(t, []SchemaBinding{{Key: "rootUrl", Path: "gitea.rootUrl", Kind: KindRootURL, RouteName: "gitea", NamespaceRef: "gitea"}}, BindingsByKind(bindings, KindRootURL))
	})
	t.Run("normalizes namespace keys and references", func(t *testing.T) {
		// Given nested .namespace properties and a matching qualified reference.
		bindings := decodeBindings(t, []byte(`{"properties":{"gitea":{"properties":{"namespace":{"x-interactive-labs":{"kind":"namespace-name"}}}},"datasphere":{"properties":{"namespace":{"x-interactive-labs":{"kind":"namespace-name"}},"rootUrl":{"x-interactive-labs":{"kind":"root-url","namespaceRef":"datasphere.namespace"}}}}}}`))
		// Then namespace keys and route references use the same normalized name.
		ns := BindingsByKind(bindings, KindNamespaceName)
		sort.Slice(ns, func(i, j int) bool { return ns[i].Key < ns[j].Key })
		assert.Equal(t, []SchemaBinding{{Key: "datasphere", Path: "datasphere.namespace", Kind: KindNamespaceName}, {Key: "gitea", Path: "gitea.namespace", Kind: KindNamespaceName}}, ns)
		assert.Equal(t, []SchemaBinding{{Key: "rootUrl", Path: "datasphere.rootUrl", Kind: KindRootURL, NamespaceRef: "datasphere"}}, BindingsByKind(bindings, KindRootURL))
	})
	t.Run("no passwords", func(t *testing.T) {
		// Given only a namespace annotation, when interpreted, then no passwords exist.
		bindings := decodeBindings(t, []byte(`{"properties":{"ns":{"x-interactive-labs":{"kind":"namespace-name"}}}}`))
		assert.Len(t, BindingsByKind(bindings, KindNamespaceName), 1)
		assert.Empty(t, BindingsByKind(bindings, KindGeneratedPassword))
	})
	t.Run("retains unknown kinds and unresolved references but omits empty kinds", func(t *testing.T) {
		// Given correctly typed annotations without a namespace/reference policy.
		bindings := decodeBindings(t, []byte(`{"properties":{"future":{"x-interactive-labs":{"kind":"future-kind"}},"empty":{"x-interactive-labs":{"kind":""}},"url":{"x-interactive-labs":{"kind":"root-url","namespaceRef":"missing.namespace"}}}}`))
		// Then unknown nonempty kinds remain available for selected string attributes.
		assert.Len(t, bindings, 2)
		assert.Empty(t, BindingsByKind(bindings, KindNamespaceName))
		assert.Equal(t, []SchemaBinding{{Key: "future", Path: "future", Kind: "future-kind"}}, BindingsByKind(bindings, "future-kind"))
		assert.Equal(t, []SchemaBinding{{Key: "url", Path: "url", Kind: KindRootURL, NamespaceRef: "missing"}}, BindingsByKind(bindings, KindRootURL))
	})
}

func decodeBindings(t *testing.T, raw []byte) []SchemaBinding {
	t.Helper()
	schema, err := chart.DecodeSchema(raw)
	require.NoError(t, err)
	return ParseSchemaBindings(schema)
}

func TestBindingPath(t *testing.T) {
	// Given namespace bindings, when looking up keys, then only matching paths return.
	bindings := []SchemaBinding{{Key: "gitea", Path: "namespaces.gitea", Kind: KindNamespaceName}, {Key: "lab", Path: "namespaces.lab", Kind: KindNamespaceName}}
	assert.Equal(t, "namespaces.gitea", BindingPath(bindings, "gitea"))
	assert.Equal(t, "namespaces.lab", BindingPath(bindings, "lab"))
	assert.Empty(t, BindingPath(bindings, "missing"))
}
