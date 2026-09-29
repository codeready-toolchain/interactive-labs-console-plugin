// Package setup handles lab environment provisioning: namespace creation via
// SpaceRequests and Helm chart deployment.
package setup

import (
	"encoding/json"
	"fmt"
	"strings"
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

// ParseSchemaBindings reads a JSON Schema and returns all x-interactive-labs
// bindings grouped by kind.
func ParseSchemaBindings(raw []byte) (*SchemaBindings, error) {
	var schema jsonSchemaNode
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("parse values schema: %w", err)
	}
	var all []ValueBinding
	walkAllProperties(&schema, "", &all)

	result := &SchemaBindings{}
	for _, b := range all {
		switch b.Kind {
		case KindNamespaceName:
			result.Namespaces = append(result.Namespaces, NamespaceBinding{Key: strings.TrimSuffix(b.Key, ".namespace"), Path: b.Path})
		case KindGeneratedPassword:
			result.Passwords = append(result.Passwords, b)
		case KindRootURL:
			result.RootURLs = append(result.RootURLs, RootURLBinding{
				Key:          b.Key,
				Path:         b.Path,
				RouteName:    b.RouteName,
				NamespaceRef: b.NamespaceRef,
			})
		}
	}
	return result, nil
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

type interactiveLabsExt struct {
	Kind         string `json:"kind"`
	RouteName    string `json:"routeName,omitempty"`
	NamespaceRef string `json:"namespaceRef,omitempty"`
}

type jsonSchemaNode struct {
	Type            string                    `json:"type"`
	Properties      map[string]jsonSchemaNode `json:"properties"`
	InteractiveLabs *interactiveLabsExt       `json:"x-interactive-labs"`
}

func walkAllProperties(node *jsonSchemaNode, prefix string, out *[]ValueBinding) {
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
