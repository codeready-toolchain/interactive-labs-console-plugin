package chart

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Annotation is the supported x-interactive-labs annotation structure.
// Interpretation of its values belongs to provisioning, not content validation.
type Annotation struct {
	Kind         string `json:"kind"`
	RouteName    string `json:"routeName,omitempty"`
	NamespaceRef string `json:"namespaceRef,omitempty"`
}

// Schema is the supported property tree of a chart's values.schema.json.
// Unknown fields and annotation kinds remain allowed; this is not full JSON Schema validation.
type Schema struct {
	Type            string            `json:"type"`
	Properties      map[string]Schema `json:"properties"`
	InteractiveLabs *Annotation       `json:"x-interactive-labs"`
}

// DecodeSchema requires a JSON object and correctly typed supported fields.
// It imposes no setup binding requirements or namespace/reference policy.
func DecodeSchema(raw []byte) (*Schema, error) {
	var schema *Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("parse values schema: %w", err)
	}
	if schema == nil {
		return nil, errors.New("values schema must be a JSON object")
	}
	return schema, nil
}
