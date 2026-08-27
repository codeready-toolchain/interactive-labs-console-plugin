// Package catalog reads the admin-managed lab catalog from a ConfigMap.
package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
)

// Entry is a single catalog lab entry derived from the ConfigMap.
type Entry struct {
	CatalogRef         string
	DisplayName        string
	Description        string
	Repository         string
	Branch             string
	LabPath            string
	AttributeOverrides map[string]string
}

type rawEntry struct {
	DisplayName        string            `yaml:"displayName"`
	Description        string            `yaml:"description"`
	Repository         string            `yaml:"repository"`
	Branch             string            `yaml:"branch"`
	LabPath            string            `yaml:"labPath"`
	AttributeOverrides map[string]string `yaml:"attributeOverrides"`
}

// DeriveRef computes an opaque URL-safe catalogRef from stable identity fields.
// The ref is the SHA-256 hex digest of canonical JSON over repository, branch, labPath,
// and attributeOverrides. Display fields (displayName, description) and request-runtime
// attributes are excluded. nil and empty attributeOverrides produce the same ref.
func DeriveRef(repository, branch, labPath string, overrides map[string]string) string {
	if overrides == nil {
		overrides = map[string]string{}
	}
	type canonical struct {
		AttributeOverrides map[string]string `json:"attributeOverrides"`
		Branch             string            `json:"branch"`
		LabPath            string            `json:"labPath"`
		Repository         string            `json:"repository"`
	}
	b, _ := json.Marshal(canonical{
		AttributeOverrides: overrides,
		Branch:             branch,
		LabPath:            labPath,
		Repository:         repository,
	})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// Read parses the interactive-labs-catalog ConfigMap into typed entries.
// The ConfigMap must contain a "catalog.yaml" key whose value is a top-level
// YAML sequence. Sequence order is preserved in the returned slice.
// Returns a configuration_error if the key is missing, the YAML is invalid,
// any required field is absent, or derived catalogRefs are duplicated.
func Read(cm *corev1.ConfigMap) ([]Entry, error) {
	raw, ok := cm.Data["catalog.yaml"]
	if !ok {
		return nil, fmt.Errorf("configuration_error: ConfigMap missing required key \"catalog.yaml\"")
	}

	var items []rawEntry
	if err := yaml.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("configuration_error: catalog.yaml is not valid YAML: %w", err)
	}
	if items == nil && strings.TrimSpace(raw) != "" {
		return nil, fmt.Errorf("configuration_error: catalog.yaml must be a YAML sequence")
	}

	seen := make(map[string]bool, len(items))
	entries := make([]Entry, 0, len(items))
	for i, item := range items {
		label := fmt.Sprintf("entry[%d]", i)
		if item.DisplayName == "" {
			return nil, fmt.Errorf("configuration_error: %s missing required field displayName", label)
		}
		if item.Repository == "" {
			return nil, fmt.Errorf("configuration_error: %s missing required field repository", label)
		}
		if item.Branch == "" {
			return nil, fmt.Errorf("configuration_error: %s missing required field branch", label)
		}
		if item.LabPath == "" {
			return nil, fmt.Errorf("configuration_error: %s missing required field labPath", label)
		}
		ref := DeriveRef(item.Repository, item.Branch, item.LabPath, item.AttributeOverrides)
		if seen[ref] {
			return nil, fmt.Errorf("configuration_error: duplicate catalog entry ref (same repository, branch, labPath, and attributeOverrides)")
		}
		seen[ref] = true
		entries = append(entries, Entry{
			CatalogRef:         ref,
			DisplayName:        item.DisplayName,
			Description:        item.Description,
			Repository:         item.Repository,
			Branch:             item.Branch,
			LabPath:            item.LabPath,
			AttributeOverrides: item.AttributeOverrides,
		})
	}
	return entries, nil
}
