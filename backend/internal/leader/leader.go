// Package leader selects one backend replica to run the leader-only
// background workers (TTL reaper, cleanup scheduler, monitors, pollers).
//
// With election enabled, the replicas compete for a coordination.k8s.io/v1
// Lease through client-go leader election. With election disabled, the
// process is always the leader (docker-compose, local development, one
// replica).
//
// A replica that loses the lease does not exit. It stops the leader workers
// and campaigns for the lease again.
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Default timings (the client-go defaults).
const (
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second
)

// Config holds the leader election settings.
type Config struct {
	// LeaseDuration is how long other candidates wait before they take over
	// a lease that is not renewed.
	LeaseDuration time.Duration
	// RenewDeadline is how long the leader tries to renew before it stops
	// leading.
	RenewDeadline time.Duration
	// RetryPeriod is the time between two acquire or renew attempts.
	RetryPeriod time.Duration
	// LeaseName is the name of the Lease object.
	LeaseName string
	// Namespace is the namespace of the Lease object.
	Namespace string
	// Identity identifies this candidate in the Lease (the pod name).
	Identity string
	// Enabled turns the election on. When false, this process is always
	// the leader.
	Enabled bool
}

// Validate checks the settings. It checks only when election is enabled.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.LeaseName == "" {
		return errors.New("lease name is required")
	}
	if c.Namespace == "" {
		return errors.New("namespace is required")
	}
	if c.Identity == "" {
		return errors.New("identity is required")
	}
	if c.LeaseDuration < time.Second {
		// The Lease stores the duration in whole seconds.
		return errors.New("lease duration must be at least 1s")
	}
	if c.RenewDeadline <= 0 || c.RetryPeriod <= 0 {
		return errors.New("renew deadline and retry period must be greater than zero")
	}
	if c.LeaseDuration <= c.RenewDeadline {
		return errors.New("lease duration must be greater than renew deadline")
	}
	if c.RenewDeadline <= time.Duration(leaderelection.JitterFactor*float64(c.RetryPeriod)) {
		return fmt.Errorf("renew deadline must be greater than %.1f x retry period", leaderelection.JitterFactor)
	}
	return nil
}

// Elector runs the leader election for one process.
type Elector struct {
	client  kubernetes.Interface // nil when election is disabled
	cfg     Config
	leading atomic.Bool
}

// New creates an Elector. client is required when election is enabled and
// ignored when it is disabled.
func New(cfg Config, client kubernetes.Interface) (*Elector, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("leader election config: %w", err)
	}
	if cfg.Enabled && client == nil {
		return nil, errors.New("leader election is enabled but no Kubernetes client is set")
	}
	return &Elector{client: client, cfg: cfg}, nil
}

// NewInCluster creates an Elector. When election is enabled, it builds the
// Lease client from the in-cluster service account configuration. It returns
// an error when the process does not run in a Kubernetes pod.
func NewInCluster(cfg Config) (*Elector, error) {
	if !cfg.Enabled {
		return New(cfg, nil)
	}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("LEADER_ELECTION_ENABLED=true needs the in-cluster Kubernetes configuration (run in a pod with a service account, or set LEADER_ELECTION_ENABLED=false): %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client for leader election: %w", err)
	}
	return New(cfg, client)
}

// IsLeader reports whether this process runs the leader workers now.
func (e *Elector) IsLeader() bool {
	return e.leading.Load()
}

// Enabled reports whether the election is on.
func (e *Elector) Enabled() bool {
	return e.cfg.Enabled
}

// Run blocks until ctx is done.
//
// For each leadership term, Run calls onStartedLeading with a term context.
// The term context is cancelled when the term ends (lease lost or ctx done).
// Then Run calls onStoppedLeading and waits for it to return before it
// campaigns again. onStoppedLeading must stop the leader workers, wait for
// them and report whether they stopped in time. When they did not, the
// lease is not released (the other replicas wait for the lease duration).
// Run never calls the two callbacks at the same time.
//
// When ctx is done, the leader stops its workers and then releases the
// lease, so another replica takes over after about one retry period.
func (e *Elector) Run(ctx context.Context, onStartedLeading func(ctx context.Context), onStoppedLeading func() bool) {
	if !e.cfg.Enabled {
		slog.Info("Leader election is disabled: this replica runs the leader workers")
		e.runTerm(ctx, onStartedLeading, onStoppedLeading)
		return
	}

	slog.Info("Leader election started",
		"lease", e.cfg.LeaseName,
		"namespace", e.cfg.Namespace,
		"identity", e.cfg.Identity,
		"lease_duration", e.cfg.LeaseDuration,
		"renew_deadline", e.cfg.RenewDeadline,
		"retry_period", e.cfg.RetryPeriod,
	)
	e.checkLeaseAccess(ctx)
	for ctx.Err() == nil {
		if err := e.campaign(ctx, onStartedLeading, onStoppedLeading); err != nil {
			slog.Error("Leader election failed", "error", err)
		}
		// Wait a short time before the next campaign so that an error that
		// repeats at once does not make a busy loop.
		select {
		case <-ctx.Done():
		case <-time.After(e.cfg.RetryPeriod):
		}
	}
	slog.Info("Leader election stopped", "identity", e.cfg.Identity)
}

