// Package setup handles lab environment provisioning: namespace creation via
// SpaceRequests and Helm chart deployment.
package setup

import (
	"encoding/json"
	"fmt"
)

// NamespaceBinding maps a Helm values path to a SpaceRequest-provisioned namespace.
type NamespaceBinding struct {
	Key  string // property key used as the SpaceRequest name (e.g. "gitea")
	Path string // dot-separated Helm values path (e.g. "namespaces.gitea")
}

// ParseNamespaceBindings reads a JSON Schema (values.schema.json) and returns
// one NamespaceBinding for every property whose x-interactive-labs.kind equals
// "namespace-name". It walks the property tree at any depth.
func ParseNamespaceBindings(raw []byte) ([]NamespaceBinding, error) {
	var schema jsonSchemaNode
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("parse values schema: %w", err)
	}
	var bindings []NamespaceBinding
	walkProperties(&schema, "", &bindings)
	return bindings, nil
}

type interactiveLabsExt struct {
	Kind string `json:"kind"`
}

type jsonSchemaNode struct {
	Type            string                    `json:"type"`
	Properties      map[string]jsonSchemaNode `json:"properties"`
	InteractiveLabs *interactiveLabsExt       `json:"x-interactive-labs"`
}

func walkProperties(node *jsonSchemaNode, prefix string, out *[]NamespaceBinding) {
	for key, child := range node.Properties {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if child.InteractiveLabs != nil && child.InteractiveLabs.Kind == "namespace-name" {
			*out = append(*out, NamespaceBinding{Key: key, Path: path})
		}
		if len(child.Properties) > 0 {
			walkProperties(&child, path, out)
		}
	}
}
