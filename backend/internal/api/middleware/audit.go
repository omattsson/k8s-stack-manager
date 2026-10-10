package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditLogger allows the audit middleware to persist audit entries.
// models.AuditLogRepository satisfies this interface.
type AuditLogger interface {
	Create(log *models.AuditLog) error
}

// bodyLogWriter wraps gin.ResponseWriter to capture the response body.
type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// NewAuditMiddleware returns middleware that creates an AuditLog entry after
// successful mutating requests (POST, PUT, DELETE with status < 400).
//
// The action, the entity type and the entity ID come from the route table
// (auditRoutes) for operations that are not plain CRUD, for example
// POST /stack-instances/:id/deploy gives deploy | stack_instance | <id>. Other
// routes are plain CRUD: the action comes from the HTTP method (create,
// update, delete) and the entity type from the last resource segment of the
// route. See AuditRouteFor.
func NewAuditMiddleware(repo AuditLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method != http.MethodPost && method != http.MethodPut && method != http.MethodDelete {
			c.Next()
			return
		}

		route := AuditRouteFor(method, c.FullPath())
		if route.Skip {
			c.Next()
			return
		}

		// Capture response body so we can extract entity ID for creates.
		blw := &bodyLogWriter{body: bytes.NewBuffer(nil), ResponseWriter: c.Writer}
		c.Writer = blw

		c.Next()

		// Only audit successful operations.
		if c.Writer.Status() >= 400 {
			return
		}

		entries := buildAuditEntries(c, route, blw.body.Bytes())
		if len(entries) == 0 {
			return
		}

		// Fire and forget — audit failures must not affect the response.
		go func() {
			for _, entry := range entries {
				if err := repo.Create(entry); err != nil {
					slog.Error("failed to create audit log", "error", err)
				}
			}
		}()
	}
}

// AuditRoute describes how the audit middleware records a route.
type AuditRoute struct {
	// Action is the audit action, for example "deploy".
	Action string
	// EntityType is the audit entity type, for example "stack_instance".
	EntityType string
	// IDParam is the path parameter with the entity ID. Empty: the "id"
	// field of the JSON response (for example the ID of a created entity).
	IDParam string
	// DetailParams copies path parameters into the details (parameter name
	// to details key).
	DetailParams map[string]string
	// DetailQuery copies query parameters into the details (same key).
	DetailQuery []string
	// ResultFields copies top-level fields of the JSON response into the
	// details (response field to details key), for example the log ID of a
	// deploy or the ID of a clone.
	ResultFields map[string]string
	// BulkIDField is set for bulk operations: the response has a "results"
	// list, and the middleware writes one entry per item with status
	// "success". The field holds the entity ID of an item.
	BulkIDField string
	// Skip means no audit entry: the request changes nothing worth auditing
	// (notification read state), or the handler writes its own entry.
	Skip bool
	// CRUD is true for a route that is not in the route table: the action
	// comes from the HTTP method and the entity type from the route.
	CRUD bool
}

// Audit actions that are not derived from the HTTP method.
const (
	AuditActionCreate         = "create"
	AuditActionUpdate         = "update"
	AuditActionDelete         = "delete"
	AuditActionDeploy         = "deploy"
	AuditActionStop           = "stop"
	AuditActionClean          = "clean"
	AuditActionRollback       = "rollback"
	AuditActionExtendTTL      = "extend_ttl"
	AuditActionClone          = "clone"
	AuditActionInvokeAction   = "invoke_action"
	AuditActionPublish        = "publish"
	AuditActionUnpublish      = "unpublish"
	AuditActionInstantiate    = "instantiate"
	AuditActionImport         = "import"
	AuditActionUpgrade        = "upgrade"
	AuditActionTest           = "test"
	AuditActionTestConnection = "test_connection"
	AuditActionSetDefault     = "set_default"
	AuditActionRun            = "run"
	AuditActionDisable        = "disable"
	AuditActionEnable         = "enable"
	AuditActionResetPassword  = "reset_password"
	AuditActionChangeRole     = "change_role"
)

// Actions and entity types that other packages write directly (not through
// the middleware).
const (
	AuditActionQuickDeploy           = "quick_deploy"            // handlers.QuickDeployHandler
	AuditActionExpired               = "expired"                 // ttl reaper
	AuditActionCleanupPolicyExecuted = "cleanup_policy_executed" // cleanup scheduler
)

