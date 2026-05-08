package database

import (
	"testing"

	"backend/internal/models"
	"backend/pkg/crypto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptKubeconfig(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{
		encryptionKey: crypto.DeriveKey("test-key-for-encryption"),
	}

	cluster := &models.Cluster{
		KubeconfigData: "apiVersion: v1\nkind: Config\nclusters: []",
	}
	original := cluster.KubeconfigData

	restored, err := repo.encryptKubeconfig(cluster)
	require.NoError(t, err)
	assert.Equal(t, original, restored)
	assert.NotEqual(t, original, cluster.KubeconfigData, "data should be encrypted")

	err = repo.decryptKubeconfig(cluster)
	require.NoError(t, err)
	assert.Equal(t, original, cluster.KubeconfigData)
}

func TestEncryptKubeconfig_EmptyData(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{
		encryptionKey: crypto.DeriveKey("test-key"),
	}

	cluster := &models.Cluster{KubeconfigData: ""}
	restored, err := repo.encryptKubeconfig(cluster)
	require.NoError(t, err)
	assert.Empty(t, restored)
	assert.Empty(t, cluster.KubeconfigData)
}

func TestEncryptKubeconfig_NoEncryptionKey(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{}
	cluster := &models.Cluster{KubeconfigData: "some data"}

	_, err := repo.encryptKubeconfig(cluster)
	assert.Error(t, err, "should fail when encryption key is not set")
}

func TestDecryptKubeconfig_EmptyData(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{
		encryptionKey: crypto.DeriveKey("test-key"),
	}
	cluster := &models.Cluster{KubeconfigData: ""}

	err := repo.decryptKubeconfig(cluster)
	require.NoError(t, err)
	assert.Empty(t, cluster.KubeconfigData)
}

func TestDecryptKubeconfig_NoKey(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{}
	cluster := &models.Cluster{KubeconfigData: "ciphertext-here"}

	err := repo.decryptKubeconfig(cluster)
	require.NoError(t, err, "should skip decryption when no key")
	assert.Equal(t, "ciphertext-here", cluster.KubeconfigData)
}

func TestDecryptKubeconfig_PlaintextData(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{
		encryptionKey: crypto.DeriveKey("test-key"),
	}
	cluster := &models.Cluster{KubeconfigData: "not-base64-data"}

	err := repo.decryptKubeconfig(cluster)
	require.NoError(t, err, "should treat non-base64 as plaintext")
	assert.Equal(t, "not-base64-data", cluster.KubeconfigData)
}

func TestEncryptDecryptRegistryPassword(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{
		encryptionKey: crypto.DeriveKey("test-key-for-encryption"),
	}

	cluster := &models.Cluster{RegistryPassword: "super-secret-password"}
	original := cluster.RegistryPassword

	restored, err := repo.encryptRegistryPassword(cluster)
	require.NoError(t, err)
	assert.Equal(t, original, restored)
	assert.NotEqual(t, original, cluster.RegistryPassword, "password should be encrypted")

	err = repo.decryptRegistryPassword(cluster)
	require.NoError(t, err)
	assert.Equal(t, original, cluster.RegistryPassword)
}

func TestEncryptRegistryPassword_EmptyOrNoKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		password string
		hasKey   bool
	}{
		{"empty password", "", true},
		{"no encryption key", "secret", false},
		{"both empty", "", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &GORMClusterRepository{}
			if tt.hasKey {
				repo.encryptionKey = crypto.DeriveKey("key")
			}
			cluster := &models.Cluster{RegistryPassword: tt.password}

			restored, err := repo.encryptRegistryPassword(cluster)
			require.NoError(t, err)
			assert.Equal(t, tt.password, restored)
			assert.Equal(t, tt.password, cluster.RegistryPassword)
		})
	}
}

func TestDecryptRegistryPassword_PlaintextData(t *testing.T) {
	t.Parallel()

	repo := &GORMClusterRepository{
		encryptionKey: crypto.DeriveKey("key"),
	}
	cluster := &models.Cluster{RegistryPassword: "not-base64"}

	err := repo.decryptRegistryPassword(cluster)
	require.NoError(t, err, "should treat non-base64 as plaintext")
	assert.Equal(t, "not-base64", cluster.RegistryPassword)
}
