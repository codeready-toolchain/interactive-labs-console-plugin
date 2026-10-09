// Package manifest validates LabManifest v1alpha1 documents and bundle path safety.
package manifest

import (
	"bytes"
	"errors"
	"io"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	laberr "github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
)

// LabManifest is the parsed v1alpha1 lab manifest.
type LabManifest struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind"       json:"kind"`
	Metadata   Metadata `yaml:"metadata"   json:"metadata"`
	Spec       Spec     `yaml:"spec"       json:"spec"`
}

type Metadata struct {
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type Spec struct {
	Attributes map[string]string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	SetupDir   string            `yaml:"setupDir,omitempty" json:"setupDir,omitempty"`
}

type ModuleManifest struct {
	Checkpoints []Checkpoint `yaml:"checkpoints" json:"checkpoints"`
}

type Checkpoint struct {
	ID       string `yaml:"id"       json:"id"`
	Type     string `yaml:"type"     json:"type"`
	PagePath string `yaml:"pagePath" json:"pagePath"`
	Label    string `yaml:"label"    json:"label"`
}

// ValidateManifest parses and validates a raw YAML lab.yaml.
// Returns (manifest, nil) on success or (nil, *LabError) on failure.
func ValidateManifest(raw []byte) (*LabManifest, *laberr.LabError) {
	var m LabManifest
	root, err := decodeStrict(raw, &m)
	if err != nil || root.Kind != yaml.MappingNode || !isMapping(field(root, "metadata")) ||
		(field(root, "spec") != nil && !isMapping(field(root, "spec"))) ||
		(field(field(root, "spec"), "attributes") != nil && !isMapping(field(field(root, "spec"), "attributes"))) ||
		!stringFields(field(root, "metadata"), "title", "description") ||
		!stringFields(field(root, "spec"), "setupDir") ||
		!stringValues(field(field(root, "spec"), "attributes")) {
		return nil, laberr.New(laberr.TypeContentError, "manifest is not valid YAML", nil)
	}
	if m.APIVersion != "v1alpha1" {
		return nil, laberr.New(laberr.TypeManifestVersionUnsupported,
			"manifest apiVersion must be v1alpha1", map[string]string{"got": m.APIVersion})
	}
	if m.Kind != "Lab" {
		return nil, laberr.New(laberr.TypeContentError, "manifest kind must be Lab", nil)
	}
	if strings.TrimSpace(m.Metadata.Title) == "" {
		return nil, laberr.New(laberr.TypeContentError, "manifest metadata.title is required", nil)
	}
	return &m, nil
}

// ValidateModule validates optional module.yaml against pages navigable in that module only.
func ValidateModule(raw []byte, navigable []string) (*ModuleManifest, *laberr.LabError) {
	var m ModuleManifest
	root, err := decodeStrict(raw, &m)
	checkpoints := field(root, "checkpoints")
	if err != nil || root.Kind != yaml.MappingNode || checkpoints == nil || checkpoints.Kind != yaml.SequenceNode {
		return nil, laberr.New(laberr.TypeContentError, "invalid module.yaml", nil)
	}
	seen := make(map[string]bool)
	for i, checkpoint := range m.Checkpoints {
		if !isMapping(checkpoints.Content[i]) || !stringFields(checkpoints.Content[i], "id", "type", "pagePath", "label") ||
			strings.TrimSpace(checkpoint.ID) == "" || checkpoint.Type != "manual" ||
			strings.TrimSpace(checkpoint.PagePath) == "" || strings.TrimSpace(checkpoint.Label) == "" ||
			path.IsAbs(checkpoint.PagePath) || strings.Contains(checkpoint.PagePath, "\\") ||
			slices.Contains(strings.Split(checkpoint.PagePath, "/"), "..") || seen[checkpoint.ID] {
			return nil, laberr.New(laberr.TypeContentError, "invalid module checkpoint", nil)
		}
		m.Checkpoints[i].PagePath = path.Clean(checkpoint.PagePath)
		if !slices.Contains(navigable, m.Checkpoints[i].PagePath) {
			return nil, laberr.New(laberr.TypeContentError, "checkpoint page is not navigable in module", nil)
		}
		seen[checkpoint.ID] = true
	}
	return &m, nil
}

func decodeStrict(raw []byte, dst any) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) != 1 {
		return nil, io.ErrUnexpectedEOF
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, io.ErrUnexpectedEOF
	}
	return doc.Content[0], nil
}

func field(mapping *yaml.Node, key string) *yaml.Node {
	if !isMapping(mapping) {
		return nil
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func isMapping(node *yaml.Node) bool { return node != nil && node.Kind == yaml.MappingNode }

func stringFields(mapping *yaml.Node, keys ...string) bool {
	for _, key := range keys {
		if value := field(mapping, key); value != nil && (value.Kind != yaml.ScalarNode || value.Tag != "!!str") {
			return false
		}
	}
	return true
}

func stringValues(mapping *yaml.Node) bool {
	if !isMapping(mapping) {
		return true
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Tag != "!!str" || mapping.Content[i+1].Tag != "!!str" {
			return false
		}
	}
	return true
}

// ValidateBundlePaths checks that all bundle keys are safe (no path traversal)
// and that at least one navigable .adoc path exists.
// A navigable path is one where no segment starts with _.
func ValidateBundlePaths(keys []string) *laberr.LabError {
	navigable := 0
	for _, k := range keys {
		clean := path.Clean(k)
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
			return laberr.New(laberr.TypePathTraversal,
				"bundle key escapes content root", map[string]string{"key": k})
		}
		if !isIncludeOnly(k) {
			navigable++
		}
	}
	if navigable == 0 {
		return laberr.New(laberr.TypeContentError, "no navigable .adoc pages in bundle", nil)
	}
	return nil
}

// isIncludeOnly returns true if any path segment starts with _.
func isIncludeOnly(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, "_") {
			return true
		}
	}
	return false
}