// KnownAuditActions lists every action that new audit entries can have. The
// audit log page offers the same list as filter options.
var KnownAuditActions = []string{
	AuditActionCreate, AuditActionUpdate, AuditActionDelete,
	AuditActionDeploy, AuditActionStop, AuditActionClean, AuditActionRollback,
	AuditActionExtendTTL, AuditActionClone, AuditActionInvokeAction,
	AuditActionPublish, AuditActionUnpublish, AuditActionInstantiate,
	AuditActionImport, AuditActionUpgrade, AuditActionTest,
	AuditActionTestConnection, AuditActionSetDefault, AuditActionRun,
	AuditActionDisable, AuditActionEnable, AuditActionResetPassword,
	AuditActionChangeRole,
	AuditActionQuickDeploy, AuditActionExpired, AuditActionCleanupPolicyExecuted,
}

// KnownAuditEntityTypes lists every entity type that new audit entries can
// have. The audit log page offers the same list as filter options.
var KnownAuditEntityTypes = []string{
	"stack_template", "stack_definition", "stack_instance", "chart_config",
	"value_override", "branch_override", "quota_override", "user", "api_key",
	"cluster", "quota", "shared_values", "cleanup_policy",
	"notification_channel", "notification_subscription",
	"notification_preference", "favorite", "namespace",
}

const auditAPIPrefix = "/api/v1"

// auditRoutes maps "METHOD route pattern" (gin FullPath) to the audit entry
// of operations that are not plain CRUD. Keep it in sync with routes.go; the
// routes test checks that every mutating route gives a known action and
// entity type.
var auditRoutes = map[string]AuditRoute{
	// Templates.
	"POST /templates/:id/publish":     {Action: AuditActionPublish, EntityType: "stack_template", IDParam: "id"},
	"POST /templates/:id/unpublish":   {Action: AuditActionUnpublish, EntityType: "stack_template", IDParam: "id"},
	"POST /templates/:id/instantiate": {Action: AuditActionInstantiate, EntityType: "stack_template", IDParam: "id", ResultFields: map[string]string{"id": "stack_definition_id"}},
	"POST /templates/:id/clone":       {Action: AuditActionClone, EntityType: "stack_template", IDParam: "id", ResultFields: map[string]string{"id": "clone_id"}},
	// Quick deploy writes its own entry (quick_deploy | stack_instance).
	"POST /templates/:id/quick-deploy": {Skip: true},
	"POST /templates/bulk/delete":      {Action: AuditActionDelete, EntityType: "stack_template", BulkIDField: "template_id"},
	"POST /templates/bulk/publish":     {Action: AuditActionPublish, EntityType: "stack_template", BulkIDField: "template_id"},
	"POST /templates/bulk/unpublish":   {Action: AuditActionUnpublish, EntityType: "stack_template", BulkIDField: "template_id"},

	// Stack definitions.
	"POST /stack-definitions/import":      {Action: AuditActionImport, EntityType: "stack_definition"},
	"POST /stack-definitions/:id/upgrade": {Action: AuditActionUpgrade, EntityType: "stack_definition", IDParam: "id"},

	// Stack instances.
	"POST /stack-instances/:id/clone":         {Action: AuditActionClone, EntityType: "stack_instance", IDParam: "id", ResultFields: map[string]string{"id": "clone_id"}},
	"POST /stack-instances/:id/deploy":        {Action: AuditActionDeploy, EntityType: "stack_instance", IDParam: "id", ResultFields: map[string]string{"log_id": "log_id"}},
	"POST /stack-instances/:id/stop":          {Action: AuditActionStop, EntityType: "stack_instance", IDParam: "id", ResultFields: map[string]string{"log_id": "log_id"}},
	"POST /stack-instances/:id/clean":         {Action: AuditActionClean, EntityType: "stack_instance", IDParam: "id", ResultFields: map[string]string{"log_id": "log_id"}},
	"POST /stack-instances/:id/rollback":      {Action: AuditActionRollback, EntityType: "stack_instance", IDParam: "id", ResultFields: map[string]string{"log_id": "log_id"}},
	"POST /stack-instances/:id/extend":        {Action: AuditActionExtendTTL, EntityType: "stack_instance", IDParam: "id"},
	"POST /stack-instances/:id/actions/:name": {Action: AuditActionInvokeAction, EntityType: "stack_instance", IDParam: "id", DetailParams: map[string]string{"name": "action"}},
	"POST /stack-instances/bulk/deploy":       {Action: AuditActionDeploy, EntityType: "stack_instance", BulkIDField: "instance_id"},
	"POST /stack-instances/bulk/stop":         {Action: AuditActionStop, EntityType: "stack_instance", BulkIDField: "instance_id"},
	"POST /stack-instances/bulk/clean":        {Action: AuditActionClean, EntityType: "stack_instance", BulkIDField: "instance_id"},
	"POST /stack-instances/bulk/delete":       {Action: AuditActionDelete, EntityType: "stack_instance", BulkIDField: "instance_id"},

	// Users and API keys.
	"PUT /users/:id/disable":                       {Action: AuditActionDisable, EntityType: "user", IDParam: "id"},
	"PUT /users/:id/enable":                        {Action: AuditActionEnable, EntityType: "user", IDParam: "id"},
	"PUT /users/:id/password":                      {Action: AuditActionResetPassword, EntityType: "user", IDParam: "id"},
	"PUT /users/:id/role":                          {Action: AuditActionChangeRole, EntityType: "user", IDParam: "id", ResultFields: map[string]string{"old_role": "old_role", "new_role": "new_role"}},
	"POST /users/:id/api-keys":                     {Action: AuditActionCreate, EntityType: "api_key", DetailParams: map[string]string{"id": "user_id"}},
	"DELETE /users/:id/api-keys/:keyId":            {Action: AuditActionDelete, EntityType: "api_key", IDParam: "keyId", DetailParams: map[string]string{"id": "user_id"}},
	"DELETE /admin/orphaned-namespaces/:namespace": {Action: AuditActionDelete, EntityType: "namespace", IDParam: "namespace"},

	// Cleanup policies, notification channels, clusters.
	"POST /admin/cleanup-policies/:id/run":       {Action: AuditActionRun, EntityType: "cleanup_policy", IDParam: "id", DetailQuery: []string{"dry_run"}},
	"POST /admin/notification-channels/:id/test": {Action: AuditActionTest, EntityType: "notification_channel", IDParam: "id"},
	"POST /clusters/:id/test":                    {Action: AuditActionTestConnection, EntityType: "cluster", IDParam: "id"},
	"POST /clusters/:id/default":                 {Action: AuditActionSetDefault, EntityType: "cluster", IDParam: "id"},

	// Notifications: read state is not a change worth auditing.
	"POST /notifications/:id/read": {Skip: true},
	"POST /notifications/read-all": {Skip: true},

	// Favorites.
	"DELETE /favorites/:entityType/:entityId": {Action: AuditActionDelete, EntityType: "favorite", IDParam: "entityId", DetailParams: map[string]string{"entityType": "entity_type"}},
}

