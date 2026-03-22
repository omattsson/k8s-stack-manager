package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"

	"github.com/robfig/cron/v3"
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
	Action       string `json:"action"`
	Status       string `json:"status"` // "success", "error", "dry_run"
	Error        string `json:"error,omitempty"`
}

// Scheduler manages cron-based cleanup policy execution.
type Scheduler struct {
	cron         *cron.Cron
	policyRepo   models.CleanupPolicyRepository
	instanceRepo models.StackInstanceRepository
	auditRepo    models.AuditLogRepository
	executor     ActionExecutor // can be nil (dry-run only mode)
	mu           sync.Mutex
	entryMap     map[string]cron.EntryID // policyID → cron entry
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewScheduler creates a new cleanup scheduler.
func NewScheduler(
	policyRepo models.CleanupPolicyRepository,
	instanceRepo models.StackInstanceRepository,
	auditRepo models.AuditLogRepository,
	executor ActionExecutor,
) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cron:         cron.New(),
		policyRepo:   policyRepo,
		instanceRepo: instanceRepo,
		auditRepo:    auditRepo,
		executor:     executor,
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
}

func (s *Scheduler) executePolicyWithOptions(policy *models.CleanupPolicy, dryRun bool) ([]CleanupResult, error) {
	filter, err := ParseCondition(policy.Condition)
	if err != nil {
		return nil, fmt.Errorf("parsing condition: %w", err)
	}

	// Get instances for target cluster(s).
	var instances []models.StackInstance
	if policy.ClusterID == "all" {
		instances, err = s.instanceRepo.List()
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
	} else {
		instances, err = s.instanceRepo.FindByCluster(policy.ClusterID)
		if err != nil {
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
			Action:       policy.Action,
		}

		if dryRun {
			result.Status = "dry_run"
		} else if s.executor != nil {
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
			var execErr error
			switch policy.Action {
			case "stop":
				execErr = s.executor.StopInstance(ctx, inst)
			case "clean":
				execErr = s.executor.CleanInstance(ctx, inst)
			case "delete":
				execErr = s.executor.DeleteInstance(ctx, inst)
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
