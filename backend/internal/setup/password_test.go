package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePassword_Length(t *testing.T) {
	pw, err := GeneratePassword()
	require.NoError(t, err)
	assert.Len(t, pw, passwordLength)
}

func TestGeneratePassword_Uniqueness(t *testing.T) {
	pw1, err := GeneratePassword()
	require.NoError(t, err)
	pw2, err := GeneratePassword()
	require.NoError(t, err)
	assert.NotEqual(t, pw1, pw2)
}

func TestGeneratePassword_CharsetOnly(t *testing.T) {
	pw, err := GeneratePassword()
	require.NoError(t, err)
	for _, c := range pw {
		assert.Contains(t, passwordCharset, string(c))
	}
}

func TestGeneratePasswordValues_ReturnsCorrectPaths(t *testing.T) {
	bindings := []ValueBinding{
		{Key: "postgres", Path: "passwords.postgres", Kind: KindGeneratedPassword},
		{Key: "giteaAdmin", Path: "passwords.giteaAdmin", Kind: KindGeneratedPassword},
	}

	values, err := GeneratePasswordValues(bindings)
	require.NoError(t, err)
	require.Len(t, values, 2)
	assert.Contains(t, values, "passwords.postgres")
	assert.Contains(t, values, "passwords.giteaAdmin")
	assert.Len(t, values["passwords.postgres"], passwordLength)
	assert.Len(t, values["passwords.giteaAdmin"], passwordLength)
	assert.NotEqual(t, values["passwords.postgres"], values["passwords.giteaAdmin"])
}

func TestGeneratePasswordValues_EmptyBindings(t *testing.T) {
	values, err := GeneratePasswordValues(nil)
	require.NoError(t, err)
	assert.Empty(t, values)
}
