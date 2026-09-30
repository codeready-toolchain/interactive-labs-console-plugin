package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogIdentity(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	t.Run("full digest fits a Kubernetes label without truncation", func(t *testing.T) {
		label, err := CatalogRefLabel(ref)
		require.NoError(t, err)
		assert.Len(t, label, 52)
		assert.NotEqual(t, label, strings.Repeat("ab", 31)+"cd")
		decoded, err := CatalogRefFromLabel(label)
		require.NoError(t, err)
		assert.Equal(t, ref, decoded)
	})
	t.Run("rejects invalid identity", func(t *testing.T) {
		_, err := CatalogRefLabel("not-a-digest")
		require.Error(t, err)
		_, err = CatalogRefFromLabel("abc")
		require.Error(t, err)
	})
}
