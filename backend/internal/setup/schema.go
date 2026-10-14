// Package setup handles lab environment provisioning: namespace creation via
// SpaceRequests and Helm chart deployment.
package setup

import (
	"strings"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/chart"
)

const (
	KindNamespaceName     = "namespace-name"
	KindGeneratedPassword = "generated-password"
	KindRootURL           = "root-url"
)

// SchemaBinding is a schema property annotated with x-interactive-labs.
type SchemaBinding struct {
	Key          string // leaf property key, normalized for namespace bindings
	Path         string // dot-separated Helm values path
	Kind         string // nonempty x-interactive-labs.kind, including unknown kinds
	RouteName    string // root-url Route resource name
	NamespaceRef string // root-url normalized namespace binding key
}

// BindingsByKind returns only the bindings matching the given kind.
func BindingsByKind(bindings []SchemaBinding, kind string) []SchemaBinding {
	var result []SchemaBinding
	for _, b := range bindings {
		if b.Kind == kind {
			result = append(result, b)
		}
	}
	return result
}

// ParseSchemaBindings interprets an already validated chart schema. References
// remain unresolved; unknown nonempty kinds are retained for selected attributes.
func ParseSchemaBindings(schema *chart.Schema) []SchemaBinding {
	var all []SchemaBinding
	walkAllProperties(schema, "", &all)
	for i := range all {
		b := &all[i]
		switch b.Kind {
		case KindNamespaceName:
			key := strings.TrimSuffix(b.Path, ".namespace")
			if key == b.Path {
				key = b.Key
			}
			b.Key = key
		case KindRootURL:
			b.NamespaceRef = strings.TrimSuffix(b.NamespaceRef, ".namespace")
		}
	}
	return all
}

// BindingPath returns the Helm values path for a matching key, or empty string.
func BindingPath(bindings []SchemaBinding, key string) string {
	for _, b := range bindings {
		if b.Key == key {
			return b.Path
		}
	}
	return ""
}

func walkAllProperties(node *chart.Schema, prefix string, out *[]SchemaBinding) {
	for key, child := range node.Properties {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if child.InteractiveLabs != nil && child.InteractiveLabs.Kind != "" {
			*out = append(*out, SchemaBinding{Key: key, Path: path, Kind: child.InteractiveLabs.Kind, RouteName: child.InteractiveLabs.RouteName, NamespaceRef: child.InteractiveLabs.NamespaceRef})
		}
		if len(child.Properties) > 0 {
			walkAllProperties(&child, path, out)
		}
	}
}
