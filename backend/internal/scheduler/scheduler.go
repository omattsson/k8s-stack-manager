package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"backend/internal/hooks"
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

// HookFirer dispatches lifecycle events to webhook subscribers.
// *hooks.Dispatcher implements it.
type HookFirer interface {
	Fire(ctx context.Context, event string, envelope hooks.EventEnvelope) error
}

// Run kinds of a cleanup policy run (cleanup-policy-executed "run").
const (
	runScheduled = "scheduled"
	runManual    = "manual"
)

// maxHookInstances limits the instance list of a cleanup-policy-executed
// envelope; the counts always cover all matching instances.
const maxHookInstances = 200

// defaultActionTimeout limits one stop, clean or delete of a policy run.
const defaultActionTimeout = 5 * time.Minute

// hookTimeouts is implemented by *hooks.Dispatcher.
type hookTimeouts interface {
	TotalTimeout(event string) time.Duration
}

// maxHookErrorLen limits the error text of one instance in the envelope.
const maxHookErrorLen = 500

// maxMessageNames is the number of instance names in the
// cleanup.policy.executed channel message; more give "and N more".
const maxMessageNames = 10

// DefaultReloadInterval is how often an active scheduler reads the enabled
// policies again (see Run).
const DefaultReloadInterval = time.Minute

// catchUpWindow is how far back activate looks for a scheduled run that no
// replica did (for example during a leader change).
const catchUpWindow = 5 * time.Minute

// Scheduler manages cron-based cleanup policy execution.
//
// Only an active scheduler runs cron jobs. Run activates it for one
// leadership term; Start activates it until Stop. Manual runs (RunPolicy)
// work on every replica.
type Scheduler struct {
	cron           *cron.Cron      // nil when not active
	jobCtx         context.Context // context of the cron jobs of the active period
	ctx            context.Context // process lifetime; manual runs
	cancel         context.CancelFunc
	policyRepo     models.CleanupPolicyRepository
	instanceRepo   models.StackInstanceRepository
	auditRepo      models.AuditLogRepository
	executor       ActionExecutor          // can be nil (dry-run only mode)
	notifier       CleanupNotifier         // can be nil
	hooks          HookFirer               // can be nil
	entryMap       map[string]cron.EntryID // policyID → cron entry
	loaded         map[string]string       // policyID → schedule of the loaded policies
	reloadInterval time.Duration
	now            func() time.Time // clock of the catch-up check; tests replace it
	jobs           sync.WaitGroup   // catch-up runs started outside the cron instance
	mu             sync.Mutex
	active         bool
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
		policyRepo:     policyRepo,
		instanceRepo:   instanceRepo,
		auditRepo:      auditRepo,
		executor:       executor,
		notifier:       notifier,
		entryMap:       make(map[string]cron.EntryID),
		loaded:         make(map[string]string),
		reloadInterval: DefaultReloadInterval,
		now:            time.Now,
		ctx:            ctx,
		cancel:         cancel,
	}
}

// WithReloadInterval sets how often Run reads the enabled policies again.
// Values <= 0 are ignored.
func (s *Scheduler) WithReloadInterval(d time.Duration) *Scheduler {
	if d > 0 {
		s.reloadInterval = d
	}
	return s
}

// WithHooks sets the dispatcher for cleanup-policy-executed. Pass nil (an
// untyped nil, not a nil *hooks.Dispatcher) to disable it.
func (s *Scheduler) WithHooks(h HookFirer) *Scheduler {
	s.hooks = h
	return s
}

// Active reports whether the scheduler runs cron jobs now.
func (s *Scheduler) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Start loads enabled policies and starts the cron scheduler. It stays
// active until Stop.
func (s *Scheduler) Start() error {
	return s.activate(s.ctx)
}

// Stop gracefully stops the cron scheduler, waiting for running jobs to
// finish, and cancels manual runs.
func (s *Scheduler) Stop() {
	s.deactivate()
	s.cancel()
}

// Run activates the scheduler until ctx is done (one leadership term). It
// blocks. Run can be called again after it returned.
//
// While active, Run reads the enabled policies every reload interval and
// updates the cron entries when they changed. A policy change that another
// replica handles (its Reload does nothing there) so reaches the leader
// within one reload interval.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		err := s.activate(ctx)
		if err == nil {
			break
		}
		slog.Error("Cleanup scheduler: failed to load policies, retrying", "error", err, "retry_in", s.reloadInterval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.reloadInterval):
		}
	}
	defer s.deactivate()

	ticker := time.NewTicker(s.reloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Reload(); err != nil {
				slog.Error("Cleanup scheduler: periodic reload failed", "error", err)
			}
		}
	}
}

