package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"backend/internal/models"

	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ActionExecutor performs cleanup actions on stack instances.
type ActionExecutor interface {
	StopInstance(ctx context.Context, instance *models.StackInstance) error
	CleanInstance(ctx context.Context, instance *models.StackInstance) error
	DeleteInstance(ctx context.Context, instance *models.StackInstance) error
}

// CleanupResult describes the outcome of a cleanup action on a single instance.
type CleanupResult struct {
	InstanceID   string `json:"instance_id"`
	InstanceName string `json:"instance_name"`
	Namespace    string `json:"namespace"`
	OwnerID      string `json:"owner_id"`
	Action       string `json:"action"`
	Status       string `json:"status"` // "success", "error", "dry_run"
	Error        string `json:"error,omitempty"`
}

// CleanupNotifier creates in-app notifications for cleanup events.
type CleanupNotifier interface {
	Notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string) error
	NotifySystem(ctx context.Context, notifType, title, message, entityType, entityID string) error
}

// Scheduler manages cron-based cleanup policy execution.
type Scheduler struct {
	cron         *cron.Cron
	policyRepo   models.CleanupPolicyRepository
	instanceRepo models.StackInstanceRepository
	auditRepo    models.AuditLogRepository
	executor     ActionExecutor    // can be nil (dry-run only mode)
	notifier     CleanupNotifier   // can be nil
	mu           sync.Mutex
	entryMap     map[string]cron.EntryID // policyID → cron entry
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewScheduler creates a new cleanup scheduler. notifier may be nil.
func NewScheduler(
	policyRepo models.CleanupPolicyRepository,
	instanceRepo models.StackInstanceRepository,
	auditRepo models.AuditLogRepository,
	executor ActionExecutor,
	notifier CleanupNotifier,
) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cron: cron.New(
			cron.WithLocation(time.UTC),
			cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)),
		),
		policyRepo:   policyRepo,
		instanceRepo: instanceRepo,
		auditRepo:    auditRepo,
		executor:     executor,
		notifier:     notifier,
		entryMap:     make(map[string]cron.EntryID),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Start loads enabled policies and starts the cron scheduler.
func (s *Scheduler) Start() error {
	policies, err := s.policyRepo.ListEnabled()
	if err != nil {
		return fmt.Errorf("loading policies: %w", err)
	}
	s.mu.Lock()
	for i := range policies {
		s.schedulePolicy(policies[i])
	}
	s.mu.Unlock()
	s.cron.Start()
	slog.Info("Cleanup scheduler started", "policies", len(policies))
	return nil
}

// Stop gracefully stops the cron scheduler, waiting for running jobs to finish.
func (s *Scheduler) Stop() {
	s.cancel()
	ctx := s.cron.Stop()
	<-ctx.Done()
	slog.Info("Cleanup scheduler stopped")
}

// Reload re-reads enabled policies and updates cron entries.
func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove all existing entries.
	for id, entryID := range s.entryMap {
		s.cron.Remove(entryID)
		delete(s.entryMap, id)
	}

	// Reload enabled policies.
	policies, err := s.policyRepo.ListEnabled()
	if err != nil {
		return fmt.Errorf("reloading policies: %w", err)
	}
	for i := range policies {
		s.schedulePolicy(policies[i])
	}
	slog.Info("Cleanup scheduler reloaded", "policies", len(policies))
	return nil
}

func (s *Scheduler) schedulePolicy(policy models.CleanupPolicy) {
	p := policy // capture for closure
	entryID, err := s.cron.AddFunc(p.Schedule, func() {
		s.executePolicy(p)
	})
	if err != nil {
		slog.Error("Failed to schedule cleanup policy", "policy", p.Name, "error", err)
		return
	}
	s.entryMap[p.ID] = entryID
}

// RunPolicy executes a policy immediately (manual trigger). Returns matched instances.
func (s *Scheduler) RunPolicy(policyID string, dryRun bool) ([]CleanupResult, error) {
	policy, err := s.policyRepo.FindByID(policyID)
	if err != nil {
		return nil, err
	}
	return s.executePolicyWithOptions(policy, dryRun)
}

func (s *Scheduler) executePolicy(policy models.CleanupPolicy) {
	results, err := s.executePolicyWithOptions(&policy, policy.DryRun)
	if err != nil {
		slog.Error("Cleanup policy execution failed", "policy", policy.Name, "error", err)
	}

	// Update LastRunAt regardless of outcome.
	now := time.Now().UTC()
	policy.LastRunAt = &now
	if updateErr := s.policyRepo.Update(&policy); updateErr != nil {
		slog.Error("Failed to update policy LastRunAt", "policy", policy.ID, "error", updateErr)
	}
	slog.Info("Cleanup policy executed", "policy", policy.Name, "results", len(results))

	s.notifyPolicyExecuted(&policy, results)
}

