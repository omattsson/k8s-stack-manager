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

	// hash should be an Argon2id-encoded string (starts with $argon2id$)
	assert.Contains(t, hash, "$argon2id$")

	// VerifyAPIKeyHash must confirm the raw key matches the generated hash
	assert.True(t, VerifyAPIKeyHash(rawKey, hash), "VerifyAPIKeyHash should confirm the raw key")
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

func TestHashAPIKeyWithSalt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "normal key", input: "abc123def456"},
		{name: "empty string", input: ""},
		{name: "long key", input: "a]b!c@d#e$f%g^h&i*j(k)l_m+n=o{p}q[r"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hash, err := HashAPIKeyWithSalt(tt.input)
			assert.NoError(t, err)
			assert.Contains(t, hash, "$argon2id$", "hash should be Argon2id encoded")
			assert.True(t, VerifyAPIKeyHash(tt.input, hash), "VerifyAPIKeyHash should confirm the hash")
		})
	}
}

func TestHashAPIKeyWithSalt_Uniqueness(t *testing.T) {
	t.Parallel()

	// Two hashes of the same input must differ (different salts) but both verify.
	input := "test-key-12345"
	hash1, err1 := HashAPIKeyWithSalt(input)
	assert.NoError(t, err1)
	hash2, err2 := HashAPIKeyWithSalt(input)
	assert.NoError(t, err2)

	assert.NotEqual(t, hash1, hash2, "salted hashes of the same key should differ")
	assert.True(t, VerifyAPIKeyHash(input, hash1))
	assert.True(t, VerifyAPIKeyHash(input, hash2))
}

func TestVerifyAPIKeyHash_DifferentInputs(t *testing.T) {
	t.Parallel()

	hash, err := HashAPIKeyWithSalt("key-one")
	assert.NoError(t, err)
	assert.False(t, VerifyAPIKeyHash("key-two", hash), "different key must not verify")
}

func TestVerifyAPIKeyHash_InvalidEncoding(t *testing.T) {
	t.Parallel()

	assert.False(t, VerifyAPIKeyHash("any-key", "not-a-valid-hash"))
	assert.False(t, VerifyAPIKeyHash("any-key", ""))
}
