package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/auth"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// concurrentChangeRepo applies a change to the stored row right after a
// FindByID read, to simulate a concurrent admin change between the read and
// the write of a handler.
type concurrentChangeRepo struct {
	*MockUserRepository
	change func(stored *models.User)
}

func (r *concurrentChangeRepo) FindByID(id string) (*models.User, error) {
	u, err := r.MockUserRepository.FindByID(id)
	if err == nil && r.change != nil {
		r.MockUserRepository.mu.Lock()
		if stored, ok := r.MockUserRepository.lookupLocked(id); ok {
			r.change(stored)
		}
		r.MockUserRepository.mu.Unlock()
	}
	return u, err
}

// TestResetUserPassword_KeepsConcurrentChanges checks that a password reset
// writes only the hash: a demote or disable that commits between the read
// and the write stays, and the revocation uses the stored ID.
func TestResetUserPassword_KeepsConcurrentChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pathID       string
		change       func(u *models.User)
		wantRole     string
		wantDisabled bool
	}{
		{name: "demote during the reset", pathID: "user-t", change: func(u *models.User) { u.Role = "user" }, wantRole: "user"},
		{name: "disable during the reset", pathID: "user-t", change: func(u *models.User) { u.Disabled = true }, wantRole: "devops", wantDisabled: true},
		{name: "upper-case path ID", pathID: "USER-T", wantRole: "devops"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRevokeFixture(t)
			mock := NewMockUserRepository()
			require.NoError(t, mock.Create(&models.User{ID: "admin-c", Username: "c", Role: "admin"}))
			require.NoError(t, mock.Create(&models.User{ID: "user-t", Username: "t", Role: "devops", PasswordHash: "old"}))

			h := NewUserHandler(&concurrentChangeRepo{MockUserRepository: mock, change: tt.change}, f.refreshRepo, f.apiKeyRepo)
			h.SetSessionStore(f.store)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("admin-c", "admin"))
			r.PUT("/api/v1/users/:id/password", h.ResetUserPassword)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/users/"+tt.pathID+"/password", bytes.NewBufferString(`{"password":"a-new-password-1"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			u, err := mock.FindByID("user-t")
			require.NoError(t, err)
			assert.Equal(t, tt.wantRole, u.Role)
			assert.Equal(t, tt.wantDisabled, u.Disabled)
			assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("a-new-password-1")))
			assert.True(t, f.store.wasBlockUserCalledFor("user-t"), "revoke must use the stored ID")
			if tt.pathID != "user-t" {
				assert.False(t, f.store.wasBlockUserCalledFor(tt.pathID))
			}
		})
	}
}

// TestOIDCUserSync_KeepsConcurrentDisable checks that the SSO profile sync and
// the link of a local user write only their columns: a disable that commits
// after the read stays.
func TestOIDCUserSync_KeepsConcurrentDisable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		link     bool
	}{
		{name: "sync of an SSO user", provider: "oidc"},
		{name: "link of a local user", provider: "local", link: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, _, repo := newOIDCHandlerSetup(t, false)
			require.NoError(t, repo.Create(&models.User{ID: "u1", Username: "sso", Role: "user", AuthProvider: tt.provider, PasswordHash: "h"}))
			stale, err := repo.FindByID("u1")
			require.NoError(t, err)

			// Concurrent disable after the read.
			repo.mu.Lock()
			repo.users["u1"].Disabled = true
			repo.mu.Unlock()

			oidcUser := &auth.OIDCUser{Subject: "sub-1", Email: "new@example.com", Name: "New Name", Roles: []string{"k8s-stack-devops"}}
			if tt.link {
				_, err = h.linkLocalUserToOIDC(stale, oidcUser)
			} else {
				_, err = h.updateExistingOIDCUser(stale, oidcUser)
			}
			require.NoError(t, err)

			u, err := repo.FindByID("u1")
			require.NoError(t, err)
			assert.True(t, u.Disabled, "the concurrent disable must stay")
			assert.Equal(t, "new@example.com", u.Email)
			assert.Equal(t, "New Name", u.DisplayName)
			assert.Equal(t, "devops", u.Role)
			assert.Equal(t, "oidc", u.AuthProvider)
			if tt.link {
				assert.Empty(t, u.PasswordHash)
			}
		})
	}
}
