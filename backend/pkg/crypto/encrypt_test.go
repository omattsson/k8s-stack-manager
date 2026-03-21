package crypto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{name: "normal text", plaintext: []byte("hello world, this is sensitive data")},
		{name: "empty plaintext", plaintext: []byte{}},
		{name: "binary data", plaintext: []byte{0x00, 0x01, 0xFF, 0xFE}},
		{name: "large payload", plaintext: make([]byte, 10000)},
	}

	key := DeriveKey("test-passphrase")

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encrypted, err := Encrypt(tt.plaintext, key)
			require.NoError(t, err)
			assert.NotEqual(t, tt.plaintext, encrypted)

			decrypted, err := Decrypt(encrypted, key)
			require.NoError(t, err)
			if len(tt.plaintext) == 0 {
				assert.Empty(t, decrypted)
			} else {
				assert.Equal(t, tt.plaintext, decrypted)
			}
		})
	}
}

func TestEncrypt_InvalidKeyLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  []byte
	}{
		{name: "empty key", key: []byte{}},
		{name: "16-byte key", key: make([]byte, 16)},
		{name: "31-byte key", key: make([]byte, 31)},
		{name: "33-byte key", key: make([]byte, 33)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Encrypt([]byte("data"), tt.key)
			assert.EqualError(t, err, "encryption key must be exactly 32 bytes")
		})
	}
}

func TestDecrypt_InvalidKeyLength(t *testing.T) {
	t.Parallel()

	_, err := Decrypt([]byte("some-ciphertext-data-here-1234567890"), make([]byte, 16))
	assert.EqualError(t, err, "encryption key must be exactly 32 bytes")
}

func TestDecrypt_WrongKey(t *testing.T) {
	t.Parallel()

	key1 := DeriveKey("passphrase-one")
	key2 := DeriveKey("passphrase-two")

	encrypted, err := Encrypt([]byte("secret"), key1)
	require.NoError(t, err)

	_, err = Decrypt(encrypted, key2)
	assert.Error(t, err)
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	t.Parallel()

	key := DeriveKey("test-key")
	encrypted, err := Encrypt([]byte("secret data"), key)
	require.NoError(t, err)

	// Tamper with the last byte of ciphertext
	encrypted[len(encrypted)-1] ^= 0xFF

	_, err = Decrypt(encrypted, key)
	assert.Error(t, err)
}

func TestDecrypt_CiphertextTooShort(t *testing.T) {
	t.Parallel()

	key := DeriveKey("test-key")
	_, err := Decrypt([]byte("short"), key)
	assert.EqualError(t, err, "ciphertext too short")
}

func TestDeriveKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		passphrase string
	}{
		{name: "normal passphrase", passphrase: "my-secret-key"},
		{name: "empty passphrase", passphrase: ""},
		{name: "long passphrase", passphrase: "this-is-a-very-long-passphrase-that-exceeds-32-bytes-easily"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := DeriveKey(tt.passphrase)
			assert.Len(t, key, 32)
		})
	}

	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()
		key1 := DeriveKey("same-passphrase")
		key2 := DeriveKey("same-passphrase")
		assert.Equal(t, key1, key2)
	})

	t.Run("different passphrases produce different keys", func(t *testing.T) {
		t.Parallel()
		key1 := DeriveKey("passphrase-a")
		key2 := DeriveKey("passphrase-b")
		assert.NotEqual(t, key1, key2)
	})
}
