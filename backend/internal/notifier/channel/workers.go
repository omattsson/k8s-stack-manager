package channel

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/models"

	"github.com/google/uuid"
)

const (
	// channelQueueSize is the number of events that wait for one channel.
	channelQueueSize = 100
	// workerIdleTimeout ends the worker of a channel without events.
	workerIdleTimeout = 10 * time.Minute
	// deliveryLogTimeout limits the delivery log write, also after Stop.
	deliveryLogTimeout = 5 * time.Second
)

// channelJob is one event for one channel. A job without a body records a
// failed delivery with errMsg and sends nothing.
type channelJob struct {
	body      []byte
	eventType string
	errMsg    string
	channel   models.NotificationChannel
}

type channelWorker struct {
	jobs chan channelJob
}

// workerPool runs one worker per channel ID, so a slow channel does not
// delay the others (#649). The zero value is ready to use.
type workerPool struct {
	ctx     context.Context
	cancel  context.CancelFunc
	workers map[string]*channelWorker
	// queueSize and idleTimeout: 0 means the defaults; tests set them.
	queueSize   int
	idleTimeout time.Duration
	running     atomic.Int64
	wg          sync.WaitGroup
	// mu guards workers, stopped and the idle exit, so a job is never
	// queued to a worker that has decided to exit.
	mu       sync.Mutex
	stopOnce sync.Once
	stopped  bool
}

func (p *workerPool) initLocked() {
	if p.workers != nil {
		return
	}
	p.workers = make(map[string]*channelWorker)
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if p.queueSize <= 0 {
		p.queueSize = channelQueueSize
	}
	if p.idleTimeout <= 0 {
		p.idleTimeout = workerIdleTimeout
	}
}

// workerCount returns the number of channel workers in the pool.
func (p *workerPool) workerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

// submit queues job for the worker of its channel and starts the worker when
// none runs. It never blocks: a full queue drops the job.
func (d *Dispatcher) submit(job channelJob) {
	p := &d.workers
	id := job.channel.ID
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		d.RecordDrop(job.eventType, id, DropReasonShutdown)
		return
	}
	p.initLocked()
	w := p.workers[id]
	if w == nil {
		w = &channelWorker{jobs: make(chan channelJob, p.queueSize)}
		p.workers[id] = w
		p.wg.Add(1)
		p.running.Add(1)
		go d.runWorker(id, w)
	}
	select {
	case w.jobs <- job:
		p.mu.Unlock()
	default:
		p.mu.Unlock()
		d.RecordDrop(job.eventType, id, DropReasonQueueFull)
	}
}

// runWorker delivers the jobs of one channel in order. It returns after
// idleTimeout without a job, or when Stop cancels the pool.
func (d *Dispatcher) runWorker(id string, w *channelWorker) {
	p := &d.workers
	defer p.wg.Done()
	defer p.running.Add(-1)

	idle := time.NewTimer(p.idleTimeout)
	defer idle.Stop()
	for {
		if p.ctx.Err() != nil {
			d.discard(id, w)
			return
		}
		select {
		case <-p.ctx.Done():
			d.discard(id, w)
			return
		case job := <-w.jobs:
			d.runJob(p.ctx, job)
			idle.Reset(p.idleTimeout)
		case <-idle.C:
			if p.exitIfIdle(id, w) {
				return
			}
			idle.Reset(p.idleTimeout)
		}
	}
}

// exitIfIdle removes w from the pool when its queue is empty. It holds the
// lock of submit, so no job can arrive between the check and the removal.
func (p *workerPool) exitIfIdle(id string, w *channelWorker) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(w.jobs) > 0 {
		return false
	}
	if p.workers[id] == w {
		delete(p.workers, id)
	}
	return true
}

// discard removes w from the pool and counts the jobs that are still queued
// at Stop. Stop has set stopped, so no new job arrives.
func (d *Dispatcher) discard(id string, w *channelWorker) {
	p := &d.workers
	p.mu.Lock()
	if p.workers[id] == w {
		delete(p.workers, id)
	}
	p.mu.Unlock()
	for {
		select {
		case job := <-w.jobs:
			d.RecordDrop(job.eventType, job.channel.ID, DropReasonShutdown)
		default:
			return
		}
	}
}

// runJob delivers one job and writes its delivery log.
func (d *Dispatcher) runJob(ctx context.Context, job channelJob) {
	ch := &job.channel
	status, statusCode, errMsg := "failed", 0, job.errMsg
	if job.body != nil {
		status, statusCode, errMsg = d.deliver(ctx, ch, job.eventType, job.body)
	}

	// The row of a request that Stop cancelled is written too.
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryLogTimeout)
	defer cancel()
	if logErr := d.repo.CreateDeliveryLog(logCtx, &models.NotificationDeliveryLog{
		ID:           uuid.New().String(),
		ChannelID:    ch.ID,
		ChannelName:  ch.Name,
		EventType:    job.eventType,
		Status:       status,
		StatusCode:   statusCode,
		ErrorMessage: errMsg,
		CreatedAt:    time.Now().UTC(),
	}); logErr != nil {
		slog.Error("notification channel: failed to create delivery log",
			"channel", ch.Name, "event", job.eventType, "error", logErr)
	}
}

// Stop cancels the deliveries in progress, drops the queued events (counted
// with reason shutdown) and waits for all channel workers. It then logs the
// drops that no warning has reported yet. Later events are dropped. Safe to
// call more than once.
func (d *Dispatcher) Stop() {
	p := &d.workers
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopped = true
		if p.cancel != nil {
			p.cancel()
		}
		p.mu.Unlock()
		p.wg.Wait()
		d.drops.flush()
	})
}

// RecordDrop counts one event that a channel did not get: the metric
// notification.channel.dropped, and at most one warning per event type,
// channel and reason per minute. channelID is empty for the intake queue of
// the notifier.
func (d *Dispatcher) RecordDrop(eventType, channelID, reason string) {
	d.drops.record(eventType, channelID, reason)
}
