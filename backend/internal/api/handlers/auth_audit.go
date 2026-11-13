package handlers

import (
	"encoding/json"
	"log/slog"

	"backend/internal/api/middleware"
	"backend/internal/ctxutil"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// writeAuthAudit writes a sign-in audit entry (#784). It reads the request
// context while the handler runs, then writes in a goroutine, so a slow
// database never delays the answer. A known user ID without a username gets
// the username from users; the username also names the user entity. Never
// pass a typed username or a secret.
func writeAuthAudit(c *gin.Context, repo models.AuditLogRepository, users models.UserRepository, e *models.AuditLog) {
	if repo == nil {
		return
	}
	e.EntityType = "user"
	e.EntityID = e.UserID
	middleware.ApplyRequestContext(c, e)
	ctx, cancel := ctxutil.Detach(c.Request.Context(), ctxutil.WriteTimeout)
	middleware.GoAuditWrite(func() {
		defer cancel()
		if e.Username == "" && e.UserID != "" && users != nil {
			if u, err := users.FindByID(ctx, e.UserID); err == nil && u != nil {
				e.Username = u.Username
			}
		}
		if e.EntityName == "" && e.UserID != "" {
			e.EntityName = middleware.TruncateUTF8(e.Username, models.MaxAuditEntityNameLen)
		}
		if err := repo.Create(ctx, e); err != nil {
			slog.Error("failed to create sign-in audit entry", "action", e.Action, "user_id", e.UserID, "error", err)
		}
	})
}

// auditReason returns the details of a failed sign-in.
func auditReason(reason string) string {
	raw, _ := json.Marshal(map[string]string{"reason": reason})
	return string(raw)
}
