package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/hooks"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// offsetPattern accepts a non-negative decimal integer that fits in int64.
var offsetPattern = regexp.MustCompile(`^[0-9]{1,18}$`)

// Fixed messages of a stored action run without a subscriber message. They
// never hold a URL or the error detail.
const (
	actionRunMsgUnreachable  = "Subscriber unreachable"
	actionRunMsgNonJSON      = "Subscriber returned a non-JSON body"
	actionRunMsgTooLarge     = "Subscriber returned a body that is too large"
	actionRunMsgClientClosed = "The client closed the request before the answer"
)

const (
	// actionRunWriteTimeout bounds one action run write, so a slow
	// database delays the action response by at most this time.
	actionRunWriteTimeout = 2 * time.Second
	// maxActionRunMessageLen is the size of action_runs.message in
	// characters.
	maxActionRunMessageLen = 500
)

// actionParameterInfo is the public part of an action parameter.
type actionParameterInfo struct {
	Default     any      `json:"default,omitempty"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type" enums:"string,bool,enum"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required"`
	// Sensitive is true when the run history stores "[redacted]" instead of
	// the value; the web UI uses a password field.
	Sensitive bool `json:"sensitive,omitempty"`
}

// actionInfo is the public part of a registered action. It never holds the
// subscriber URL, the secret or headers.
type actionInfo struct {
	Name        string                `json:"name"`
	Label       string                `json:"label"`
	Description string                `json:"description,omitempty"`
	Confirm     string                `json:"confirm,omitempty"`
	Parameters  []actionParameterInfo `json:"parameters"`
	// HasJobLog is true when the backend proxies the job log of the action
	// (the action config has a log_path).
	HasJobLog bool `json:"has_job_log"`
	// CanInvoke is true when the caller may run the action on this instance.
	CanInvoke bool `json:"can_invoke"`
}

// actionListResponse is the response of ListActions.
type actionListResponse struct {
	InstanceID string       `json:"instance_id"`
	Actions    []actionInfo `json:"actions"`
	CanInvoke  bool         `json:"can_invoke"`
}

// actionJobLogResponse is the response of GetActionJobLog.
type actionJobLogResponse struct {
	Action     string `json:"action"`
	InstanceID string `json:"instance_id"`
	JobID      string `json:"job_id"`
	// Status is the job state from the subscriber: running while the job
	// runs, else a final state such as succeeded or failed.
	Status string `json:"status"`
	// Log is the log text from Offset to NextOffset.
	Log string `json:"log"`
	// Offset is the requested byte offset.
	Offset int64 `json:"offset"`
	// NextOffset is the offset for the next poll.
	NextOffset int64 `json:"next_offset"`
	// Done is true when the job has ended and the log is read to the end.
	Done bool `json:"done"`
	// Truncated is true when the chunk was cut at the size cap; poll again
	// at once with next_offset.
	Truncated bool `json:"truncated"`
}

// toActionInfo copies only the public fields of an action subscription.
func toActionInfo(a hooks.ActionSubscription, canInvoke bool) actionInfo {
	label := a.Label
	if label == "" {
		label = a.Name
	}
	params := make([]actionParameterInfo, 0, len(a.Parameters))
	for _, p := range a.Parameters {
		pl := p.Label
		if pl == "" {
			pl = p.Name
		}
		params = append(params, actionParameterInfo{
			Default:     p.Default,
			Name:        p.Name,
			Label:       pl,
			Description: p.Description,
			Type:        p.Type,
			Options:     p.Options,
			Required:    p.Required,
			Sensitive:   p.Sensitive,
		})
	}
	return actionInfo{
		Name:        a.Name,
		Label:       label,
		Description: a.Description,
		Confirm:     a.Confirm,
		Parameters:  params,
		HasJobLog:   a.HasJobLog(),
		CanInvoke:   canInvoke,
	}
}