// activate loads the enabled policies and starts a new cron instance whose
// jobs use jobCtx. It does nothing when the scheduler is already active.
func (s *Scheduler) activate(jobCtx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return nil
	}
	policies, err := s.policyRepo.ListEnabled()
	if err != nil {
		return fmt.Errorf("loading policies: %w", err)
	}
	s.cron = cron.New(
		cron.WithLocation(time.UTC),
		cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)),
	)
	s.jobCtx = jobCtx
	s.loadLocked(policies)
	// Read the time before the cron instance starts: the cron instance runs
	// scheduled times after its start, the catch-up only times strictly
	// before this value. So no scheduled time runs twice.
	startedAt := s.now().UTC()
	s.cron.Start()
	s.active = true
	slog.Info("Cleanup scheduler started", "policies", len(policies))
	s.catchUpLocked(policies, startedAt)
	return nil
}

// catchUpLocked runs a policy once when its last scheduled time strictly
// before now is within catchUpWindow and after its last run (or its creation when it never ran).
// This covers a run that fell into a leader change. @every schedules have no
// fixed times and are not caught up. The run goes through the wrapped cron
// job, so it does not overlap a cron run of the same policy. s.mu must be
// held.
func (s *Scheduler) catchUpLocked(policies []models.CleanupPolicy, now time.Time) {
	for i := range policies {
		p := &policies[i]
		entryID, ok := s.entryMap[p.ID]
		if !ok {
			continue
		}
		prev, ok := previousScheduledTime(p.Schedule, now)
		if !ok {
			continue
		}
		ref := p.CreatedAt
		if p.LastRunAt != nil {
			ref = *p.LastRunAt
		}
		if !prev.After(ref) {
			continue
		}
		slog.Info("Cleanup policy missed its scheduled run, running it now",
			"policy", p.Name, "scheduled_at", prev, "last_run_at", p.LastRunAt)
		job := s.cron.Entry(entryID).WrappedJob
		s.jobs.Add(1)
		go func() {
			defer s.jobs.Done()
			job.Run()
		}()
	}
}

// previousScheduledTime returns the latest scheduled time of a cron
// expression in (now - catchUpWindow, now). It returns false for @every
// schedules and invalid expressions.
func previousScheduledTime(spec string, now time.Time) (time.Time, bool) {
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		return time.Time{}, false
	}
	if _, every := sched.(cron.ConstantDelaySchedule); every {
		return time.Time{}, false
	}
	var prev time.Time
	for t := sched.Next(now.Add(-catchUpWindow)); !t.IsZero() && t.Before(now); t = sched.Next(t) {
		prev = t
	}
	return prev, !prev.IsZero()
}

// deactivate stops the cron instance and waits for running jobs.
func (s *Scheduler) deactivate() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	c := s.cron
	s.cron = nil
	s.active = false
	s.entryMap = make(map[string]cron.EntryID)
	s.loaded = make(map[string]string)
	s.mu.Unlock()

	<-c.Stop().Done()
	s.jobs.Wait()
	slog.Info("Cleanup scheduler stopped")
}

// Reload re-reads enabled policies and updates cron entries. When the
// scheduler is not active (a replica that is not the leader), Reload does
// nothing: the active scheduler on the leader reads the change at its next
// periodic reload.
func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.active {
		slog.Debug("Cleanup scheduler is not active on this replica; the leader loads the policy change")
		return nil
	}

	policies, err := s.policyRepo.ListEnabled()
	if err != nil {
		return fmt.Errorf("reloading policies: %w", err)
	}
	if s.sameSchedulesLocked(policies) {
		return nil
	}

	// Remove all existing entries.
	for id, entryID := range s.entryMap {
		s.cron.Remove(entryID)
		delete(s.entryMap, id)
	}
	s.loadLocked(policies)
	slog.Info("Cleanup scheduler reloaded", "policies", len(policies))
	return nil
}

