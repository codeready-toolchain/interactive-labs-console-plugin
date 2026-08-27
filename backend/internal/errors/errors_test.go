package errors_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
)

func TestNewError_JSONShape(t *testing.T) {
	e := errors.New(errors.TypeLabNotFound, "lab x is missing", nil)

	b, err := json.Marshal(e)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.Equal(t, "v1alpha1", got["apiVersion"])
	assert.Equal(t, "Error", got["kind"])
	assert.Equal(t, "lab_not_found", got["type"])
	assert.Equal(t, "lab x is missing", got["message"])
}

func TestNewError_WithDetail(t *testing.T) {
	detail := map[string]string{"key": "value"}
	e := errors.New(errors.TypeConfigurationError, "bad config", detail)

	b, _ := json.Marshal(e)
	var got map[string]any
	json.Unmarshal(b, &got) //nolint:errcheck

	assert.NotNil(t, got["detail"])
}

func TestError_NeverContainsBearerToken(t *testing.T) {
	e := errors.New(errors.TypeUnauthorized, "token rejected", nil)
	b, _ := json.Marshal(e)
	s := string(b)
	assert.NotContains(t, s, "Bearer")
	assert.NotContains(t, s, "eyJ")
}