// asyncJobID returns result.job_id when the action has a job log, the
// subscriber answered 2xx, and the result is a JSON object with a valid
// job_id string.
func asyncJobID(sub hooks.ActionSubscription, statusCode int, result json.RawMessage) string {
	if !sub.HasJobLog() || statusCode < 200 || statusCode >= 300 {
		return ""
	}
	var body struct {
		JobID any `json:"job_id"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return ""
	}
	jobID, ok := body.JobID.(string)
	if !ok || !hooks.ValidJobID(jobID) {
		return ""
	}
	return jobID
}

// ListActions godoc
// @Summary     List the actions that can run on a stack instance
// @Description Returns the registered actions with their UI metadata (label, description, confirmation text, parameter schema) and a can_invoke flag. Any authenticated user who can view the instance gets the list; can_invoke is true for the owner, an admin and a devops user. The response never contains the subscriber URL, secret or headers. Without an action registry the list is empty.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id  path     string  true  "Instance ID"
// @Success     200 {object} actionListResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "Instance not found"
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/actions [get]
func (h *InstanceHandler) ListActions(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instance id is required"})
		return
	}

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	canInvoke := canModifyInstance(c, inst)
	resp := actionListResponse{InstanceID: id, CanInvoke: canInvoke, Actions: []actionInfo{}}
	if h.actions != nil {
		for _, a := range h.actions.List() {
			resp.Actions = append(resp.Actions, toActionInfo(a, canInvoke))
		}
	}
	c.JSON(http.StatusOK, resp)
}

// GetActionJobLog godoc
// @Summary     Read the job log of an asynchronous action
// @Description Fetches the job log of an asynchronous action from the action subscriber, from the byte offset to the end (at most 256 KiB per call). The action config must have a log_path. Poll with next_offset until done is true. The first poll that returns done stores the final job status and the finish time on the action run of the job (GET /stack-instances/{id}/action-runs); later polls change nothing. Only the instance owner, an admin or a devops user may read the job log (the same rule as invoke). The backend signs the subscriber request like an invoke call and never returns the subscriber URL.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id     path     string true  "Instance ID"
// @Param       name   path     string true  "Action name"
// @Param       job_id path     string true  "Job ID (1-128 characters: A-Z a-z 0-9 . _ -, not starting with a dot)"
// @Param       offset query    int    false "Byte offset to read from (default 0)"
// @Success     200 {object} actionJobLogResponse
// @Failure     400 {object} map[string]string "Invalid job_id or offset"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string "Instance, action, job log route or job not found"
// @Failure     500 {object} map[string]string
// @Failure     502 {object} map[string]string "Subscriber unreachable or returned an error"
// @Failure     503 {object} map[string]string "Action registry not configured"
// @Router      /api/v1/stack-instances/{id}/actions/{name}/jobs/{job_id}/log [get]
func (h *InstanceHandler) GetActionJobLog(c *gin.Context) {
	id := c.Param("id")
	name := c.Param("name")
	jobID := c.Param("job_id")
	if id == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instance id and action name are required"})
		return
	}
	if !hooks.ValidJobID(jobID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job_id"})
		return
	}
	var offset int64
	if raw, present := c.GetQuery("offset"); present {
		if !offsetPattern.MatchString(raw) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
			return
		}
		parsed, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
			return
		}
		offset = parsed
	}
	if h.actions == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "action registry not configured"})
		return
	}

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: the same rule as invoke. A job log can hold sensitive
	// output (database names, hosts, error text), so viewers may not read it.
	if !requireInstanceModify(c, inst) {
		return
	}

	jl, fetchErr := h.actions.FetchJobLog(c.Request.Context(), name, jobID, offset, instanceRefFor(inst))
	if fetchErr != nil {
		var unk hooks.ErrUnknownAction
		switch {
		case errors.As(fetchErr, &unk):
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown action"})
		case errors.Is(fetchErr, hooks.ErrNoJobLog):
			c.JSON(http.StatusNotFound, gin.H{"error": "action has no job log"})
		case errors.Is(fetchErr, hooks.ErrInvalidJobID):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job_id"})
		case errors.Is(fetchErr, hooks.ErrInvalidOffset):
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
		case errors.Is(fetchErr, hooks.ErrJobNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "job log not found"})
		default:
			// The detailed error can hold the subscriber URL: log it only.
			slog.Error("action job log fetch failed",
				logKeyInstanceID, id,
				"action", name,
				"job_id", jobID,
				"hook_request_id", jl.RequestID,
				"error", fetchErr,
			)
			c.JSON(http.StatusBadGateway, gin.H{
				"error":      "action subscriber unreachable or returned an error",
				"action":     name,
				"request_id": jl.RequestID,
			})
		}
		return
	}

	if jl.Done {
		h.storeJobFinalStatus(c, inst.ID, name, jobID, jl.Status)
	}

	c.JSON(http.StatusOK, actionJobLogResponse{
		Action:     name,
		InstanceID: id,
		JobID:      jobID,
		Status:     jl.Status,
		Log:        strings.ToValidUTF8(string(jl.Log), "�"),
		Offset:     offset,
		NextOffset: jl.NextOffset,
		Done:       jl.Done,
		Truncated:  jl.Truncated,
	})
}

// actionRunInfo is one row of the action run list.
type actionRunInfo struct {
	StartedAt time.Time `json:"started_at"`
	// FinishedAt is the answer time of a run without a job, or the time the
	// job log proxy saw the final job status.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Parameters holds the declared parameters only; a sensitive value is
	// "[redacted]".
	Parameters map[string]any `json:"parameters"`
	ID         string         `json:"id"`
	InstanceID string         `json:"instance_id"`
	Action     string         `json:"action"`
	// ActionLabel is the current label of the action, else its name.
	ActionLabel string `json:"action_label"`
	UserID      string `json:"user_id"`
	// StartedBy is the username of user_id; omitted when the user is gone.
	StartedBy string `json:"started_by,omitempty"`
	Message   string `json:"message,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	JobStatus string `json:"job_status,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	// StatusCode is the HTTP status of the subscriber; 0 means no answer.
	StatusCode int `json:"status_code"`
	// HasJobLog is true when the run has a job and the action still has a
	// log_path.
	HasJobLog bool `json:"has_job_log"`
}

// actionRunListResponse is the response of ListActionRuns.
type actionRunListResponse struct {
	InstanceID string `json:"instance_id"`
	// NextCursor loads the next page; omitted on the last page.
	NextCursor string          `json:"next_cursor,omitempty"`
	Data       []actionRunInfo `json:"data"`
	// CanViewLogs is true when the caller may read job logs (owner, admin,
	// devops).
	CanViewLogs bool `json:"can_view_logs"`
}

// newActionRun builds the run of an invoke. It keeps only the declared
// parameters (hooks.ActionSubscription.RecordedParameters).
func newActionRun(c *gin.Context, instanceID, name string, sub hooks.ActionSubscription, params map[string]any, startedAt time.Time) *models.ActionRun {
	recorded, err := json.Marshal(sub.RecordedParameters(params))
	if err != nil {
		recorded = []byte("{}")
	}
	return &models.ActionRun{
		StartedAt:  startedAt,
		InstanceID: instanceID,
		Action:     name,
		UserID:     middleware.GetUserIDFromContext(c),
		Parameters: string(recorded),
	}
}

// actionRunFailureMessage returns the stored message of an invoke error.
func actionRunFailureMessage(err error) string {
	switch {
	case errors.Is(err, hooks.ErrActionNonJSONBody):
		return actionRunMsgNonJSON
	case errors.Is(err, hooks.ErrActionBodyTooLarge):
		return actionRunMsgTooLarge
	default:
		return actionRunMsgUnreachable
	}
}

// actionRunContext returns the context of an action run write: it ignores a
// client disconnect and ends after actionRunWriteTimeout.
func actionRunContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(c.Request.Context()), actionRunWriteTimeout)
}