// AuditRouteFor returns how the audit middleware records a request with this
// method and route pattern (gin FullPath). Routes in the route table get
// their entry from it. Other routes are plain CRUD: the action comes from the
// method and the entity type from the last resource segment of the route; the
// entity ID is the "id" or "chartId" path parameter, or the "id" field of the
// response for a create.
func AuditRouteFor(method, fullPath string) AuditRoute {
	key := method + " " + strings.TrimPrefix(fullPath, auditAPIPrefix)
	if r, ok := auditRoutes[key]; ok {
		return r
	}
	r := AuditRoute{EntityType: extractEntityType(fullPath), CRUD: true}
	switch method {
	case http.MethodPost:
		r.Action = AuditActionCreate
	case http.MethodPut:
		r.Action = AuditActionUpdate
	case http.MethodDelete:
		r.Action = AuditActionDelete
	}
	return r
}

// buildAuditEntries builds the audit entries of a successful request. body is
// the response body.
func buildAuditEntries(c *gin.Context, route AuditRoute, body []byte) []*models.AuditLog {
	userID := GetUserIDFromContext(c)
	username := GetUsernameFromContext(c)
	now := time.Now().UTC()
	newEntry := func(entityID string, details map[string]any) *models.AuditLog {
		entry := &models.AuditLog{
			ID:         uuid.New().String(),
			UserID:     userID,
			Username:   username,
			Action:     route.Action,
			EntityType: route.EntityType,
			EntityID:   truncateEntityID(entityID),
			Timestamp:  now,
		}
		if len(details) > 0 {
			if raw, err := json.Marshal(details); err == nil {
				entry.Details = string(raw)
			}
		}
		return entry
	}

	var resp map[string]any
	_ = json.Unmarshal(body, &resp) // not JSON or not an object: resp stays nil

	if route.BulkIDField != "" {
		return bulkAuditEntries(route, resp, newEntry)
	}

	details := map[string]any{}
	for param, key := range route.DetailParams {
		if v := c.Param(param); v != "" {
			details[key] = v
		}
	}
	for _, q := range route.DetailQuery {
		if v, ok := c.GetQuery(q); ok {
			details[q] = v
		}
	}
	for field, key := range route.ResultFields {
		if v, ok := resp[field]; ok && v != nil && v != "" {
			details[key] = v
		}
	}

	var entityID string
	switch {
	case route.CRUD:
		entityID = c.Param("id")
		if entityID == "" {
			entityID = c.Param("chartId")
		}
		if entityID == "" && c.Request.Method == http.MethodPost {
			entityID = responseID(resp)
		}
		addChildIDs(c, entityID, resp, details)
	case route.IDParam != "":
		entityID = c.Param(route.IDParam)
	default:
		// Table route without an ID parameter: the ID of the response.
		entityID = responseID(resp)
	}
	return []*models.AuditLog{newEntry(entityID, details)}
}

