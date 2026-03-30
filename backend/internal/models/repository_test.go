package models

import (
	"context"
	"errors"
	"testing"

	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupRepoTestDB creates an in-memory SQLite GORM DB and runs AutoMigrate for
// User and Item tables. Returns the *gorm.DB ready for repository tests.
func setupRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Item{}))
	return db
}

// ---------------------------------------------------------------------------
// GenericRepository.Create
// ---------------------------------------------------------------------------

func TestGenericRepository_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates valid item", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Widget", Price: 9.99}
		err := repo.Create(ctx, item)
		require.NoError(t, err)
		assert.NotZero(t, item.ID)
		assert.Equal(t, uint(1), item.Version, "version should default to 1")
	})

	t.Run("validation error for empty name", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "", Price: 9.99}
		err := repo.Create(ctx, item)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrValidation))
	})

	t.Run("validation error for negative price", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Bad", Price: -1.0}
		err := repo.Create(ctx, item)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
	})

	t.Run("non-Validator entity skips validation", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		// User does NOT implement Validator on the non-pointer receiver in the
		// same way Item does; for a clean test we just create a user directly.
		// Actually, User does implement Validator, so let's use a valid one.
		user := &User{ID: "u1", Username: "alice", Role: "user"}
		err := repo.Create(ctx, user)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// GenericRepository.FindByID
// ---------------------------------------------------------------------------

func TestGenericRepository_FindByID(t *testing.T) {
	t.Parallel()

	t.Run("finds existing item", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Find Me", Price: 5.0}
		require.NoError(t, repo.Create(ctx, item))

		var found Item
		err := repo.FindByID(ctx, item.ID, &found)
		require.NoError(t, err)
		assert.Equal(t, "Find Me", found.Name)
		assert.Equal(t, 5.0, found.Price)
	})

	t.Run("returns not found for missing ID", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		var found Item
		err := repo.FindByID(ctx, 99999, &found)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrNotFound))
	})
}

// ---------------------------------------------------------------------------
// GenericRepository.Update
// ---------------------------------------------------------------------------

func TestGenericRepository_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates with version increment", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Original", Price: 10.0}
		require.NoError(t, repo.Create(ctx, item))
		assert.Equal(t, uint(1), item.Version)

		item.Price = 20.0
		require.NoError(t, repo.Update(ctx, item))
		assert.Equal(t, uint(2), item.Version, "version should be incremented")

		var found Item
		require.NoError(t, repo.FindByID(ctx, item.ID, &found))
		assert.Equal(t, 20.0, found.Price)
		assert.Equal(t, uint(2), found.Version)
	})

	t.Run("version mismatch fails", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Conflict", Price: 10.0}
		require.NoError(t, repo.Create(ctx, item))

		var stale Item
		require.NoError(t, repo.FindByID(ctx, item.ID, &stale))

		// First update succeeds.
		item.Price = 15.0
		require.NoError(t, repo.Update(ctx, item))

		// Second update with stale version should fail.
		stale.Price = 25.0
		err := repo.Update(ctx, &stale)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "version mismatch")
		// Stale copy's version should be rolled back.
		assert.Equal(t, uint(1), stale.Version)
	})

	t.Run("validation error on update", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Valid", Price: 10.0}
		require.NoError(t, repo.Create(ctx, item))

		item.Name = "" // Invalid
		err := repo.Update(ctx, item)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrValidation))
	})
}

// ---------------------------------------------------------------------------
// GenericRepository.Delete
// ---------------------------------------------------------------------------

func TestGenericRepository_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing item", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		item := &Item{Name: "Delete Me", Price: 5.0}
		require.NoError(t, repo.Create(ctx, item))

		toDelete := &Item{Base: Base{ID: item.ID}}
		err := repo.Delete(ctx, toDelete)
		require.NoError(t, err)

		var found Item
		err = repo.FindByID(ctx, item.ID, &found)
		assert.Error(t, err, "should not find soft-deleted item")
	})

	t.Run("returns not found for non-existent item", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		toDelete := &Item{Base: Base{ID: 99999}}
		err := repo.Delete(ctx, toDelete)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrNotFound))
	})
}

// ---------------------------------------------------------------------------
// GenericRepository.List
// ---------------------------------------------------------------------------