// recordActionRun stores run before the invoke response, so a list refresh
// sees it. It returns the run ID, or "" without a repository or after a
// failed write: the action never fails because of the history.
func (h *InstanceHandler) recordActionRun(c *gin.Context, run *models.ActionRun) string {
	if h.actionRuns == nil {
		return ""
	}
	// ActionResultMessage cuts at 500 bytes plus an ellipsis, which can
	// split a character; the column holds 500 characters.
	run.Message = truncateRunes(strings.ToValidUTF8(run.Message, ""), maxActionRunMessageLen)
	ctx, cancel := actionRunContext(c)
	defer cancel()
	if err := h.actionRuns.Create(ctx, run); err != nil {
		// Never log the parameters.
		slog.Warn("action run record failed", logKeyInstanceID, run.InstanceID, "action", run.Action, "error", err)
		return ""
	}
	return run.ID
}

// storeJobFinalStatus stores the final status of a job on its action run.
// A failure is logged; the job log response stays the same.
func (h *InstanceHandler) storeJobFinalStatus(c *gin.Context, instanceID, action, jobID, status string) {
	if h.actionRuns == nil || status == "" {
		return
	}
	ctx, cancel := actionRunContext(c)
	defer cancel()
	if _, err := h.actionRuns.SetJobFinalStatus(ctx, instanceID, action, jobID, status, time.Now().UTC()); err != nil {
		slog.Warn("action run record failed", logKeyInstanceID, instanceID, "action", action, "job_id", jobID, "error", err)
	}
}

