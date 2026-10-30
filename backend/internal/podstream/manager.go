// Package podstream owns the long-lived WebSocket sessions that tunnel to a
// pod (port-forward and exec): limits, the session registry, the copy
// pumps, deadlines, close codes and metrics. The HTTP handler runs the checks
// and the upgrade, then hands the connection to the Manager.
package podstream

import (
	"context"
	"errors"
	"sync"
	"time"
)

// DefaultProcessCap is the number of sessions that one replica accepts.
const DefaultProcessCap = 500

// Close codes of the stream protocol (the standard ones are used as numbers).
const (
	CloseNormal        = 1000
	CloseGoingAway     = 1001
	CloseUnsupported   = 1003
	ClosePolicy        = 1008
	CloseMessageTooBig = 1009
	CloseInternalError = 1011
	CloseMaxDuration   = 4001
	CloseIdleTimeout   = 4002
	CloseBadStart      = 4400 // exec: the first client message is not a valid start message
	CloseStartTimeout  = 4408 // exec: no start message in time
	ClosePodError      = 4502
	closeAbnormalOnly  = 1006 // never sent; the connection is gone
)

// Errors of the slot API.
var (
	// ErrLimitReached means the per-user limit or the process cap is reached
	// (HTTP 429).
	ErrLimitReached = errors.New("tunnel limit reached")
	// ErrShutdown means the manager is shutting down (HTTP 503).
	ErrShutdown = errors.New("stream manager is shutting down")
)

// Config configures a Manager. Zero values: Kind port_forward, MaxPerUser 5,
// ProcessCap 500, MaxDuration 1h, IdleTimeout 15m.
type Config struct {
	// Kind is the kind label of the sessions of this manager (KindPortForward
	// or KindExec). Each kind has its own Manager and so its own limits.
	Kind        string
	Timings     Timings
	MaxDuration time.Duration
	IdleTimeout time.Duration
	MaxPerUser  int
	ProcessCap  int
	// NoIdleTimeout turns the idle timeout off (exec has none).
	NoIdleTimeout bool
}

// Manager holds the limits and the registry of the open sessions of one
// replica.
type Manager struct {
	baseCtx    context.Context
	cancelBase context.CancelFunc
	perUser    map[string]int
	cfg        Config
	wg         sync.WaitGroup
	mu         sync.Mutex
	total      int
	closing    bool
}

// NewManager returns a Manager. Call Shutdown when the process stops.
func NewManager(cfg Config) *Manager {
	if cfg.Kind == "" {
		cfg.Kind = KindPortForward
	}
	if cfg.MaxPerUser <= 0 {
		cfg.MaxPerUser = 5
	}
	if cfg.ProcessCap <= 0 {
		cfg.ProcessCap = DefaultProcessCap
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = time.Hour
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 15 * time.Minute
	}
	cfg.Timings = cfg.Timings.withDefaults()
	// The base context is cancelled only by Shutdown; it is not tied to any
	// request.
	base, cancel := context.WithCancel(context.Background())
	return &Manager{baseCtx: base, cancelBase: cancel, perUser: map[string]int{}, cfg: cfg}
}

// Kind is the kind label of the sessions of this manager.
func (m *Manager) Kind() string { return m.cfg.Kind }

// MaxDuration is the longest life of a session.
func (m *Manager) MaxDuration() time.Duration { return m.cfg.MaxDuration }

// IdleTimeout is the idle timeout of a session.
func (m *Manager) IdleTimeout() time.Duration { return m.cfg.IdleTimeout }

// Slot is one reserved session of a user. Release gives it back; it is safe
// to call more than once.
type Slot struct {
	m      *Manager
	userID string
	once   sync.Once
}

// Acquire reserves a session for userID. It returns ErrLimitReached when the
// per-user limit or the process cap is reached, ErrShutdown after Shutdown.
// The caller must Release the slot, or hand it to a session.
func (m *Manager) Acquire(userID string) (*Slot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrShutdown
	}
	if m.total >= m.cfg.ProcessCap {
		RecordRejected(m.cfg.Kind, "process_cap")
		return nil, ErrLimitReached
	}
	if m.perUser[userID] >= m.cfg.MaxPerUser {
		RecordRejected(m.cfg.Kind, "user_limit")
		return nil, ErrLimitReached
	}
	m.total++
	m.perUser[userID]++
	return &Slot{m: m, userID: userID}, nil
}

// Release gives the slot back.
func (s *Slot) Release() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		m := s.m
		m.mu.Lock()
		defer m.mu.Unlock()
		m.total--
		if m.perUser[s.userID] <= 1 {
			delete(m.perUser, s.userID)
		} else {
			m.perUser[s.userID]--
		}
	})
}

// Active returns the number of reserved slots.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}

// track registers a session goroutine. It reports false after Shutdown.
func (m *Manager) track() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return false
	}
	m.wg.Add(1)
	return true
}

// Shutdown closes every session with 1001 and waits for the session
// goroutines or for ctx. The http.Server does not close hijacked connections:
// call it right after the server shutdown.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	m.cancelBase()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