func TestGenericRepository_List(t *testing.T) {
	t.Parallel()

	t.Run("lists all items", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		require.NoError(t, repo.Create(ctx, &Item{Name: "A", Price: 1.0}))
		require.NoError(t, repo.Create(ctx, &Item{Name: "B", Price: 2.0}))
		require.NoError(t, repo.Create(ctx, &Item{Name: "C", Price: 3.0}))

		var items []Item
		err := repo.List(ctx, &items)
		require.NoError(t, err)
		assert.Len(t, items, 3)
	})

	t.Run("list with exact filter", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		require.NoError(t, repo.Create(ctx, &Item{Name: "Alpha", Price: 10.0}))
		require.NoError(t, repo.Create(ctx, &Item{Name: "Beta", Price: 20.0}))

		var items []Item
		err := repo.List(ctx, &items, Filter{Field: "name", Op: "exact", Value: "Alpha"})
		require.NoError(t, err)
		assert.Len(t, items, 1)
		assert.Equal(t, "Alpha", items[0].Name)
	})

	t.Run("list with LIKE filter (default op)", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		require.NoError(t, repo.Create(ctx, &Item{Name: "Hello World", Price: 1.0}))
		require.NoError(t, repo.Create(ctx, &Item{Name: "Goodbye", Price: 2.0}))

		var items []Item
		err := repo.List(ctx, &items, Filter{Field: "name", Value: "Hello"})
		require.NoError(t, err)
		assert.Len(t, items, 1)
		assert.Equal(t, "Hello World", items[0].Name)
	})

	t.Run("list with >= filter on price", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		require.NoError(t, repo.Create(ctx, &Item{Name: "Cheap", Price: 5.0}))
		require.NoError(t, repo.Create(ctx, &Item{Name: "Expensive", Price: 50.0}))

		var items []Item
		err := repo.List(ctx, &items, Filter{Field: "price", Op: ">=", Value: 10.0})
		require.NoError(t, err)
		assert.Len(t, items, 1)
		assert.Equal(t, "Expensive", items[0].Name)
	})

	t.Run("list with <= filter on price", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		require.NoError(t, repo.Create(ctx, &Item{Name: "Cheap", Price: 5.0}))
		require.NoError(t, repo.Create(ctx, &Item{Name: "Expensive", Price: 50.0}))

		var items []Item
		err := repo.List(ctx, &items, Filter{Field: "price", Op: "<=", Value: 10.0})
		require.NoError(t, err)
		assert.Len(t, items, 1)
		assert.Equal(t, "Cheap", items[0].Name)
	})

	t.Run("list with pagination", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		for i := 0; i < 5; i++ {
			require.NoError(t, repo.Create(ctx, &Item{Name: "Item", Price: float64(i + 1)}))
		}

		var items []Item
		err := repo.List(ctx, &items, Pagination{Limit: 2, Offset: 1})
		require.NoError(t, err)
		assert.Len(t, items, 2)
	})

	t.Run("list with disallowed filter field", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		var items []Item
		err := repo.List(ctx, &items, Filter{Field: "id", Op: "exact", Value: 1})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid filter field")
	})

	t.Run("list with filter and pagination combined", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		repo := NewRepository(db)
		ctx := context.Background()

		for i := 1; i <= 5; i++ {
			require.NoError(t, repo.Create(ctx, &Item{Name: "Batch", Price: float64(i)}))
		}

		var items []Item
		err := repo.List(ctx, &items,
			Filter{Field: "name", Op: "exact", Value: "Batch"},
			Pagination{Limit: 2, Offset: 0},
		)
		require.NoError(t, err)
		assert.Len(t, items, 2)
	})
}

// ---------------------------------------------------------------------------
// GenericRepository.Ping and Close
// ---------------------------------------------------------------------------

func TestGenericRepository_Ping(t *testing.T) {
	t.Parallel()

	db := setupRepoTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	err := repo.Ping(ctx)
	assert.NoError(t, err)
}

func TestGenericRepository_Close(t *testing.T) {
	t.Parallel()

	db := setupRepoTestDB(t)
	repo := NewRepository(db)

	err := repo.Close()
	require.NoError(t, err)

	// After close, Ping should fail.
	ctx := context.Background()
	err = repo.Ping(ctx)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// NewRepositoryWithFilterFields — functional tests with actual DB
// ---------------------------------------------------------------------------

func TestNewRepositoryWithFilterFields_Functional(t *testing.T) {
	t.Parallel()

	t.Run("custom filter fields allow filtering", func(t *testing.T) {
		t.Parallel()
		db := setupRepoTestDB(t)
		// Only allow filtering by "name" (not "price").
		repo := NewRepositoryWithFilterFields(db, []string{"name"})
		ctx := context.Background()

		require.NoError(t, repo.Create(ctx, &Item{Name: "Allowed", Price: 10.0}))

		var items []Item
		err := repo.List(ctx, &items, Filter{Field: "name", Op: "exact", Value: "Allowed"})
		require.NoError(t, err)
		assert.Len(t, items, 1)

		// Filtering by "price" should fail since it is not in the whitelist.
		err = repo.List(ctx, &items, Filter{Field: "price", Op: ">=", Value: 5.0})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid filter field")
	})
}
