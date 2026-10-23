package database

import (
	"encoding/base64"
	"strings"

	"backend/pkg/crypto"
)

// encryptedPrefix marks a value that this version encrypted. An unprefixed
// value is legacy plaintext, or ciphertext of an older version.
const encryptedPrefix = "enc:v1:"

// minCiphertextLen is the AES-GCM nonce plus the tag: a shorter value is not ciphertext.
const minCiphertextLen = 12 + 16

// sealValue encrypts plain and adds the version prefix.
func sealValue(plain string, key []byte) (string, error) {
	encrypted, err := crypto.Encrypt([]byte(plain), key)
	if err != nil {
		return "", err
	}
	return encryptedPrefix + base64.StdEncoding.EncodeToString(encrypted), nil
}

// openValue reads a stored credential. It returns the plaintext and
// unreadable == "" when the value is plaintext or decrypts. Otherwise it
// returns an empty value and the reason. Unprefixed ciphertext of an older
// version decrypts only when legacyCiphertext is set; a shorter or non-base64
// unprefixed value is legacy plaintext.
func openValue(stored string, key []byte, legacyCiphertext bool) (plain, unreadable string) {
	if stored == "" {
		return "", ""
	}
	prefixed := strings.HasPrefix(stored, encryptedPrefix)
	if len(key) == 0 {
		if prefixed {
			return "", "no encryption key"
		}
		return stored, ""
	}
	if !prefixed && !legacyCiphertext {
		return stored, ""
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encryptedPrefix))
	if err != nil || (!prefixed && len(decoded) < minCiphertextLen) {
		if prefixed {
			return "", "invalid encoding"
		}
		return stored, ""
	}
	decrypted, err := crypto.Decrypt(decoded, key)
	if err != nil {
		return "", "decryption failed"
	}
	return string(decrypted), ""
}
