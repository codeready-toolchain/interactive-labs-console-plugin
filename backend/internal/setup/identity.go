package setup

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	CatalogRefLabelKey   = "interactive-labs.openshift.io/catalog-ref"
	BindingKeyAnnotation = "interactive-labs.openshift.io/binding-key"
)

var catalogRefEncoding = base32.HexEncoding.WithPadding(base32.NoPadding)

// CatalogRefLabel encodes the complete SHA-256 catalog identity in a Kubernetes label.
func CatalogRefLabel(ref string) (string, error) {
	bytes, err := hex.DecodeString(ref)
	if err != nil || len(bytes) != 32 || hex.EncodeToString(bytes) != ref {
		return "", fmt.Errorf("invalid catalog identity")
	}
	return strings.ToLower(catalogRefEncoding.EncodeToString(bytes)), nil
}

func CatalogRefFromLabel(label string) (string, error) {
	if len(label) != 52 || label != strings.ToLower(label) {
		return "", fmt.Errorf("invalid catalog identity label")
	}
	bytes, err := catalogRefEncoding.DecodeString(strings.ToUpper(label))
	if err != nil || len(bytes) != 32 || strings.ToLower(catalogRefEncoding.EncodeToString(bytes)) != label {
		return "", fmt.Errorf("invalid catalog identity label")
	}
	return hex.EncodeToString(bytes), nil
}