// runTerm runs one leadership term: start, wait for ctx, stop. It reports
// whether the workers stopped in time.
func (e *Elector) runTerm(ctx context.Context, onStartedLeading func(ctx context.Context), onStoppedLeading func() bool) bool {
	e.leading.Store(true)
	slog.Info("Leadership acquired: starting leader workers", "identity", e.cfg.Identity)
	onStartedLeading(ctx)
	<-ctx.Done()
	e.leading.Store(false)
	slog.Info("Leadership lost: stopping leader workers", "identity", e.cfg.Identity)
	if !onStoppedLeading() {
		slog.Warn("Leader workers did not stop in time", "identity", e.cfg.Identity)
		return false
	}
	slog.Info("Leader workers stopped", "identity", e.cfg.Identity)
	return true
}

// campaign runs one client-go election round: acquire, lead until the lease
// is lost or ctx is done, stop the workers, release the lease.
//
// The release is done here, not by client-go (ReleaseOnCancel is false):
// client-go releases the lease before it cancels the leader context, so the
// next leader could start while the workers of this replica still stop.
// Here the release happens only after the term (and so the workers) ended.
func (e *Elector) campaign(ctx context.Context, onStartedLeading func(ctx context.Context), onStoppedLeading func() bool) error {
	lock := newRenewTrackingLock(&resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      e.cfg.LeaseName,
			Namespace: e.cfg.Namespace,
		},
		Client:     e.client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: e.cfg.Identity},
	})

	// runCtx ends this round. It is cancelled after a term ended, so that
	// client-go stops renewing and campaign can release the lease.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// client-go starts OnStartedLeading in a goroutine and can return from
	// Run before that goroutine runs. term makes sure that a term either runs
	// completely before campaign returns, or does not run.
	term := &termGuard{done: make(chan struct{})}

	le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   e.cfg.LeaseDuration,
		RenewDeadline:   e.cfg.RenewDeadline,
		RetryPeriod:     e.cfg.RetryPeriod,
		ReleaseOnCancel: false,
		Name:            e.cfg.LeaseName,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				if !term.begin() {
					return
				}
				defer close(term.done)
				termCtx, termCancel := context.WithCancel(leaderCtx)
				defer termCancel()
				// The watchdog ends only the term. The lease is released
				// after the workers stopped (runCancel, then release).
				go e.watchRenewals(termCtx, lock, termCancel)
				term.stopped = e.runTerm(termCtx, onStartedLeading, onStoppedLeading)
				runCancel()
			},
			// The term ends in OnStartedLeading when termCtx is done.
			// client-go also calls OnStoppedLeading when this candidate never
			// became the leader, so it cannot be used for the term.
			OnStoppedLeading: func() {},
			OnNewLeader: func(identity string) {
				if identity == e.cfg.Identity {
					return
				}
				slog.Info("Leader election: another replica is the leader",
					"leader", identity, "identity", e.cfg.Identity)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create leader elector: %w", err)
	}

	le.Run(runCtx)
	if started, stopped := term.wait(); started {
		if !stopped {
			// Releasing now would let the next leader start while these
			// workers still run. The lease expires after its duration.
			slog.Warn("Leader election: lease not released because the leader workers still run; another replica takes over after the lease duration",
				"identity", e.cfg.Identity)
		} else {
			e.release(lock)
		}
	}
	return nil
}

// releaseOutcome is the result of release.
type releaseOutcome int

const (
	releaseDone      releaseOutcome = iota // the lease was released
	releaseNotHolder                       // another replica holds the lease
	releaseConflict                        // another replica took the lease during the release
	releaseFailed                          // read or update failed
)

