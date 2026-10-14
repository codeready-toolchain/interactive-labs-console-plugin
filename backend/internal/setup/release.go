package setup

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// HelmReleaseName derives a stable, deterministic Helm release name from the
// authenticated user subject, catalog ref, normalized repository-relative chart
// directory, and namespace bindings, encoded as byte-length-prefixed fields.
// catalogRef already encodes repository, branch, lab path, and attribute
// overrides, so including it prevents collisions across distinct repositories.
// The result is "il-" followed by a truncated hex SHA-256 digest.
func HelmReleaseName(subject, catalogRef, chartPath string, bindings []SchemaBinding) string {
	var keys []string
	for _, b := range bindings {
		if b.Kind == KindNamespaceName {
			keys = append(keys, b.Key)
		}
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, field := range append([]string{subject, catalogRef, chartPath}, keys...) {
		fmt.Fprintf(h, "%d:%s", len(field), field)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return fmt.Sprintf("il-%x", sum[:6])
}
