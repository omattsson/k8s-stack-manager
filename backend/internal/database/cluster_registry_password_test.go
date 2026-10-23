package database

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"backend/internal/models"
	"backend/pkg/crypto"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testRegistryKey = "registry-test-key-0123456789abcdef"

func registryCluster(name, password string) *models.Cluster {
	return &models.Cluster{
		Name: name, APIServerURL: "https://k8s.example.com",
		RegistryURL: "registry.example.com", RegistryUsername: "u", RegistryPassword: password,
	}
}

func storedPassword(t *testing.T, db *gorm.DB, id string) string {
	t.Helper()
	var raw string
	require.NoError(t, db.Model(&models.Cluster{}).Where("id = ?", id).Select("registry_password").Scan(&raw).Error)
	return raw
}

func TestClusterRepository_RegistryPasswordWithoutKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("create is refused and stores nothing", func(t *testing.T) {
		t.Parallel()
		db := setupTestDBWithAllTables(t)
		repo := NewGORMClusterRepository(db, "")
		c := registryCluster("a", "s3cret-pass")
		err := repo.Create(ctx, c)
		require.Error(t, err)
		assert.True(t, errors.Is(err, dberrors.ErrValidation))
		assert.Contains(t, err.Error(), "KUBECONFIG_ENCRYPTION_KEY")
		assert.Equal(t, "s3cret-pass", c.RegistryPassword, "caller struct is not changed")
		n, cerr := repo.CountAll(ctx)
		require.NoError(t, cerr)
		assert.Zero(t, n)
	})

	t.Run("update to a new password is refused", func(t *testing.T) {
		t.Parallel()
		db := setupTestDBWithAllTables(t)
		repo := NewGORMClusterRepository(db, "")
		c := registryCluster("a", "")
		c.RegistryURL, c.RegistryUsername = "", ""
		require.NoError(t, repo.Create(ctx, c))
		c.RegistryURL, c.RegistryUsername, c.RegistryPassword = "registry.example.com", "u", "new-pass"
		err := repo.Update(ctx, c)
		require.Error(t, err)
		assert.True(t, errors.Is(err, dberrors.ErrValidation))
		assert.Empty(t, storedPassword(t, db, c.ID))
	})

	t.Run("update keeps an unchanged legacy plaintext password", func(t *testing.T) {
		t.Parallel()
		db := setupTestDBWithAllTables(t)
		keyed := NewGORMClusterRepository(db, testRegistryKey)
		c := registryCluster("a", "x")
		require.NoError(t, keyed.Create(ctx, c))
		require.NoError(t, db.Model(&models.Cluster{}).Where("id = ?", c.ID).Update("registry_password", "legacy-plain").Error)

		repo := NewGORMClusterRepository(db, "")
		found, err := repo.FindByID(ctx, c.ID)
		require.NoError(t, err)
		found.Region = "westus"
		require.NoError(t, repo.Update(ctx, found))
		assert.Equal(t, "legacy-plain", storedPassword(t, db, c.ID))

		found.RegistryPassword = "changed"
		require.Error(t, repo.Update(ctx, found))
		assert.Equal(t, "legacy-plain", storedPassword(t, db, c.ID))
	})
}

func TestClusterRepository_RegistryPasswordWithKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMClusterRepository(db, testRegistryKey)

	c := registryCluster("a", "s3cret-pass")
	require.NoError(t, repo.Create(ctx, c))
	raw := storedPassword(t, db, c.ID)
	assert.NotEqual(t, "s3cret-pass", raw)
	require.True(t, strings.HasPrefix(raw, encryptedPrefix), "new ciphertext has the version prefix")
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, encryptedPrefix))
	require.NoError(t, err)
	plain, err := crypto.Decrypt(decoded, crypto.DeriveKey(testRegistryKey))
	require.NoError(t, err)
	assert.Equal(t, "s3cret-pass", string(plain))

	found, err := repo.FindByID(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "s3cret-pass", found.RegistryPassword)

	found.RegistryPassword = "other-pass"
	require.NoError(t, repo.Update(ctx, found))
	assert.NotEqual(t, "other-pass", storedPassword(t, db, c.ID))
	again, err := repo.FindByID(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "other-pass", again.RegistryPassword)

	// A legacy plaintext password is read as is and encrypted on the next save.
	require.NoError(t, db.Model(&models.Cluster{}).Where("id = ?", c.ID).Update("registry_password", "legacy-plain").Error)
	legacy, err := repo.FindByID(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "legacy-plain", legacy.RegistryPassword)
	require.NoError(t, repo.Update(ctx, legacy))
	assert.NotEqual(t, "legacy-plain", storedPassword(t, db, c.ID))
}

