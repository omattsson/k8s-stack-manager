package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"backend/internal/hooks"

	"github.com/gin-gonic/gin"
)

// offsetPattern accepts a non-negative decimal integer that fits in int64.
var offsetPattern = regexp.MustCompile(`^[0-9]{1,18}$`)

// actionParameterInfo is the public part of an action parameter.
type actionParameterInfo struct {
	Default     any      `json:"default,omitempty"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type" enums:"string,bool,enum"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required"`
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

	inst, err := h.instanceRepo.FindByID(id)
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
// @Description Fetches the job log of an asynchronous action from the action subscriber, from the byte offset to the end (at most 256 KiB per call). The action config must have a log_path. Poll with next_offset until done is true. Only the instance owner, an admin or a devops user may read the job log (the same rule as invoke). The backend signs the subscriber request like an invoke call and never returns the subscriber URL.
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

	inst, err := h.instanceRepo.FindByID(id)
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
