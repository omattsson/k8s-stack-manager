package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/ctxutil"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stalledAddCall is what stalledFavoriteRepo saw when Add started.
type stalledAddCall struct {
	deadline    time.Time
	hasDeadline bool
}

// stalledFavoriteRepo stands for a stalled database: Add waits until its
// context ends, or until the test sends the error that the limit gives.
type stalledFavoriteRepo struct {
	*MockUserFavoriteRepository
	started chan stalledAddCall
	limit   chan struct{}
}

func (r *stalledFavoriteRepo) Add(ctx context.Context, _ *models.UserFavorite) error {
	deadline, ok := ctx.Deadline()
	r.started <- stalledAddCall{deadline: deadline, hasDeadline: ok}
	select {
	case <-ctx.Done():
		return dberrors.NewDatabaseError("add favorite", ctx.Err())
	case <-r.limit:
		// The 30 s limit passed; the timer itself is tested in ctxutil.
		return dberrors.NewDatabaseError("add favorite", context.DeadlineExceeded)
	}
}

// TestDetachedWrite_DeadlineOnStalledDatabase checks a detached write (#674):
// the client that leaves does not end it, it has the ctxutil.WriteTimeout
// deadline, and the DeadlineExceeded of a stalled database answers 500.
func TestDetachedWrite_DeadlineOnStalledDatabase(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	repo := &stalledFavoriteRepo{
		MockUserFavoriteRepository: NewMockUserFavoriteRepository(),
		started:                    make(chan stalledAddCall, 1),
		limit:                      make(chan struct{}),
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Next()
	})
	r.POST("/api/v1/favorites", NewFavoriteHandler(repo).AddFavorite)

	reqCtx, leave := context.WithCancel(context.Background())
	defer leave()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/favorites",
		strings.NewReader(`{"entity_type":"instance","entity_id":"inst-1"}`)).WithContext(reqCtx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		r.ServeHTTP(w, req)
	}()

	call := <-repo.started
	require.True(t, call.hasDeadline, "the detached write has a deadline")
	assert.WithinDuration(t, start.Add(ctxutil.WriteTimeout), call.deadline, 5*time.Second)

	// The client leaves: the write goes on.
	leave()
	select {
	case <-done:
		t.Fatal("the client cancel ended the detached write")
	case <-time.After(50 * time.Millisecond):
	}

	// The stalled write reaches the limit.
	close(repo.limit)
	<-done
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, msgInternalServerError, body["error"], "a timeout does not expose the error")
}
