package channel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/ctxutil"
	"backend/internal/models"
	"backend/pkg/dberrors"

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

// channelJob is one event for one channel. body is encoded in format; a job
// without a body records a failed delivery with errMsg and sends nothing.
// channel is the copy of Dispatch, used only when the new read fails.
type channelJob struct {
	body      []byte
	eventType string
	format    string
	errMsg    string
	payload   EventPayload
	channel   models.NotificationChannel
}

type channelWorker struct {
	jobs chan channelJob
}

// workerPool runs one worker per channel ID, so a slow channel does not
// delay the others (#649). The zero value is ready to use.
type workerPool struct {
	ctx    context.Context
	cancel context.CancelFunc
	// drain is closed by Shutdown: each worker sends its queued jobs, then
	// returns.
	drain   chan struct{}
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
	p.drain = make(chan struct{})
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

// remove deletes w from the pool when it is still the worker of id.
func (p *workerPool) remove(id string, w *channelWorker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.workers[id] == w {
		delete(p.workers, id)
	}
}

// submit queues job for the worker of its channel and starts the worker when
// none runs. It never blocks: a full queue drops the job.
func (d *Dispatcher) submit(job channelJob) {
	p := &d.workers
	id := job.channel.ID
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		d.recordDrop(job.eventType, id, job.channel.Name, DropReasonShutdown)
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
		d.recordDrop(job.eventType, id, job.channel.Name, DropReasonQueueFull)
	}
}

// runWorker delivers the jobs of one channel in order. It returns after
// idleTimeout without a job, after the drain of Shutdown, or when the pool
// is cancelled.
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
		case <-p.drain:
			d.drainQueue(id, w)
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

// drainQueue sends the queued jobs until the queue is empty or the pool is
// cancelled. Shutdown has set stopped, so no new job arrives.
func (d *Dispatcher) drainQueue(id string, w *channelWorker) {
	p := &d.workers
	for p.ctx.Err() == nil {
		select {
		case job := <-w.jobs:
			d.runJob(p.ctx, job)
		default:
			p.remove(id, w)
			return
		}
	}
	d.discard(id, w)
}

// discard removes w from the pool and counts the jobs that are still queued
// when the pool is cancelled.
func (d *Dispatcher) discard(id string, w *channelWorker) {
	d.workers.remove(id, w)
	for {
		select {
		case job := <-w.jobs:
			d.recordDrop(job.eventType, id, job.channel.Name, DropReasonShutdown)
		default:
			return
		}
	}
}

// runJob delivers one job and writes its delivery log.
func (d *Dispatcher) runJob(ctx context.Context, job channelJob) {
	ch, ok := d.currentChannel(ctx, job)
	if !ok {
		return
	}
	body, errMsg := job.body, job.errMsg
	if format := bodyFormat(ch); format != job.format {
		var err error
		body, err = encodeBody(format, job.payload)
		errMsg = ""
		if err != nil {
			slog.Error("notification channel: failed to marshal payload", "format", format, "error", err)
			body, errMsg = nil, "failed to encode the payload"
		}
	}

	status, statusCode := "failed", 0
	if msg := ch.UnreadableMessage(); msg != "" {
		// The channel read (or the queued copy) has a value that cannot be
		// decrypted: deliver sends nothing and gives the fixed message.
		status, statusCode, errMsg = d.deliver(ctx, ch, job.eventType, nil)
	} else if body != nil {
		status, statusCode, errMsg = d.deliver(ctx, ch, job.eventType, body)
	}

	// The row of a request that Stop cancelled is written too.
	logCtx, cancel := ctxutil.Detach(ctx, deliveryLogTimeout)
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

// currentChannel reads the channel of job again: a channel deleted or
// disabled on any replica gets no queued event, and a new URL, secret or
// format applies (#649). On a lookup error it returns the queued copy.
func (d *Dispatcher) currentChannel(ctx context.Context, job channelJob) (*models.NotificationChannel, bool) {
	ch, err := d.repo.GetChannel(ctx, job.channel.ID)
	switch {
	case err == nil && ch != nil && ch.Enabled:
		return ch, true
	case err == nil || errors.Is(err, dberrors.ErrNotFound):
		d.recordDrop(job.eventType, job.channel.ID, job.channel.Name, DropReasonChannelRemoved)
		return nil, false
	case ctx.Err() != nil:
		d.recordDrop(job.eventType, job.channel.ID, job.channel.Name, DropReasonShutdown)
		return nil, false
	default:
		d.lookups.add(job.channel.ID, msgLookupFailed,
			"channel", job.channel.Name, "channel_id", job.channel.ID, "error", err)
		return &job.channel, true
	}
}

// Stop cancels the deliveries in progress, drops the queued events (counted
// with reason shutdown) and waits for all channel workers. Same as
// Shutdown(0).
func (d *Dispatcher) Stop() {
	d.Shutdown(0)
}

// Shutdown stops the dispatcher. New events are dropped (reason shutdown).
// For at most grace, the workers send their queued events; then Shutdown
// cancels the deliveries in progress, drops the rest and waits for all
// workers. At the end it logs the counts that no warning has reported yet.
// Safe to call more than once.
func (d *Dispatcher) Shutdown(grace time.Duration) {
	p := &d.workers
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopped = true
		if p.drain != nil {
			close(p.drain)
		}
		p.mu.Unlock()

		if grace > 0 {
			drained := make(chan struct{})
			go func() {
				p.wg.Wait()
				close(drained)
			}()
			timer := time.NewTimer(grace)
			select {
			case <-drained:
			case <-timer.C:
			}
			timer.Stop()
			defer func() { <-drained }()
		}

		p.mu.Lock()
		if p.cancel != nil {
			p.cancel()
		}
		p.mu.Unlock()
		p.wg.Wait()
		d.drops.flush()
		d.failures.flush()
		d.lookups.flush()
	})
}