// release gives up the lease when this candidate still holds it, so that
// another replica can take over after one retry period instead of the lease
// duration. The update uses the resource version of the read, so it never
// overwrites a lease that another replica took in the meantime.
func (e *Elector) release(lock resourcelock.Interface) releaseOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.RenewDeadline)
	defer cancel()
	rec, _, err := lock.Get(ctx)
	if err != nil {
		slog.Warn("Leader election: cannot read the lease to release it", "identity", e.cfg.Identity, "error", err)
		return releaseFailed
	}
	if rec.HolderIdentity != e.cfg.Identity {
		return releaseNotHolder
	}
	now := metav1.NewTime(time.Now())
	err = lock.Update(ctx, resourcelock.LeaderElectionRecord{
		LeaderTransitions:    rec.LeaderTransitions,
		LeaseDurationSeconds: 1,
		RenewTime:            now,
		AcquireTime:          now,
	})
	switch {
	case err == nil:
		slog.Info("Leader election: lease released", "identity", e.cfg.Identity)
		return releaseDone
	case apierrors.IsConflict(err):
		slog.Info("Leader election: lease not released, another replica already holds the lease", "identity", e.cfg.Identity)
		return releaseConflict
	default:
		slog.Warn("Leader election: lease release failed; another replica takes over after the lease duration",
			"identity", e.cfg.Identity, "error", err)
		return releaseFailed
	}
}

// checkLeaseAccess reads the lease once and logs a warning when the service
// account may not use leases (for example serviceAccount.create=false in the
// Helm chart without a Role for leases).
func (e *Elector) checkLeaseAccess(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.RenewDeadline)
	defer cancel()
	_, err := e.client.CoordinationV1().Leases(e.cfg.Namespace).Get(ctx, e.cfg.LeaseName, metav1.GetOptions{})
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		slog.Warn("Leader election: the service account may not read the lease; no replica runs the background workers until it gets get, create and update on leases",
			"lease", e.cfg.LeaseName, "namespace", e.cfg.Namespace, "error", err)
	}
}

// watchRenewals ends the term when the lease was not renewed for the renew
// deadline.
//
// client-go cancels the leader context only after its renew attempt timed
// out (up to retry period + renew deadline after the last renewal). The
// watchdog stops the workers earlier, at most renew deadline + retry period
// / 2 after the last renewal. With the default timings this leaves a margin
// before the lease duration ends and another replica can take the lease.
func (e *Elector) watchRenewals(ctx context.Context, lock *renewTrackingLock, endTerm func()) {
	interval := e.cfg.RetryPeriod / 2
	if interval <= 0 {
		interval = e.cfg.RetryPeriod
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if since := lock.sinceRenew(); since > e.cfg.RenewDeadline {
				slog.Warn("Leader election: lease not renewed within the renew deadline",
					"identity", e.cfg.Identity, "since_last_renew", since)
				endTerm()
				return
			}
		}
	}
}

// renewTrackingLock records the time of the last successful acquire or
// renewal of the lease by this candidate. It uses the monotonic clock.
type renewTrackingLock struct {
	resourcelock.Interface
	base      time.Time    // monotonic reference
	lastRenew atomic.Int64 // nanoseconds since base
}

func newRenewTrackingLock(l resourcelock.Interface) *renewTrackingLock {
	return &renewTrackingLock{Interface: l, base: time.Now()}
}

// Create creates the lease record and records the renewal time.
func (l *renewTrackingLock) Create(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	err := l.Interface.Create(ctx, ler)
	l.record(err, ler)
	return err
}

// Update updates the lease record and records the renewal time.
func (l *renewTrackingLock) Update(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	err := l.Interface.Update(ctx, ler)
	l.record(err, ler)
	return err
}

func (l *renewTrackingLock) record(err error, ler resourcelock.LeaderElectionRecord) {
	if err == nil && ler.HolderIdentity == l.Identity() {
		l.lastRenew.Store(int64(time.Since(l.base)))
	}
}

// sinceRenew returns the time since the last successful renewal.
func (l *renewTrackingLock) sinceRenew() time.Duration {
	return time.Since(l.base) - time.Duration(l.lastRenew.Load())
}

// termGuard closes the race between client-go's OnStartedLeading goroutine
// and the return of LeaderElector.Run.
type termGuard struct {
	done    chan struct{}
	mu      sync.Mutex
	started bool
	closed  bool
	stopped bool // set by the term before done is closed
}

// begin reports whether the term may run. It returns false after wait.
func (g *termGuard) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.started = true
	return true
}

// wait blocks until a started term is over and stops later terms. It
// reports whether a term ran and whether its workers stopped in time.
func (g *termGuard) wait() (started, stopped bool) {
	g.mu.Lock()
	g.closed = true
	started = g.started
	g.mu.Unlock()
	if started {
		<-g.done // stopped is written before done is closed
		stopped = g.stopped
	}
	return started, stopped
}