// truncateRunes returns s cut to at most n characters.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// decodeRecordedParameters reads the stored parameters; an empty or bad
// value gives an empty object.
func decodeRecordedParameters(raw string) map[string]any {
	out := map[string]any{}
	if raw == "" || json.Unmarshal([]byte(raw), &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// ListActionRuns godoc
// @Summary     List the action runs of a stack instance
// @Description Returns the stored custom action runs of the instance, newest first, with cursor paging (no total). Any authenticated user who can view the instance gets the list (the rule of the action list). parameters holds the declared parameters only; a sensitive parameter shows [redacted]. status_code 0 means that no answer came (the subscriber was unreachable, or the client closed the request). message is the refusal reason of the subscriber (a sensitive value sent with the call shows [redacted]) or a fixed text. started_by is omitted when the user no longer exists. action_label is the current label of the action, else its name. has_job_log is true when the run has a job_id and the action still has a log_path. can_view_logs is true for the owner, an admin and a devops user: only they may read a job log. Without a repository the list is empty.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id     path     string true  "Instance ID"
// @Param       limit  query    int    false "Rows per page (default 20; a value above 100 counts as 100)"
// @Param       cursor query    string false "Opaque cursor from next_cursor of the previous page"
// @Success     200 {object} actionRunListResponse
// @Failure     400 {object} map[string]string "Invalid limit or cursor parameter"
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string "Instance not found"
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/action-runs [get]
func (h *InstanceHandler) ListActionRuns(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}
	limit := models.DefaultActionRunPageSize
	if raw := c.Query("limit"); raw != "" {
		l, err := strconv.Atoi(raw)
		if err != nil || l < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit parameter"})
			return
		}
		limit = min(l, models.MaxActionRunPageSize)
	}
	cursor := c.Query("cursor")

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	resp := actionRunListResponse{InstanceID: inst.ID, CanViewLogs: canModifyInstance(c, inst), Data: []actionRunInfo{}}
	if h.actionRuns == nil {
		c.JSON(http.StatusOK, resp)
		return
	}

	runs, next, err := h.actionRuns.ListByInstance(c.Request.Context(), inst.ID, cursor, limit)
	if err != nil {
		if errors.Is(err, dberrors.ErrValidation) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid cursor parameter"})
			return
		}
		slog.Error("action run list failed", logKeyInstanceID, inst.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	userIDs := make([]string, 0, len(runs))
	for _, r := range runs {
		userIDs = append(userIDs, r.UserID)
	}
	usernames := lookupUsernames(c.Request.Context(), h.userRepo, userIDs)

	resp.NextCursor = next
	for _, r := range runs {
		resp.Data = append(resp.Data, h.toActionRunInfo(r, usernames[r.UserID]))
	}
	c.JSON(http.StatusOK, resp)
}

// toActionRunInfo builds a list row. The label and has_job_log come from the
// current action registry.
func (h *InstanceHandler) toActionRunInfo(r models.ActionRun, startedBy string) actionRunInfo {
	info := actionRunInfo{
		StartedAt:   r.StartedAt.UTC(),
		Parameters:  decodeRecordedParameters(r.Parameters),
		ID:          r.ID,
		InstanceID:  r.InstanceID,
		Action:      r.Action,
		ActionLabel: r.Action,
		UserID:      r.UserID,
		StartedBy:   startedBy,
		Message:     r.Message,
		JobID:       r.JobID,
		JobStatus:   r.JobStatus,
		RequestID:   r.RequestID,
		StatusCode:  r.StatusCode,
	}
	if r.FinishedAt != nil {
		finished := r.FinishedAt.UTC()
		info.FinishedAt = &finished
	}
	if h.actions != nil {
		if sub, ok := h.actions.Lookup(r.Action); ok {
			if sub.Label != "" {
				info.ActionLabel = sub.Label
			}
			info.HasJobLog = r.JobID != "" && sub.HasJobLog()
		}
	}
	return info
}
