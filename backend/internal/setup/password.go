package setup

import (
	"crypto/rand"
	"math/big"
)

const (
	passwordLength  = 24
	passwordCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// GeneratePassword returns a cryptographically secure random password.
func GeneratePassword() (string, error) {
	b := make([]byte, passwordLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordCharset))))
		if err != nil {
			return "", err
		}
		b[i] = passwordCharset[n.Int64()]
	}
	return string(b), nil
}

// GeneratePasswordValues creates a random password for each generated-password
// binding and returns them as a Helm values map.
func GeneratePasswordValues(bindings []ValueBinding) (map[string]string, error) {
	values := make(map[string]string, len(bindings))
	for _, b := range bindings {
		pw, err := GeneratePassword()
		if err != nil {
			return nil, err
		}
		values[b.Path] = pw
	}
	return values, nil
}
