package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateAPIKey(t *testing.T) {
	t.Parallel()

	rawKey, prefix, hash, err := GenerateAPIKey()
	assert.NoError(t, err)
	assert.NotEmpty(t, rawKey)
	assert.NotEmpty(t, prefix)
	assert.NotEmpty(t, hash)

	// rawKey should be 64-char hex (32 bytes)
	assert.Len(t, rawKey, 64)

	// prefix should be first 16 chars of rawKey
	assert.Equal(t, rawKey[:16], prefix)
	assert.Len(t, prefix, 16)

	// hash should be 64-char hex (SHA-256)
	assert.Len(t, hash, 64)

	// hash should match HashAPIKey(rawKey)
	assert.Equal(t, HashAPIKey(rawKey), hash)
}

func TestGenerateAPIKey_Uniqueness(t *testing.T) {
	t.Parallel()

	rawKey1, _, hash1, err1 := GenerateAPIKey()
	assert.NoError(t, err1)

	rawKey2, _, hash2, err2 := GenerateAPIKey()
	assert.NoError(t, err2)

	assert.NotEqual(t, rawKey1, rawKey2, "two generated keys should be different")
	assert.NotEqual(t, hash1, hash2, "hashes of different keys should be different")
}

func TestHashAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expectLen int
	}{
		{
			name:      "hashes a normal key",
			input:     "abc123def456",
			expectLen: 64,
		},
		{
			name:      "hashes an empty string",
			input:     "",
			expectLen: 64,
		},
		{
			name:      "hashes a long key",
			input:     "a]b!c@d#e$f%g^h&i*j(k)l_m+n=o{p}q[r",
			expectLen: 64,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := HashAPIKey(tt.input)
			assert.NotEmpty(t, result)
			assert.Len(t, result, tt.expectLen)
		})
	}
}

func TestHashAPIKey_Consistency(t *testing.T) {
	t.Parallel()

	input := "test-key-12345"
	hash1 := HashAPIKey(input)
	hash2 := HashAPIKey(input)
	assert.Equal(t, hash1, hash2, "same input should produce same hash")
}

func TestHashAPIKey_DifferentInputs(t *testing.T) {
	t.Parallel()

	hash1 := HashAPIKey("key-one")
	hash2 := HashAPIKey("key-two")
	assert.NotEqual(t, hash1, hash2, "different inputs should produce different hashes")
}