func TestDecryptRegistryPassword_Rules(t *testing.T) {
	t.Parallel()
	key := crypto.DeriveKey("key")
	enc, err := crypto.Encrypt([]byte("pw"), key)
	require.NoError(t, err)
	raw := base64.StdEncoding.EncodeToString(enc)
	wrongKey, err := crypto.Encrypt([]byte("pw"), crypto.DeriveKey("other"))
	require.NoError(t, err)
	wrongRaw := base64.StdEncoding.EncodeToString(wrongKey)
	// A 52-character legacy password and a 40-character hex token are valid base64 of 28 bytes or more.
	longPlain := strings.Repeat("ab12", 13)
	hexToken := strings.Repeat("0123456789", 4)

	tests := []struct {
		name       string
		stored     string
		want       string
		wantUnread bool
	}{
		{"prefixed ciphertext", encryptedPrefix + raw, "pw", false},
		{"unprefixed ciphertext of an older version", raw, "pw", false},
		{"short base64 plaintext", "password", "password", false},
		{"non base64 plaintext", "p@ss word!", "p@ss word!", false},
		{"long base64 plaintext is isolated", longPlain, "", true},
		{"hex token is isolated", hexToken, "", true},
		{"prefixed ciphertext of another key is isolated", encryptedPrefix + wrongRaw, "", true},
		{"prefixed garbage is isolated", encryptedPrefix + "!!", "", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &GORMClusterRepository{encryptionKey: key}
			c := &models.Cluster{ID: "c1", RegistryPassword: tt.stored}
			require.NoError(t, repo.decryptRegistryPassword(c), "a read never fails")
			assert.Equal(t, tt.want, c.RegistryPassword)
			assert.Equal(t, tt.wantUnread, c.RegistryPasswordUnreadable)
		})
	}

	t.Run("prefixed value without a key is never returned as a password", func(t *testing.T) {
		t.Parallel()
		repo := &GORMClusterRepository{}
		c := &models.Cluster{ID: "c1", RegistryPassword: encryptedPrefix + raw}
		require.NoError(t, repo.decryptRegistryPassword(c))
		assert.Empty(t, c.RegistryPassword)
		assert.True(t, c.RegistryPasswordUnreadable)
	})
}

// A row that cannot be decrypted must not fail List or FindByID, and a repair
// PUT or an unrelated save must not lose or overwrite the stored value.
func TestClusterRepository_UnreadablePasswordRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMClusterRepository(db, testRegistryKey)

	good := registryCluster("good", "good-pw")
	require.NoError(t, repo.Create(ctx, good))
	bad := registryCluster("bad", "x")
	require.NoError(t, repo.Create(ctx, bad))
	longPlain := strings.Repeat("ab12", 13)
	require.NoError(t, db.Model(&models.Cluster{}).Where("id = ?", bad.ID).Update("registry_password", longPlain).Error)

	list, err := repo.List(ctx)
	require.NoError(t, err, "one unreadable row does not fail List")
	require.Len(t, list, 2)
	for _, c := range list {
		if c.ID == good.ID {
			assert.Equal(t, "good-pw", c.RegistryPassword)
		} else {
			assert.Empty(t, c.RegistryPassword)
			assert.True(t, c.RegistryPasswordUnreadable)
			assert.Nil(t, c.RegistryConfig(), "no pull secret for this cluster")
		}
	}

	found, err := repo.FindByID(ctx, bad.ID)
	require.NoError(t, err)
	assert.Empty(t, found.RegistryPassword)

	// A save of the in-memory empty value keeps the stored value.
	found.Region = "west"
	require.NoError(t, repo.Update(ctx, found))
	assert.Equal(t, longPlain, storedPassword(t, db, bad.ID))

	// A repair with a new password replaces it with prefixed ciphertext.
	found.RegistryPassword = "repaired"
	require.NoError(t, repo.Update(ctx, found))
	assert.True(t, strings.HasPrefix(storedPassword(t, db, bad.ID), encryptedPrefix))
	again, err := repo.FindByID(ctx, bad.ID)
	require.NoError(t, err)
	assert.Equal(t, "repaired", again.RegistryPassword)
	assert.False(t, again.RegistryPasswordUnreadable)
}

func TestClusterRepository_CountRegistryPasswordsWithoutKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupTestDBWithAllTables(t)
	keyed := NewGORMClusterRepository(db, testRegistryKey)
	for _, name := range []string{"a", "b", "c"} {
		c := registryCluster(name, "pw")
		require.NoError(t, keyed.Create(ctx, c))
		if name != "c" {
			require.NoError(t, db.Model(&models.Cluster{}).Where("id = ?", c.ID).Update("registry_password", "plain").Error)
		}
	}
	none := &models.Cluster{Name: "d", APIServerURL: "https://k8s.example.com"}
	require.NoError(t, keyed.Create(ctx, none))

	noKey := NewGORMClusterRepository(db, "")
	cleartext, encrypted, err := noKey.CountRegistryPasswordsWithoutKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, cleartext)
	assert.Equal(t, 1, encrypted)

	cleartext, encrypted, err = keyed.CountRegistryPasswordsWithoutKey(ctx)
	require.NoError(t, err)
	assert.Zero(t, cleartext+encrypted)
}

func TestClusterRepository_CheckCredentialsStorable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupTestDBWithAllTables(t)
	noKey := NewGORMClusterRepository(db, "")
	keyed := NewGORMClusterRepository(db, testRegistryKey)

	assert.NoError(t, keyed.CheckCredentialsStorable(ctx, &models.Cluster{KubeconfigData: "kc", RegistryPassword: "pw"}))
	assert.NoError(t, noKey.CheckCredentialsStorable(ctx, &models.Cluster{}))
	assert.ErrorIs(t, noKey.CheckCredentialsStorable(ctx, &models.Cluster{RegistryPassword: "pw"}), dberrors.ErrValidation)
	assert.ErrorIs(t, noKey.CheckCredentialsStorable(ctx, &models.Cluster{KubeconfigData: "kc"}), dberrors.ErrValidation)
}
