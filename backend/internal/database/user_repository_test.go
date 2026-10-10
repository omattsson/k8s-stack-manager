package database

import (
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupUserRepo(t *testing.T) *GORMUserRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMUserRepository(db)
}

func TestGORMUserRepository_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    models.User
		wantErr bool
		errType error
	}{
		{
			name:    "success",
			user:    models.User{ID: "u1", Username: "alice", Role: "developer"},
			wantErr: false,
		},
		{
			name:    "empty username fails validation",
			user:    models.User{ID: "u2", Username: ""},
			wantErr: true,
			errType: dberrors.ErrValidation,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			err := repo.Create(&tt.user)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, tt.user.CreatedAt)
				assert.NotEmpty(t, tt.user.UpdatedAt)
			}
		})
	}
}

func TestGORMUserRepository_FindByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		seed    bool
		wantErr bool
	}{
		{name: "found", id: "u-find-1", seed: true, wantErr: false},
		{name: "not found", id: "nonexistent", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.User{ID: tt.id, Username: "user-" + tt.id, Role: "developer"}))
			}
			user, err := repo.FindByID(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, user)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.id, user.ID)
			}
		})
	}
}

func TestGORMUserRepository_FindByUsername(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	require.NoError(t, repo.Create(&models.User{ID: "u-fname", Username: "findme", Role: "developer"}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		user, err := repo.FindByUsername("findme")
		require.NoError(t, err)
		assert.Equal(t, "findme", user.Username)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByUsername("nope")
		assert.Error(t, err)
	})
}

func TestGORMUserRepository_FindByExternalID(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	extID := "ext-123"
	require.NoError(t, repo.Create(&models.User{
		ID:           "u-ext",
		Username:     "external-user",
		Role:         "developer",
		AuthProvider: "oidc",
		ExternalID:   &extID,
	}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		user, err := repo.FindByExternalID("oidc", "ext-123")
		require.NoError(t, err)
		assert.Equal(t, "external-user", user.Username)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByExternalID("oidc", "nope")
		assert.Error(t, err)
	})
}

func TestGORMUserRepository_Update(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	user := models.User{ID: "u-upd", Username: "before", Role: "developer"}
	require.NoError(t, repo.Create(&user))

	user.DisplayName = "Updated Name"
	err := repo.Update(&user)
	require.NoError(t, err)

	found, err := repo.FindByID("u-upd")
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", found.DisplayName)
}

func TestGORMUserRepository_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		seed    bool
		wantErr bool
	}{
		{name: "success", id: "u-del-1", seed: true, wantErr: false},
		{name: "not found", id: "u-del-none", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.User{ID: tt.id, Username: "del-" + tt.id, Role: "developer"}))
			}
			err := repo.Delete(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				_, err := repo.FindByID(tt.id)
				assert.Error(t, err)
			}
		})
	}
}

func TestGORMUserRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	require.NoError(t, repo.Create(&models.User{ID: "u-l1", Username: "list1", Role: "developer"}))
	require.NoError(t, repo.Create(&models.User{ID: "u-l2", Username: "list2", Role: "admin"}))

	users, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, users, 2)
}

