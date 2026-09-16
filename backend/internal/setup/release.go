package setup

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// HelmReleaseName derives a stable, deterministic Helm release name from the
// authenticated user subject, lab path, branch, and namespace bindings.
// The result is "il-" followed by a truncated hex SHA-256 digest.
func HelmReleaseName(subject, labPath, branch string, bindings []NamespaceBinding) string {
	keys := make([]string, len(bindings))
	for i, b := range bindings {
		keys[i] = b.Key
	}
	sort.Strings(keys)

	h := sha256.New()
	h.Write([]byte(subject))
	h.Write([]byte("|"))
	h.Write([]byte(labPath))
	h.Write([]byte("|"))
	h.Write([]byte(branch))
	for _, k := range keys {
		h.Write([]byte("|"))
		h.Write([]byte(k))
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return fmt.Sprintf("il-%x", sum[:6])
}
