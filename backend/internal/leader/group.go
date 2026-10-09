package leader

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultStopTimeout is how long Group.Stop waits for the workers.
const DefaultStopTimeout = 30 * time.Second

// Worker is a leader-only background job. Run must block until ctx is done
// and then return. Run is called once per leadership term, so it must work
// when it is called again after it returned (start, stop, start again).
type Worker struct {
	Run  func(ctx context.Context)
	Name string
}

// Group runs a set of workers for one leadership term at a time. Use
// Group.Start and Group.Stop as the callbacks of Elector.Run.
type Group struct {
	cancel      context.CancelFunc
	done        chan struct{} // closed when the current term's workers returned
	prevDone    chan struct{} // done channel of the last stopped term
	workers     []Worker
	stopTimeout time.Duration
	mu          sync.Mutex
	closed      bool
}

// NewGroup creates a Group. A stopTimeout <= 0 uses DefaultStopTimeout.
func NewGroup(stopTimeout time.Duration, workers ...Worker) *Group {
	if stopTimeout <= 0 {
		stopTimeout = DefaultStopTimeout
	}
	return &Group{workers: workers, stopTimeout: stopTimeout}
}

// Add adds a worker. The worker starts with the next term.
func (g *Group) Add(w Worker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.workers = append(g.workers, w)
}

// Names returns the worker names in start order.
func (g *Group) Names() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	names := make([]string, 0, len(g.workers))
	for _, w := range g.workers {
		names = append(names, w.Name)
	}
	return names
}

// Running reports whether a term is active.
func (g *Group) Running() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancel != nil
}

// Start starts all workers with a context derived from ctx and returns at
// once. It does nothing when a term is already active or after Close. When
// the workers of the previous term did not stop in time, the new workers
// start only after the old workers returned.
func (g *Group) Start(ctx context.Context) {
	g.mu.Lock()
	if g.closed || g.cancel != nil {
		g.mu.Unlock()
		return
	}
	termCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prev := g.prevDone
	workers := append([]Worker(nil), g.workers...)
	g.cancel, g.done = cancel, done
	g.mu.Unlock()

	go func() {
		defer close(done)
		if prev != nil {
			select {
			case <-prev:
			case <-termCtx.Done():
				// This term ends before it started its workers. done
				// must still close only after the previous workers
				// returned: the next term waits on done.
				<-prev
				return
			}
		}
		var wg sync.WaitGroup
		for _, w := range workers {
			wg.Add(1)
			go func(w Worker) {
				defer wg.Done()
				w.Run(termCtx)
			}(w)
		}
		slog.Info("Leader workers started", "workers", len(workers))
		wg.Wait()
	}()
}

// Stop cancels the workers and waits for them up to the stop timeout. It
// returns false when the workers did not stop in time.
func (g *Group) Stop() bool {
	return g.stop(g.stopTimeout)
}

// Close stops the workers (waiting up to timeout) and makes later Start
// calls do nothing. Use it on shutdown, before the lease is released, so
// that the next leader does not start while these workers still run.
func (g *Group) Close(timeout time.Duration) bool {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	if timeout <= 0 {
		timeout = g.stopTimeout
	}
	return g.stop(timeout)
}

func (g *Group) stop(timeout time.Duration) bool {
	g.mu.Lock()
	cancel, done := g.cancel, g.done
	g.cancel, g.done = nil, nil
	if done != nil {
		g.prevDone = done
	}
	g.mu.Unlock()

	if cancel == nil {
		return true
	}
	cancel()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		slog.Error("Leader workers did not stop in time; the next term waits for them", "timeout", timeout)
		return false
	}
}