func TestGORMUserRepository_GuardedChanges(t *testing.T) {
	t.Parallel()

	admin := func(id string, disabled bool) models.User {
		return models.User{ID: id, Username: id, Role: models.RoleAdmin, Disabled: disabled}
	}
	user := func(id, role string) models.User {
		return models.User{ID: id, Username: id, Role: role}
	}

	type result struct {
		role     string
		disabled bool
		gone     bool
	}

	tests := []struct {
		name      string
		users     []models.User
		run       func(r *GORMUserRepository) error
		target    string
		want      result
		wantErrIs error
	}{
		{
			name:   "role: user to devops",
			users:  []models.User{admin("c", false), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { _, err := r.UpdateRole("c", "u1", "devops"); return err },
			target: "u1", want: result{role: "devops"},
		},
		{
			name:   "role: unchanged is a no-op",
			users:  []models.User{admin("c", false), user("u1", "devops")},
			run:    func(r *GORMUserRepository) error { _, err := r.UpdateRole("c", "u1", "devops"); return err },
			target: "u1", want: result{role: "devops"},
		},
		{
			name:   "role: demote another admin",
			users:  []models.User{admin("c", false), admin("a1", false)},
			run:    func(r *GORMUserRepository) error { _, err := r.UpdateRole("c", "a1", "user"); return err },
			target: "a1", want: result{role: "user"},
		},
		{
			name:   "role: last enabled admin keeps the role",
			users:  []models.User{admin("a1", false), admin("a2", true)},
			run:    func(r *GORMUserRepository) error { _, err := r.UpdateRole("a1", "a1", "devops"); return err },
			target: "a1", want: result{role: models.RoleAdmin}, wantErrIs: models.ErrLastAdmin,
		},
		{
			name:   "role: disabled caller is refused",
			users:  []models.User{admin("c", true), admin("a1", false), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { _, err := r.UpdateRole("c", "u1", "admin"); return err },
			target: "u1", want: result{role: "user"}, wantErrIs: models.ErrCallerNotAdmin,
		},
		{
			name:   "role: non-admin caller is refused",
			users:  []models.User{user("c", "devops"), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { _, err := r.UpdateRole("c", "u1", "admin"); return err },
			target: "u1", want: result{role: "user"}, wantErrIs: models.ErrCallerNotAdmin,
		},
		{
			name:      "role: unknown user",
			users:     []models.User{admin("c", false)},
			run:       func(r *GORMUserRepository) error { _, err := r.UpdateRole("c", "ghost", "user"); return err },
			wantErrIs: dberrors.ErrNotFound,
		},
		{
			name:   "disable: other user",
			users:  []models.User{admin("c", false), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { return r.SetDisabled("c", "u1", true) },
			target: "u1", want: result{role: "user", disabled: true},
		},
		{
			name:   "disable: last enabled admin is refused",
			users:  []models.User{admin("a1", false)},
			run:    func(r *GORMUserRepository) error { return r.SetDisabled("a1", "a1", true) },
			target: "a1", want: result{role: models.RoleAdmin}, wantErrIs: models.ErrLastAdmin,
		},
		{
			name:   "enable: allowed without another admin",
			users:  []models.User{admin("c", false), admin("a1", true)},
			run:    func(r *GORMUserRepository) error { return r.SetDisabled("c", "a1", false) },
			target: "a1", want: result{role: models.RoleAdmin},
		},
		{
			name:   "disable: disabled caller is refused",
			users:  []models.User{admin("c", true), admin("a1", false), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { return r.SetDisabled("c", "u1", true) },
			target: "u1", want: result{role: "user"}, wantErrIs: models.ErrCallerNotAdmin,
		},
		{
			name:   "delete: other admin",
			users:  []models.User{admin("c", false), admin("a1", false)},
			run:    func(r *GORMUserRepository) error { return r.DeleteGuarded("c", "a1") },
			target: "a1", want: result{gone: true},
		},
		{
			name:   "delete: last enabled admin is refused",
			users:  []models.User{admin("a1", false), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { return r.DeleteGuarded("a1", "a1") },
			target: "a1", want: result{role: models.RoleAdmin}, wantErrIs: models.ErrLastAdmin,
		},
		{
			name:   "delete: non-admin caller is refused",
			users:  []models.User{user("c", "user"), user("u1", "user")},
			run:    func(r *GORMUserRepository) error { return r.DeleteGuarded("c", "u1") },
			target: "u1", want: result{role: "user"}, wantErrIs: models.ErrCallerNotAdmin,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			for i := range tt.users {
				require.NoError(t, repo.Create(&tt.users[i]))
			}

			err := tt.run(repo)
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			} else {
				require.NoError(t, err)
			}
			if tt.target == "" {
				return
			}
			u, err := repo.FindByID(tt.target)
			if tt.want.gone {
				require.ErrorIs(t, err, dberrors.ErrNotFound)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.role, u.Role)
			assert.Equal(t, tt.want.disabled, u.Disabled)
		})
	}
}

func TestGORMUserRepository_TargetedUpdates(t *testing.T) {
	t.Parallel()

	str := func(s string) *string { return &s }

	tests := []struct {
		name      string
		user      models.User
		run       func(r *GORMUserRepository) error
		want      models.User // ID, Role, Disabled, Email, DisplayName, AuthProvider, PasswordHash checked
		wantErrIs error
	}{
		{
			name: "password update keeps role and disabled",
			user: models.User{ID: "u1", Username: "u1", Role: "devops", Disabled: true, PasswordHash: "old"},
			run:  func(r *GORMUserRepository) error { return r.UpdatePassword("u1", "new") },
			want: models.User{Role: "devops", Disabled: true, PasswordHash: "new", AuthProvider: "local"},
		},
		{
			name:      "password update of an unknown user",
			user:      models.User{ID: "u1", Username: "u1", Role: "user"},
			run:       func(r *GORMUserRepository) error { return r.UpdatePassword("ghost", "new") },
			want:      models.User{Role: "user", AuthProvider: "local"},
			wantErrIs: dberrors.ErrNotFound,
		},
		{
			name: "profile update of an SSO user sets the role and keeps disabled",
			user: models.User{ID: "u1", Username: "u1", Role: "user", AuthProvider: "oidc", Disabled: true},
			run: func(r *GORMUserRepository) error {
				return r.UpdateProfile("u1", models.UserProfileUpdate{Email: str("a@example.com"), DisplayName: str("A"), Role: str("devops")})
			},
			want: models.User{Role: "devops", Disabled: true, Email: "a@example.com", DisplayName: "A", AuthProvider: "oidc"},
		},
		{
			name: "profile update never sets the role of a local user",
			user: models.User{ID: "u1", Username: "u1", Role: "user", AuthProvider: "local", PasswordHash: "h"},
			run: func(r *GORMUserRepository) error {
				return r.UpdateProfile("u1", models.UserProfileUpdate{Role: str("admin")})
			},
			want: models.User{Role: "user", AuthProvider: "local", PasswordHash: "h"},
		},
		{
			name: "link to a provider sets the role and clears the password",
			user: models.User{ID: "u1", Username: "u1", Role: "user", AuthProvider: "local", PasswordHash: "h", Disabled: true},
			run: func(r *GORMUserRepository) error {
				return r.UpdateProfile("u1", models.UserProfileUpdate{LinkProvider: str("oidc"), ExternalID: str("sub-1"), Role: str("devops")})
			},
			want: models.User{Role: "devops", AuthProvider: "oidc", Disabled: true},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			require.NoError(t, repo.Create(&tt.user))

			err := tt.run(repo)
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			} else {
				require.NoError(t, err)
			}
			u, err := repo.FindByID("u1")
			require.NoError(t, err)
			assert.Equal(t, tt.want.Role, u.Role)
			assert.Equal(t, tt.want.Disabled, u.Disabled)
			assert.Equal(t, tt.want.Email, u.Email)
			assert.Equal(t, tt.want.DisplayName, u.DisplayName)
			assert.Equal(t, tt.want.AuthProvider, u.AuthProvider)
			assert.Equal(t, tt.want.PasswordHash, u.PasswordHash)
		})
	}
}