// bulkAuditEntries returns one entry per item of the bulk response with
// status "success", so that a filter on the entity ID finds the operation.
// A response that cannot be read gives one entry without an entity ID.
func bulkAuditEntries(route AuditRoute, resp map[string]any, newEntry func(string, map[string]any) *models.AuditLog) []*models.AuditLog {
	results, ok := resp["results"].([]any)
	if !ok {
		return []*models.AuditLog{newEntry("", map[string]any{"bulk": true})}
	}
	var entries []*models.AuditLog
	for _, item := range results {
		m, ok := item.(map[string]any)
		if !ok || m["status"] != "success" {
			continue
		}
		id, _ := m[route.BulkIDField].(string)
		if id == "" {
			continue
		}
		details := map[string]any{"bulk": true}
		if logID, ok := m["log_id"].(string); ok && logID != "" {
			details["log_id"] = logID
		}
		entries = append(entries, newEntry(id, details))
	}
	return entries
}

// addChildIDs adds the IDs of a nested child resource to the details of a
// plain CRUD entry whose entity ID is the parent ID (for example
// PUT /clusters/:id/shared-values/:valueId keeps the cluster ID as entity ID):
// every other path parameter (chartId gives "chart_id", valueId gives
// "value_id"), and for a create the "id" of the response as "created_id".
func addChildIDs(c *gin.Context, entityID string, resp map[string]any, details map[string]any) {
	if c.Param("id") == "" || entityID != c.Param("id") {
		return
	}
	for _, p := range c.Params {
		if p.Key == "id" || p.Value == "" {
			continue
		}
		details[snakeCase(p.Key)] = p.Value
	}
	if c.Request.Method == http.MethodPost {
		if created := responseID(resp); created != "" && created != entityID {
			details["created_id"] = created
		}
	}
}

// snakeCase turns a camelCase path parameter name into snake_case.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// truncateEntityID cuts an entity ID to models.MaxAuditEntityIDLen bytes (at
// a UTF-8 rune start), so the insert never fails on the column size. IDs are
// UUIDs or namespace names (at most 63 characters); a longer value is not a
// valid ID, and the cut keeps the entry.
func truncateEntityID(id string) string {
	if len(id) <= models.MaxAuditEntityIDLen {
		return id
	}
	cut := models.MaxAuditEntityIDLen
	for cut > 0 && !utf8.RuneStart(id[cut]) {
		cut--
	}
	return id[:cut]
}

// responseID returns the "id" field of a JSON response object as a string.
func responseID(resp map[string]any) string {
	id, ok := resp["id"]
	if !ok || id == nil {
		return ""
	}
	return fmt.Sprintf("%v", id)
}

// extractEntityType derives the entity type from the route path.
// e.g. "/api/v1/stack-definitions/:id/charts/:chartId" → "chart_config"
func extractEntityType(fullPath string) string {
	parts := strings.Split(strings.Trim(fullPath, "/"), "/")

	// Walk backwards to find the last meaningful resource segment.
	for i := len(parts) - 1; i >= 0; i-- {
		seg := parts[i]
		if strings.HasPrefix(seg, ":") {
			continue
		}
		return normalizeEntityType(seg)
	}
	return "unknown"
}

// entityTypeBySegment maps route segments to singular entity type names.
var entityTypeBySegment = map[string]string{
	"templates":             "stack_template",
	"stack-definitions":     "stack_definition",
	"stack-instances":       "stack_instance",
	"charts":                "chart_config",
	"overrides":             "value_override",
	"branches":              "branch_override",
	"quota-overrides":       "quota_override",
	"auth":                  "user",
	"register":              "user",
	"users":                 "user",
	"audit-logs":            "audit_log",
	"api-keys":              "api_key",
	"clusters":              "cluster",
	"quotas":                "quota",
	"shared-values":         "shared_values",
	"cleanup-policies":      "cleanup_policy",
	"notification-channels": "notification_channel",
	"subscriptions":         "notification_subscription",
	"preferences":           "notification_preference",
	"favorites":             "favorite",
	"orphaned-namespaces":   "namespace",
}

func normalizeEntityType(segment string) string {
	if v, ok := entityTypeBySegment[segment]; ok {
		return v
	}
	return strings.ReplaceAll(segment, "-", "_")
}
