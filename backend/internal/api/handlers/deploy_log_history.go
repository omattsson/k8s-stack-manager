package handlers

import (
	"context"
	"strings"

	"backend/internal/models"
)

// Views of GET /stack-instances/:id/deploy-log.
const (
	// deployLogViewFull is the default view: all fields of each log.
	deployLogViewFull = "full"
	// deployLogViewSummary returns the row columns only and the highlights.
	deployLogViewSummary = "summary"
)

// Error messages of the deploy log list parameters.
const (
	msgInvalidDeployLogView   = "Invalid view parameter"
	msgInvalidDeployLogAction = "Invalid action parameter"
	msgInvalidDeployLogStatus = "Invalid status parameter"
)

// parseDeployLogList splits a comma list, trims the spaces and drops the
// empty items. ok is false when valid rejects an item.
func parseDeployLogList(raw string, valid func(string) bool) (values []string, ok bool) {
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !valid(item) {
			return nil, false
		}
		values = append(values, item)
	}
	return values, true
}

// summaryDeployLog clears the large columns, so a summary row has only the
// row columns even when the repository loaded more.
func summaryDeployLog(log *models.DeploymentLog) {
	log.Output = ""
	log.ValuesSnapshot = ""
	log.ChartVersions = ""
}

// setDeployLogStartedBy sets StartedBy of each log from its UserID with one
// batch user lookup. A user that is not found keeps StartedBy empty.
func setDeployLogStartedBy(ctx context.Context, userRepo models.UserRepository, logs []*models.DeploymentLog) {
	ids := make([]string, 0, len(logs))
	for _, l := range logs {
		if l != nil {
			ids = append(ids, l.UserID)
		}
	}
	names := lookupUsernames(ctx, userRepo, ids)
	for _, l := range logs {
		if l != nil {
			l.StartedBy = names[l.UserID]
		}
	}
}

// deployLogResultRows returns pointers to the rows and the highlights of
// the result, for in-place changes.
func deployLogResultRows(result *models.DeploymentLogResult) []*models.DeploymentLog {
	rows := make([]*models.DeploymentLog, 0, len(result.Data)+3)
	for i := range result.Data {
		rows = append(rows, &result.Data[i])
	}
	if hl := result.Highlights; hl != nil {
		rows = append(rows, hl.Running, hl.Current, hl.LatestFailure)
	}
	return rows
}
