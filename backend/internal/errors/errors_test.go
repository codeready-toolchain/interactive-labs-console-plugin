package errors_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
)

func TestError(t *testing.T) {
	t.Run("JSON shape", func(t *testing.T) {
		e := errors.New(errors.TypeLabNotFound, "lab x is missing", nil)

		b, err := json.Marshal(e)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(b, &got))

		assert.Equal(t, "v1alpha1", got["apiVersion"])
		assert.Equal(t, "Error", got["kind"])
		assert.Equal(t, "lab_not_found", got["type"])
		assert.Equal(t, "lab x is missing", got["message"])
	})

	t.Run("with detail", func(t *testing.T) {
		detail := map[string]string{"key": "value"}
		e := errors.New(errors.TypeConfigurationError, "bad config", detail)

		b, err := json.Marshal(e)
		require.NoError(t, err)
		var got map[string]any
		json.Unmarshal(b, &got) //nolint:errcheck

		assert.NotNil(t, got["detail"])
	})

	t.Run("never contains bearer token", func(t *testing.T) {
		e := errors.New(errors.TypeUnauthorized, "token rejected", nil)
		b, err := json.Marshal(e)
		require.NoError(t, err)
		s := string(b)
		assert.NotContains(t, s, "Bearer")
		assert.NotContains(t, s, "eyJ")
	})
}
