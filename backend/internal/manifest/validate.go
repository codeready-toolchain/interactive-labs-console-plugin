// Package manifest validates LabManifest v1alpha1 documents and bundle path safety.
package manifest

import (
	"path"
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
	Name        string `yaml:"name"          json:"name"`
	DisplayName string `yaml:"displayName" json:"displayName"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type Spec struct {
	Attributes  map[string]string `yaml:"attributes,omitempty"  json:"attributes,omitempty"`
	Checkpoints []Checkpoint      `yaml:"checkpoints,omitempty" json:"checkpoints,omitempty"`
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
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, laberr.New(laberr.TypeContentError, "manifest is not valid YAML", nil)
	}
	if m.APIVersion != "v1alpha1" {
		return nil, laberr.New(laberr.TypeManifestVersionUnsupported,
			"manifest apiVersion must be v1alpha1", map[string]string{"got": m.APIVersion})
	}
	if m.Kind != "Lab" {
		return nil, laberr.New(laberr.TypeContentError, "manifest kind must be Lab", nil)
	}
	if m.Metadata.Name == "" {
		return nil, laberr.New(laberr.TypeContentError, "manifest metadata.name is required", nil)
	}
	if m.Metadata.DisplayName == "" {
		return nil, laberr.New(laberr.TypeContentError, "manifest metadata.displayName is required", nil)
	}
	return &m, nil
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