func (s *Scheduler) executePolicyWithOptions(policy *models.CleanupPolicy, dryRun bool) ([]CleanupResult, error) {
	ctx, span := schedulerTracer.Start(context.Background(), "cleanup.execute_policy",
		trace.WithAttributes(
			attribute.String("policy.id", policy.ID),
			attribute.String("policy.action", policy.Action),
			attribute.String("policy.cluster", policy.ClusterID),
			attribute.Bool("policy.dry_run", dryRun),
		),
	)
	defer span.End()

	dryRunStr := strconv.FormatBool(dryRun)
	sMetrics.executionsTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("action", policy.Action),
			attribute.String("dry_run", dryRunStr),
		),
	)

	filter, err := ParseCondition(policy.Condition)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("parsing condition: %w", err)
	}

	// Get instances for target cluster(s).
	var instances []models.StackInstance
	if policy.ClusterID == "all" {
		instances, err = s.instanceRepo.List()
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("listing instances: %w", err)
		}
	} else {
		instances, err = s.instanceRepo.FindByCluster(policy.ClusterID)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("finding instances by cluster: %w", err)
		}
	}

	var results []CleanupResult
	for i := range instances {
		inst := &instances[i]
		if !filter.MatchesInstance(inst) {
			continue
		}

		result := CleanupResult{
			InstanceID:   inst.ID,
			InstanceName: inst.Name,
			Namespace:    inst.Namespace,
			OwnerID:      inst.OwnerID,
			Action:       policy.Action,
		}

		if dryRun {
			result.Status = "dry_run"
		} else if s.executor != nil {
			execCtx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
			var execErr error
			switch policy.Action {
			case "stop":
				execErr = s.executor.StopInstance(execCtx, inst)
			case "clean":
				execErr = s.executor.CleanInstance(execCtx, inst)
			case "delete":
				execErr = s.executor.DeleteInstance(execCtx, inst)
			default:
				execErr = fmt.Errorf("unknown cleanup action: %s", policy.Action)
			}
			cancel()
			if execErr != nil {
				result.Status = "error"
				result.Error = execErr.Error()
			} else {
				result.Status = "success"
			}
			s.createAuditEntry(policy, inst, result)
		} else {
			// No executor — treat as dry run.
			result.Status = "dry_run"
		}

		results = append(results, result)
	}

	span.SetAttributes(attribute.Int("cleanup.matched_count", len(results)))
	span.SetStatus(codes.Ok, "")
	return results, nil
}

func (s *Scheduler) createAuditEntry(policy *models.CleanupPolicy, inst *models.StackInstance, result CleanupResult) {
	if s.auditRepo == nil {
		return
	}
	details, _ := json.Marshal(map[string]string{
		"policy_id":   policy.ID,
		"policy_name": policy.Name,
		"action":      policy.Action,
		"status":      result.Status,
	})
	entry := &models.AuditLog{
		UserID:     "system",
		Username:   "system",
		Action:     "cleanup_policy_executed",
		EntityType: "stack_instance",
		EntityID:   inst.ID,
		Details:    string(details),
	}
	if err := s.auditRepo.Create(entry); err != nil {
		slog.Error("Failed to create audit log for cleanup", "error", err)
	}
}

func (s *Scheduler) notifyPolicyExecuted(policy *models.CleanupPolicy, results []CleanupResult) {
	if s.notifier == nil || len(results) == 0 {
		return
	}
	ctx := s.ctx

	dryRunLabel := ""
	if policy.DryRun {
		dryRunLabel = " (dry run)"
	}

	var affected int
	for _, r := range results {
		if r.Status == "success" || r.Status == "dry_run" {
			affected++
		}
	}
	_ = s.notifier.NotifySystem(ctx,
		"cleanup.policy.executed",
		fmt.Sprintf("Cleanup policy %q ran%s", policy.Name, dryRunLabel),
		fmt.Sprintf("Policy %q matched %d instance(s), action: %s%s", policy.Name, affected, policy.Action, dryRunLabel),
		"cleanup_policy", policy.ID,
	)

	if policy.DryRun {
		return
	}
	for _, r := range results {
		if r.Status != "success" {
			continue
		}
		notifType := "cleanup.policy." + r.Action
		_ = s.notifier.Notify(ctx, r.OwnerID, notifType,
			fmt.Sprintf("Stack %q %s by cleanup policy", r.InstanceName, actionPastTense(r.Action)),
			fmt.Sprintf("Cleanup policy %q performed %s on your stack %q", policy.Name, r.Action, r.InstanceName),
			"stack_instance", r.InstanceID,
		)
	}
}

func actionPastTense(action string) string {
	switch action {
	case "stop":
		return "stopped"
	case "clean":
		return "cleaned"
	case "delete":
		return "deleted"
	default:
		return action + "ed"
	}
}
