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

// SchemaBinding is a schema property annotated with x-interactive-labs.
type SchemaBinding struct {
	Key          string // leaf property key (e.g. "gitea", "postgres")
	Path         string // dot-separated Helm values path (e.g. "namespaces.gitea")
	Kind         string // x-interactive-labs.kind value
	RouteName    string // (root-url only) Route resource name
	NamespaceRef string // (root-url only) key of the namespace binding to resolve
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

// ParseSchemaBindings reads a JSON Schema and returns all x-interactive-labs bindings.
func ParseSchemaBindings(raw []byte) ([]SchemaBinding, error) {
	var schema jsonSchemaNode
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("parse values schema: %w", err)
	}
	var all []SchemaBinding
	walkAllProperties(&schema, "", &all)

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
	return all, nil
}

// BindingPath returns the Helm values path for the binding with the given key,
// or empty string if not found.
func BindingPath(bindings []SchemaBinding, key string) string {
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

func walkAllProperties(node *jsonSchemaNode, prefix string, out *[]SchemaBinding) {
	for key, child := range node.Properties {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if child.InteractiveLabs != nil && child.InteractiveLabs.Kind != "" {
			*out = append(*out, SchemaBinding{
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
