package database

import (
	"context"
	"encoding/base64"
	"errors"
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
	decoded, err := base64.StdEncoding.DecodeString(raw)
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

func TestDecryptRegistryPassword_LegacyPlaintextRule(t *testing.T) {
	t.Parallel()
	key := crypto.DeriveKey("key")
	enc, err := crypto.Encrypt([]byte("pw"), key)
	require.NoError(t, err)
	valid := base64.StdEncoding.EncodeToString(enc)
	wrongKey, err := crypto.Encrypt([]byte("pw"), crypto.DeriveKey("other"))
	require.NoError(t, err)

	tests := []struct {
		name    string
		stored  string
		want    string
		wantErr bool
	}{
		{"ciphertext", valid, "pw", false},
		{"short base64 plaintext", "password", "password", false},
		{"base64 plaintext below ciphertext length", "QUJDREVGR0g=", "QUJDREVGR0g=", false},
		{"non base64 plaintext", "p@ss word!", "p@ss word!", false},
		{"ciphertext of another key is an error", base64.StdEncoding.EncodeToString(wrongKey), "", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &GORMClusterRepository{encryptionKey: key}
			c := &models.Cluster{RegistryPassword: tt.stored}
			err := repo.decryptRegistryPassword(c)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, c.RegistryPassword)
		})
	}
}

func TestClusterRepository_CountPlaintextRegistryPasswords(t *testing.T) {
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
	plain := &models.Cluster{Name: "d", APIServerURL: "https://k8s.example.com"}
	require.NoError(t, keyed.Create(ctx, plain))

	noKey := NewGORMClusterRepository(db, "")
	n, err := noKey.CountPlaintextRegistryPasswords(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "without a key every stored password counts")

	n, err = keyed.CountPlaintextRegistryPasswords(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}