// sameSchedulesLocked reports whether policies have the same IDs and
// schedules as the loaded entries. The jobs read the other policy fields at
// run time, so only the schedule needs new cron entries.
func (s *Scheduler) sameSchedulesLocked(policies []models.CleanupPolicy) bool {
	if len(policies) != len(s.loaded) {
		return false
	}
	for i := range policies {
		if schedule, ok := s.loaded[policies[i].ID]; !ok || schedule != policies[i].Schedule {
			return false
		}
	}
	return true
}

// loadLocked adds a cron entry for each policy. s.mu must be held.
func (s *Scheduler) loadLocked(policies []models.CleanupPolicy) {
	s.loaded = make(map[string]string, len(policies))
	for i := range policies {
		s.loaded[policies[i].ID] = policies[i].Schedule
		s.schedulePolicy(policies[i])
	}
}

func (s *Scheduler) schedulePolicy(policy models.CleanupPolicy) {
	policyID := policy.ID
	jobCtx := s.jobCtx
	entryID, err := s.cron.AddFunc(policy.Schedule, func() {
		s.executeScheduledPolicy(jobCtx, policyID)
	})
	if err != nil {
		slog.Error("Failed to schedule cleanup policy", "policy", policy.Name, "error", err)
		return
	}
	s.entryMap[policy.ID] = entryID
}

// RunPolicy executes a policy immediately (manual trigger). Returns matched instances.
func (s *Scheduler) RunPolicy(policyID string, dryRun bool) ([]CleanupResult, error) {
	policy, err := s.policyRepo.FindByID(policyID)
	if err != nil {
		return nil, err
	}
	results, err := s.executePolicyWithOptions(s.ctx, policy, dryRun)
	if err == nil {
		s.firePolicyExecuted(s.ctx, policy, results, dryRun, runManual)
	}
	return results, err
}

// executeScheduledPolicy runs a policy for its cron entry. It reads the
// policy again, so an edit that the scheduler has not reloaded yet (up to one
// reload interval) is used, and a disabled or deleted policy does not run.
func (s *Scheduler) executeScheduledPolicy(ctx context.Context, policyID string) {
	if ctx.Err() != nil {
		return
	}
	policy, err := s.policyRepo.FindByID(policyID)
	if err != nil {
		slog.Error("Cleanup policy not loaded for its scheduled run", "policy_id", policyID, "error", err)
		return
	}
	if !policy.Enabled {
		slog.Info("Cleanup policy is disabled, scheduled run skipped", "policy", policy.Name)
		return
	}

	results, err := s.executePolicyWithOptions(ctx, policy, policy.DryRun)
	if ctx.Err() != nil {
		// The leadership term ended during the run. The next leader runs
		// the policy again (catch-up or next schedule), so do not record
		// a run and do not send the summary.
		slog.Warn("Cleanup policy run interrupted by the end of the leadership term",
			"policy", policy.Name, "processed", len(results))
		return
	}
	if err != nil {
		slog.Error("Cleanup policy execution failed", "policy", policy.Name, "error", err)
	}

	// Update LastRunAt regardless of outcome.
	now := time.Now().UTC()
	policy.LastRunAt = &now
	if updateErr := s.policyRepo.Update(policy); updateErr != nil {
		slog.Error("Failed to update policy LastRunAt", "policy", policy.ID, "error", updateErr)
	}
	slog.Info("Cleanup policy executed", "policy", policy.Name, "results", len(results))

	s.notifyPolicyExecuted(ctx, policy, results)
	s.firePolicyExecuted(ctx, policy, results, policy.DryRun, runScheduled)
}

