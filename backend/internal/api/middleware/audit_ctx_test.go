package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type auditCtxKey struct{}

// ctxAuditLogger fails on a done context, as the repository does, and keeps
// the marker of the context.
type ctxAuditLogger struct {
	mockAuditLogger
	marker atomic.Value
}

func (l *ctxAuditLogger) Create(ctx context.Context, log *models.AuditLog) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.marker.Store(ctx.Value(auditCtxKey{}))
	return l.mockAuditLogger.Create(ctx, log)
}

// TestAuditMiddleware_WritesAfterTheClientLeft checks that the audit entry is
// written when the client leaves during the handler: the entry uses the
// request context without its cancel (#656), and keeps its values (trace).
func TestAuditMiddleware_WritesAfterTheClientLeft(t *testing.T) {
	t.Parallel()

	logger := &ctxAuditLogger{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), auditCtxKey{}, "request"))
	defer cancel()
	r := gin.New()
	r.Use(NewAuditMiddleware(logger))
	r.POST("/test", func(c *gin.Context) {
		cancel()
		c.JSON(http.StatusCreated, gin.H{"id": "1"})
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/test", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Eventually(t, func() bool { return logger.count() == 1 }, 2*time.Second, 10*time.Millisecond,
		"the audit entry is written although the request context is cancelled")
	assert.Equal(t, "request", logger.marker.Load(), "the entry keeps the values of the request context")
}
