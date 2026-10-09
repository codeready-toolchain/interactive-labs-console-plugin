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

// NamespaceBinding maps a Helm values path to a SpaceRequest-provisioned namespace.
type NamespaceBinding struct {
	Key  string // property key used as the SpaceRequest name (e.g. "gitea")
	Path string // dot-separated Helm values path (e.g. "namespaces.gitea")
}

// ValueBinding is a schema property annotated with x-interactive-labs.
type ValueBinding struct {
	Key          string // leaf property key (e.g. "postgres")
	Path         string // dot-separated Helm values path (e.g. "passwords.postgres")
	Kind         string // x-interactive-labs.kind value
	RouteName    string // (root-url only) Route resource name
	NamespaceRef string // (root-url only) key of the NamespaceBinding to resolve
}

// RootURLBinding maps a Helm values path to a computed Route hostname.
// The hostname is assembled as <RouteName>-<namespace>.<ingressDomain>,
// where namespace comes from the resolved NamespaceBinding referenced by NamespaceRef.
type RootURLBinding struct {
	Key          string // leaf property key (e.g. "rootUrl")
	Path         string // dot-separated Helm values path (e.g. "gitea.rootUrl")
	RouteName    string // Route resource name (e.g. "gitea")
	NamespaceRef string // key of the NamespaceBinding to use (e.g. "gitea")
}

// SchemaBindings groups all x-interactive-labs bindings by kind.
type SchemaBindings struct {
	Namespaces []NamespaceBinding
	Passwords  []ValueBinding
	RootURLs   []RootURLBinding
}

// ParseSchemaBindings interprets a decoded chart schema and groups
// supported bindings by kind. Unknown kinds are ignored; references remain unresolved.
func ParseSchemaBindings(schema *chart.Schema) *SchemaBindings {
	var all []ValueBinding
	walkAllProperties(schema, "", &all)

	result := &SchemaBindings{}
	for _, b := range all {
		switch b.Kind {
		case KindNamespaceName:
			key := strings.TrimSuffix(b.Path, ".namespace")
			if key == b.Path {
				key = b.Key
			}
			result.Namespaces = append(result.Namespaces, NamespaceBinding{Key: key, Path: b.Path})
		case KindGeneratedPassword:
			result.Passwords = append(result.Passwords, b)
		case KindRootURL:
			result.RootURLs = append(result.RootURLs, RootURLBinding{
				Key:          b.Key,
				Path:         b.Path,
				RouteName:    b.RouteName,
				NamespaceRef: strings.TrimSuffix(b.NamespaceRef, ".namespace"),
			})
		}
	}
	return result
}

// NamespaceBindingPath returns the Helm values path for the namespace binding
// with the given key, or empty string if not found.
func NamespaceBindingPath(bindings []NamespaceBinding, key string) string {
	for _, b := range bindings {
		if b.Key == key {
			return b.Path
		}
	}
	return ""
}

func walkAllProperties(node *chart.Schema, prefix string, out *[]ValueBinding) {
	for key, child := range node.Properties {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if child.InteractiveLabs != nil && child.InteractiveLabs.Kind != "" {
			*out = append(*out, ValueBinding{
				Key:          key,
				Path:         path,
				Kind:         child.InteractiveLabs.Kind,
				RouteName:    child.InteractiveLabs.RouteName,
				NamespaceRef: child.InteractiveLabs.NamespaceRef,
			})
		}
		if len(child.Properties) > 0 {
			walkAllProperties(&child, path, out)
		}
	}
}