func (s *Scheduler) executePolicyWithOptions(parent context.Context, policy *models.CleanupPolicy, dryRun bool) ([]CleanupResult, error) {
	ctx, span := schedulerTracer.Start(parent, "cleanup.execute_policy",
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
	var interrupted error
	for i := range instances {
		// Stop when the caller ended (leadership term over). The remaining
		// instances get no action and no audit entry.
		if err := ctx.Err(); err != nil {
			interrupted = err
			break
		}
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
			execCtx, cancel := context.WithTimeout(ctx, s.actionTimeout(policy.Action))
			// The envelopes of the stop, clean and delete say that this
			// policy started them.
			execCtx = hooks.WithTrigger(execCtx, policyTrigger(policy))
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
			if execErr != nil && ctx.Err() != nil {
				// Cancelled during the action: not a policy error.
				interrupted = ctx.Err()
				break
			}
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
	if interrupted != nil {
		span.SetStatus(codes.Error, "interrupted")
		return results, fmt.Errorf("cleanup policy run interrupted: %w", interrupted)
	}
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

func (s *Scheduler) notifyPolicyExecuted(ctx context.Context, policy *models.CleanupPolicy, results []CleanupResult) {
	if s.notifier == nil || len(results) == 0 {
		return
	}

	dryRunLabel := ""
	if policy.DryRun {
		dryRunLabel = " (dry run)"
	}

	var affected, failed int
	var names []string
	for _, r := range results {
		switch r.Status {
		case "success", "dry_run":
			affected++
			names = append(names, r.InstanceName)
		case "error":
			failed++
		}
	}
	message := fmt.Sprintf("Policy %q matched %d instance(s), action: %s%s", policy.Name, affected, policy.Action, dryRunLabel)
	if len(names) > 0 {
		message += ": " + joinNames(names, maxMessageNames)
	}
	if failed > 0 {
		message += fmt.Sprintf(" (%d failed)", failed)
	}
	_ = s.notifier.NotifySystem(ctx,
		"cleanup.policy.executed",
		fmt.Sprintf("Cleanup policy %q ran%s", policy.Name, dryRunLabel),
		message,
		"cleanup_policy", policy.ID,
	)

	if policy.DryRun {
		return
	}
	for _, r := range results {
		// The executor sends the owner instance.deleted for a delete, as
		// the API delete path does.
		if r.Status != "success" || r.Action == "delete" {
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

// joinNames returns the first limit names joined with ", ", then
// "and N more".
func joinNames(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:limit], ", "), len(names)-limit)
}

// policyTrigger returns the hook trigger of policy.
func policyTrigger(policy *models.CleanupPolicy) hooks.Trigger {
	return hooks.Trigger{Type: hooks.TriggerCleanupPolicy, ID: policy.ID, Name: policy.Name}
}

// firePolicyExecuted fires cleanup-policy-executed for a run with at least
// one matching instance. Dispatch errors are logged by the dispatcher.
func (s *Scheduler) firePolicyExecuted(ctx context.Context, policy *models.CleanupPolicy, results []CleanupResult, dryRun bool, run string) {
	if s.hooks == nil || len(results) == 0 {
		return
	}
	trigger := policyTrigger(policy)
	_ = s.hooks.Fire(ctx, hooks.EventCleanupPolicyExecuted, hooks.EventEnvelope{
		Trigger:       &trigger,
		CleanupPolicy: buildPolicyRun(policy, results, dryRun, run),
	})
}

// buildPolicyRun builds the cleanup-policy-executed payload.
func buildPolicyRun(policy *models.CleanupPolicy, results []CleanupResult, dryRun bool, run string) *hooks.CleanupPolicyRun {
	out := &hooks.CleanupPolicyRun{
		ID:        policy.ID,
		Name:      policy.Name,
		Action:    policy.Action,
		ClusterID: policy.ClusterID,
		Condition: policy.Condition,
		DryRun:    dryRun,
		Run:       run,
		Matched:   len(results),
		Instances: make([]hooks.CleanupPolicyInstance, 0, min(len(results), maxHookInstances)),
	}
	for _, r := range results {
		switch r.Status {
		case hooks.CleanupResultSuccess:
			out.Succeeded++
		case hooks.CleanupResultError:
			out.Failed++
		}
		if len(out.Instances) >= maxHookInstances {
			out.InstancesTruncated = true
			continue
		}
		out.Instances = append(out.Instances, hooks.CleanupPolicyInstance{
			ID:        r.InstanceID,
			Name:      r.InstanceName,
			Namespace: r.Namespace,
			OwnerID:   r.OwnerID,
			Result:    r.Status,
			Error:     hookErrorText(r.Error),
		})
	}
	return out
}

// hookErrorText flattens an error text to one line and limits its length.
func hookErrorText(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxHookErrorLen {
		cut := maxHookErrorLen
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…"
	}
	return msg
}

// actionTimeout returns the timeout of one action of a policy run: 5
// minutes, for a delete at least the sum of the pre-instance-delete hook
// timeouts plus one minute (the hooks run before the delete).
func (s *Scheduler) actionTimeout(action string) time.Duration {
	if action != "delete" {
		return defaultActionTimeout
	}
	ht, ok := s.hooks.(hookTimeouts)
	if !ok {
		return defaultActionTimeout
	}
	return max(defaultActionTimeout, ht.TotalTimeout(hooks.EventPreInstanceDelete)+time.Minute)
}
