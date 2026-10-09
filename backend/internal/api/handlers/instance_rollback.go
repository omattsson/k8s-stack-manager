package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"backend/internal/deployer"
	"backend/internal/models"

	"gopkg.in/yaml.v3"
)

// msgValuesDrift tells the user that the next deploy undoes a rollback.
const msgValuesDrift = "The stored overrides differ from the running values. The next deploy applies the stored overrides again."

// RollbackResponse is the response of the rollback endpoint.
type RollbackResponse struct {
	LogID   string `json:"log_id"`
	Message string `json:"message"`
	// TargetLogID is the deploy whose values the rollback restores. Empty
	// for a rollback by one Helm revision.
	TargetLogID string `json:"target_log_id,omitempty"`
	// ValuesDrift is set for a rollback to a target: true when the values
	// built from the stored overrides differ from the target values. The
	// rollback does not change the stored overrides, so the next deploy then
	// changes the running values again. Not set for a rollback by one
	// revision; use GET /deploy-preview after the rollback.
	ValuesDrift *bool `json:"values_drift,omitempty"`
	// Warning explains ValuesDrift when it is true.
	Warning string `json:"warning,omitempty"`
}

// rollbackTargetError is a client error found while loading a rollback target.
type rollbackTargetError struct {
	status  int
	message string
}

func (e *rollbackTargetError) Error() string { return e.message }

// rollbackTargetData is the data of a rollback target deploy log.
type rollbackTargetData struct {
	values   map[string]string // chart name -> merged values YAML
	versions map[string]string // chart name -> chart version
	branch   string
}

// loadRollbackTarget returns the values snapshot (chart name -> values YAML),
// the recorded chart versions and the branch of the deploy log targetLogID. The log must
// be a successful deploy of instanceID with a values snapshot. Client errors
// are returned as *rollbackTargetError (404 for a missing log or a log of
// another instance, 400 for a log that is not a successful deploy).
func (h *InstanceHandler) loadRollbackTarget(ctx context.Context, instanceID, targetLogID string) (*rollbackTargetData, error) {
	if h.deployLogRepo == nil {
		return nil, &rollbackTargetError{status: http.StatusServiceUnavailable, message: "Deployment log service not configured"}
	}
	logEntry, err := h.deployLogRepo.FindByID(ctx, targetLogID)
	if err != nil {
		status, message := mapError(err, "Rollback target deployment log")
		if status == http.StatusInternalServerError {
			return nil, err
		}
		return nil, &rollbackTargetError{status: status, message: message}
	}
	if logEntry.StackInstanceID != instanceID {
		return nil, &rollbackTargetError{status: http.StatusNotFound, message: "Rollback target deployment log not found for this instance"}
	}
	if logEntry.Action != models.DeployActionDeploy || logEntry.Status != models.DeployLogSuccess {
		return nil, &rollbackTargetError{status: http.StatusBadRequest, message: "Rollback target must be a successful deploy of this instance"}
	}
	values := map[string]string{}
	if logEntry.ValuesSnapshot == "" || json.Unmarshal([]byte(logEntry.ValuesSnapshot), &values) != nil || len(values) == 0 {
		return nil, &rollbackTargetError{status: http.StatusBadRequest, message: "Rollback target has no values snapshot"}
	}
	return &rollbackTargetData{
		values:   values,
		versions: deployer.ParseChartVersions(logEntry.ChartVersions),
		branch:   logEntry.Branch,
	}, nil
}

// valuesEqual reports whether two values YAML documents have the same
// content. Key order and formatting do not matter. An empty document equals
// an empty map. When a document is not valid YAML, the trimmed strings are
// compared.
func valuesEqual(a, b string) bool {
	if strings.TrimSpace(a) == strings.TrimSpace(b) {
		return true
	}
	var pa, pb any
	if yaml.Unmarshal([]byte(a), &pa) != nil || yaml.Unmarshal([]byte(b), &pb) != nil {
		return false
	}
	return reflect.DeepEqual(normalizeEmpty(pa), normalizeEmpty(pb))
}

// normalizeEmpty maps a nil document to an empty map.
func normalizeEmpty(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

// lastOperationWasRollback reports whether the latest deploy or rollback log
// of the instance (stop and clean logs are ignored) is a successful rollback,
// so the running values came from a rollback and not from the stored
// overrides.
func (h *InstanceHandler) lastOperationWasRollback(ctx context.Context, instanceID string) bool {
	if h.deployLogRepo == nil {
		return false
	}
	logs, err := h.deployLogRepo.ListLatestByActions(ctx, instanceID,
		[]string{models.DeployActionDeploy, models.DeployActionRollback}, 1)
	if err != nil || len(logs) == 0 {
		return false
	}
	return logs[0].Action == models.DeployActionRollback && logs[0].Status == models.DeployLogSuccess
}

// targetValuesDrift compares the values built from the stored overrides with
// the target values, for the charts of the target. It returns true when at
// least one chart differs.
func targetValuesDrift(pending map[string]string, target map[string]string) bool {
	for chart, values := range target {
		current, ok := pending[chart]
		if !ok {
			continue
		}
		if !valuesEqual(current, values) {
			return true
		}
	}
	return false
}

// logRollbackDriftError logs a failure to build the pending values for the
// drift check. The rollback itself continues.
func logRollbackDriftError(instanceID string, err error) {
	slog.Warn("rollback: failed to build pending values for the drift check",
		logKeyInstanceID, instanceID, "error", err)
}

// instanceValuesDrift reports whether the running values of inst come from a
// successful rollback and the stored overrides produce different values. It
// returns false when the last operation was not a rollback or when a lookup
// fails (logged).
func (h *InstanceHandler) instanceValuesDrift(ctx context.Context, inst *models.StackInstance) bool {
	if inst.LastDeployedValues == "" || !h.lastOperationWasRollback(ctx, inst.ID) {
		return false
	}
	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		slog.Warn("values drift: definition lookup failed", logKeyInstanceID, inst.ID, "error", err)
		return false
	}
	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		slog.Warn("values drift: chart lookup failed", logKeyInstanceID, inst.ID, "error", err)
		return false
	}
	pending, err := h.buildChartValues(ctx, inst, def, charts)
	if err != nil {
		logRollbackDriftError(inst.ID, err)
		return false
	}
	running := map[string]string{}
	if err := json.Unmarshal([]byte(inst.LastDeployedValues), &running); err != nil {
		return false
	}
	for _, ch := range charts {
		if !valuesEqual(pending[ch.ChartName], running[ch.ChartName]) {
			return true
		}
	}
	return false
}
