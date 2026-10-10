package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/database"
	"backend/internal/hooks"
	"backend/internal/models"
)

// CleanupExecutor implements scheduler.ActionExecutor by delegating to the
// deploy Manager. It resolves definitions and charts for each instance.
type CleanupExecutor struct {
	manager         *Manager
	definitionRepo  models.StackDefinitionRepository
	chartConfigRepo models.ChartConfigRepository
	instanceRepo    models.StackInstanceRepository
}

// NewCleanupExecutor creates a CleanupExecutor.
func NewCleanupExecutor(
	m *Manager,
	defRepo models.StackDefinitionRepository,
	ccRepo models.ChartConfigRepository,
	instRepo models.StackInstanceRepository,
) *CleanupExecutor {
	return &CleanupExecutor{
		manager:         m,
		definitionRepo:  defRepo,
		chartConfigRepo: ccRepo,
		instanceRepo:    instRepo,
	}
}

// StopInstance resolves charts and initiates an async Helm uninstall.
func (e *CleanupExecutor) StopInstance(ctx context.Context, inst *models.StackInstance) error {
	charts, err := e.resolveCharts(inst)
	if err != nil {
		return err
	}
	var chartInfos []ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, ChartDeployInfo{ChartConfig: ch})
	}
	_, err = e.manager.StopWithCharts(ctx, inst, chartInfos)
	return err
}

// CleanInstance resolves charts and initiates Helm uninstall + namespace deletion.
func (e *CleanupExecutor) CleanInstance(ctx context.Context, inst *models.StackInstance) error {
	charts, err := e.resolveCharts(inst)
	if err != nil {
		return err
	}
	_, err = e.manager.Clean(ctx, inst, charts)
	return err
}

// DeleteInstance deletes the instance from the database.
// It refuses deletion while the instance is running or in the middle of an async
// stop/clean operation, because those workflows need to read/update the record.
// Callers should ensure the instance is stopped/cleaned before requesting deletion.
func (e *CleanupExecutor) DeleteInstance(ctx context.Context, inst *models.StackInstance) error {
	// Do not start a delete when the caller stopped (for example the cleanup
	// scheduler at the end of a leadership term).
	if err := ctx.Err(); err != nil {
		return err
	}
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusDeploying, models.StackStatusStabilizing,
		models.StackStatusStopping, models.StackStatusCleaning:
		return fmt.Errorf("cannot delete instance %s while status is %s; stop/clean must complete first", inst.ID, inst.Status)
	}
	// pre-instance-delete can stop the delete, as for the API delete. The
	// error text is user-safe (never the subscriber URL); the policy run
	// records it as the error of this instance.
	if e.manager != nil {
		if hookErr := e.manager.fireDeployHook(ctx, hooks.EventPreInstanceDelete, inst, "", time.Time{}, e.triggerOpts(ctx)); hookErr != nil {
			slog.Warn("pre-instance-delete hook stopped a cleanup delete", "instance_id", inst.ID, "error", hookErr)
			return errors.New(hooks.UserMessage(hookErr, hooks.EventPreInstanceDelete, "delete"))
		}
	}
	// The delete removes the follower rows: read the followers first, so
	// that they get the "instance.deleted" notification.
	var followerIDs []string
	if e.manager != nil {
		followerIDs = e.manager.followerIDs(inst.ID)
	}
	// With a transaction runner, delete the branch overrides and the quick
	// deploy definition owned by the instance in the same transaction.
	var err error
	if e.manager != nil && e.manager.txRunner != nil {
		err = database.DeleteInstanceWithOwnedDefinition(e.manager.txRunner, inst)
	} else {
		err = e.instanceRepo.Delete(inst.ID)
	}
	if err != nil {
		return err
	}
	e.afterDelete(ctx, inst, followerIDs)
	return nil
}

// afterDelete tells subscribers and the owner about a delete, as the API
// delete path does: post-instance-delete and delete-completed (with the
// trigger of ctx, for example the cleanup policy) and the notification
// instance.deleted for the owner and followerIDs (read before the delete).
// The delete is done, so a caller that stops (the end of a leadership term)
// does not cancel the events; the subscription timeouts limit them.
func (e *CleanupExecutor) afterDelete(ctx context.Context, inst *models.StackInstance, followerIDs []string) {
	if e.manager == nil {
		return
	}
	hookCtx := context.WithoutCancel(ctx)
	opts := e.triggerOpts(ctx)
	trigger, ok := hooks.TriggerFromContext(ctx)
	for _, event := range []string{hooks.EventPostInstanceDelete, hooks.EventDeleteCompleted} {
		if err := e.manager.fireDeployHook(hookCtx, event, inst, "", time.Time{}, opts); err != nil {
			slog.Warn("hook after cleanup delete failed", "event", event, "instance_id", inst.ID, "error", err)
		}
	}
	message := fmt.Sprintf("Stack %s has been deleted", inst.Name)
	if ok && trigger.Type == hooks.TriggerCleanupPolicy && trigger.Name != "" {
		message = fmt.Sprintf("Stack %s has been deleted by cleanup policy %q", inst.Name, trigger.Name)
	}
	target := models.NewNotificationTarget(inst)
	target.FollowerIDs = followerIDs
	if target.FollowerIDs == nil {
		// The rows are gone: no lookup after the delete.
		target.FollowerIDs = []string{}
	}
	e.manager.notifyInstance(target, "instance.deleted", "Stack deleted", message)
}

func (e *CleanupExecutor) resolveCharts(inst *models.StackInstance) ([]models.ChartConfig, error) {
	def, err := e.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		return nil, fmt.Errorf("finding definition: %w", err)
	}
	charts, err := e.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		return nil, fmt.Errorf("listing charts: %w", err)
	}
	if len(charts) == 0 {
		return nil, fmt.Errorf("no charts configured for definition %s", def.ID)
	}
	return charts, nil
}

// triggerOpts returns hook options with the trigger of ctx, if any.
func (e *CleanupExecutor) triggerOpts(ctx context.Context) hookOpts {
	if trigger, ok := hooks.TriggerFromContext(ctx); ok {
		return hookOpts{Trigger: &trigger}
	}
	return hookOpts{}
}
