package websocket

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/models"
)

// Fan-out defaults. FanoutConfig fields <= 0 use these values.
const (
	// DefaultFanoutPollInterval is the time between two polls of ws_events.
	DefaultFanoutPollInterval = 500 * time.Millisecond
	// DefaultFanoutRetention is how long ws_events rows stay in the table.
	DefaultFanoutRetention = 5 * time.Minute
	// DefaultFanoutCleanupInterval is the time between two cleanups.
	DefaultFanoutCleanupInterval = time.Minute

	defaultFanoutPollBatch    = 500
	defaultFanoutBufferSize   = 4096
	defaultFanoutWriteBatch   = 200
	defaultFanoutMaxPayload   = 512 * 1024
	defaultFanoutGapTimeout   = 10 * time.Second
	defaultFanoutMaxAge       = 30 * time.Second
	defaultFanoutQueryTimeout = 5 * time.Second
	defaultFanoutFlushTimeout = 2 * time.Second
	defaultFanoutLogEvery     = 10 * time.Second
	fanoutWriteBatchBytes     = 1 << 20 // stop filling a write batch at 1 MiB of payload
	fanoutMaxPagesPerPoll     = 20      // full pages read in one poll before the next tick
	fanoutMaxGapSpan          = 1000    // larger ID gaps are not tracked
	fanoutMaxMissing          = 10000   // upper bound of tracked missing IDs
	fanoutIDsPerRecheck       = 500     // IDs per ListByIDs query
	fanoutCleanupTimeout      = 30 * time.Second
	fanoutCleanupBatch        = 5000 // rows per DELETE statement
	fanoutRestartCheckEvery   = 20   // empty polls between two ID restart checks
	// fanoutStartReplicas is the number of other replicas the start window
	// covers. Each replica has one writer goroutine with at most one
	// uncommitted batch of WriteBatch rows, so at most WriteBatch x (writers)
	// IDs below MAX(id) can still be uncommitted when the poller starts.
	fanoutStartReplicas         = 5
	fanoutDropReasonBufferFull  = "buffer_full"
	fanoutDropReasonTooLarge    = "too_large"
	fanoutDropReasonWriteFailed = "write_error"
	fanoutSkipReasonStale       = "stale"
	fanoutSkipReasonUnknown     = "unknown_target"
)

// FanoutConfig configures a Fanout. Zero values use the defaults.
type FanoutConfig struct {
	// Origin identifies this process in ws_events: the replica identity
	// (POD_NAME or host name) plus a random suffix per process. Required.
	Origin string
	// PollInterval is the time between two polls (default 500 ms).
	PollInterval time.Duration
	// GapTimeout is how long a poller waits for a missing ID to become
	// visible (default 10 s). See Fanout.
	GapTimeout time.Duration
	// MaxAge is the age (created_at) above which a row of another replica
	// is not delivered (default 30 s). After a database outage or a slow
	// poll, old live updates are skipped instead of replayed.
	MaxAge time.Duration
	// PollBatch is the row limit of one poll query (default 500).
	PollBatch int
	// BufferSize is the capacity of the write queue (default 4096).
	BufferSize int
	// WriteBatch is the maximum number of rows in one INSERT (default 200).
	WriteBatch int
	// MaxPayloadBytes is the largest message that is written (default
	// 512 KiB). Larger messages reach only the local clients.
	MaxPayloadBytes int
}

func (c FanoutConfig) withDefaults() FanoutConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultFanoutPollInterval
	}
	if c.GapTimeout <= 0 {
		c.GapTimeout = defaultFanoutGapTimeout
	}
	if c.MaxAge <= 0 {
		c.MaxAge = defaultFanoutMaxAge
	}
	if c.PollBatch <= 0 {
		c.PollBatch = defaultFanoutPollBatch
	}
	if c.BufferSize <= 0 {
		c.BufferSize = defaultFanoutBufferSize
	}
	if c.WriteBatch <= 0 {
		c.WriteBatch = defaultFanoutWriteBatch
	}
	if c.MaxPayloadBytes <= 0 {
		c.MaxPayloadBytes = defaultFanoutMaxPayload
	}
	return c
}

// FanoutStats counts the fan-out events of one Fanout (tests, debugging).
// The same values go to the OpenTelemetry counters.
type FanoutStats struct {
	Written   int64 // rows inserted
	Dropped   int64 // messages not written (buffer full, too large, write error)
	Delivered int64 // rows from other replicas delivered to local clients
	Skipped   int64 // rows from other replicas not delivered (stale, unknown target)
}

// Fanout shares the WebSocket messages and socket revocations of a hub with
// the hubs of the other replicas through the ws_events table.
//
// Write path: each hub send (Broadcast, BroadcastToInstance,
// BroadcastToUser) and each revocation (DisconnectUser, DisconnectToken)
// acts on the local clients at once and queues one row. A writer goroutine
// inserts the queued rows in batches. The queue is bounded: when it is full,
// the message is not written (counter + rate limited warning), and the hub
// call never blocks.
//
// Read path: a poller reads the rows with id > last ID every PollInterval
// and delivers the rows of other origins to the local clients. It skips the
// rows of its own origin (their clients got the message at once) and rows
// older than MaxAge. A revocation row closes the local sockets only and
// writes no new row, so revocations do not loop. At start the last ID is
// MAX(id): the poller does not replay history.
//
// Ordering: MySQL assigns auto-increment IDs at insert time, but the rows
// become visible at commit time. A row of replica A with a lower ID can
// become visible after a row of replica B with a higher ID was read. The
// poller records each ID it skipped over (a gap) for GapTimeout and reads
// these IDs again on each poll. A late row is delivered when it appears; a
// gap of a rolled-back insert expires. At start, the IDs below MAX(id) that
// are not visible yet are gaps too (see trackStartWindow). One writer
// goroutine per replica inserts its batches one after the other, so the rows
// of one origin stay in order; only rows of different origins can arrive
// out of order.
type Fanout struct {
	hub  *Hub
	repo models.WSEventRepository
	cfg  FanoutConfig

	queue   chan *models.WSEvent
	stopped atomic.Bool

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// Poller state. Only the poller goroutine (or a test that calls poll
	// directly) uses these fields.
	lastID  int64
	started bool
	missing map[int64]time.Time // missing ID -> deadline
	// emptyPolls counts empty polls; every fanoutRestartCheckEvery-th one
	// checks for an ID restart.
	emptyPolls int
	// ready is closed when the poller has read its start position.
	ready chan struct{}

	written   atomic.Int64
	dropped   atomic.Int64
	delivered atomic.Int64
	skipped   atomic.Int64

	dropLog  logLimiter
	sizeLog  logLimiter
	writeLog logLimiter
	pollLog  logLimiter
	gapLog   logLimiter

	now func() time.Time
}

// NewFanout creates a Fanout and attaches it to the hub: from now on, the
// hub sends queue rows. Call Start to run the writer and the poller, and Stop
// at shutdown. A nil hub, a nil repo or an empty origin returns nil.
func NewFanout(hub *Hub, repo models.WSEventRepository, cfg FanoutConfig) *Fanout {
	if hub == nil || repo == nil || cfg.Origin == "" {
		return nil
	}
	cfg = cfg.withDefaults()
	f := &Fanout{
		hub:      hub,
		repo:     repo,
		cfg:      cfg,
		queue:    make(chan *models.WSEvent, cfg.BufferSize),
		missing:  make(map[int64]time.Time),
		ready:    make(chan struct{}),
		dropLog:  logLimiter{every: defaultFanoutLogEvery},
		sizeLog:  logLimiter{every: defaultFanoutLogEvery},
		writeLog: logLimiter{every: defaultFanoutLogEvery},
		pollLog:  logLimiter{every: defaultFanoutLogEvery},
		gapLog:   logLimiter{every: defaultFanoutLogEvery},
		now:      time.Now,
	}
	hub.fanout.Store(f)
	return f
}

// Origin returns the replica identity of the Fanout.
func (f *Fanout) Origin() string { return f.cfg.Origin }

// Stats returns the counters of this Fanout.
func (f *Fanout) Stats() FanoutStats {
	return FanoutStats{Written: f.written.Load(), Dropped: f.dropped.Load(), Delivered: f.delivered.Load(), Skipped: f.skipped.Load()}
}

// Start runs the writer and the poller goroutines. Only the first call
// starts them.
func (f *Fanout) Start() {
	if f == nil {
		return
	}
	f.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		f.wg.Add(2)
		go f.writeLoop(ctx)
		go f.pollLoop(ctx)
		slog.Info("WebSocket fan-out started", "origin", f.cfg.Origin, "poll_interval", f.cfg.PollInterval)
	})
}

// Stop detaches the Fanout from the hub, stops the poller, writes the
// queued rows (bounded by a short timeout) and waits for the goroutines.
// Call it before the hub shutdown and before the database closes. It is
// safe to call more than once.
func (f *Fanout) Stop() {
	if f == nil {
		return
	}
	f.stopOnce.Do(func() {
		f.stopped.Store(true)
		f.hub.fanout.CompareAndSwap(f, nil)
		if f.cancel != nil {
			f.cancel()
		}
		f.wg.Wait()
	})
}

// publish queues one row for the other replicas. It never blocks.
func (f *Fanout) publish(target string, message []byte) {
	if f.stopped.Load() {
		return
	}
	if len(message) > f.cfg.MaxPayloadBytes {
		f.drop(fanoutDropReasonTooLarge, 1)
		if ok, suppressed := f.sizeLog.allow(f.now()); ok {
			slog.Warn("WebSocket fan-out message too large, other replicas do not get it",
				"target", target, "bytes", len(message), "max_bytes", f.cfg.MaxPayloadBytes, "suppressed", suppressed)
		}
		return
	}
	ev := &models.WSEvent{
		CreatedAt: f.now().UTC(),
		Target:    target,
		Origin:    f.cfg.Origin,
		Payload:   string(message),
	}
	select {
	case f.queue <- ev:
	default:
		f.drop(fanoutDropReasonBufferFull, 1)
		if ok, suppressed := f.dropLog.allow(f.now()); ok {
			slog.Warn("WebSocket fan-out buffer full, message not shared with other replicas",
				"buffer", f.cfg.BufferSize, "suppressed", suppressed)
		}
	}
}

// skip counts a row of another replica that is not delivered.
func (f *Fanout) skip(reason string) {
	f.skipped.Add(1)
	fanoutMetrics.skipped(reason)
}

func (f *Fanout) drop(reason string, n int64) {
	f.dropped.Add(n)
	fanoutMetrics.dropped(reason, n)
}

// writeLoop inserts the queued rows in batches until ctx is done, then
// writes what is left in the queue.
func (f *Fanout) writeLoop(ctx context.Context) {
	defer f.wg.Done()
	batch := make([]*models.WSEvent, 0, f.cfg.WriteBatch)
	for {
		select {
		case <-ctx.Done():
			f.flush()
			return
		case ev := <-f.queue:
			batch = f.fillBatch(append(batch[:0], ev))
			f.write(batch)
		}
	}
}

// fillBatch adds queued rows to batch without blocking, up to WriteBatch
// rows or about 1 MiB of payload (MySQL max_allowed_packet).
func (f *Fanout) fillBatch(batch []*models.WSEvent) []*models.WSEvent {
	size := 0
	for _, ev := range batch {
		size += len(ev.Payload)
	}
	for len(batch) < f.cfg.WriteBatch && size < fanoutWriteBatchBytes {
		select {
		case ev := <-f.queue:
			batch = append(batch, ev)
			size += len(ev.Payload)
		default:
			return batch
		}
	}
	return batch
}

// flush writes the rows that are in the queue at shutdown, within
// defaultFanoutFlushTimeout.
func (f *Fanout) flush() {
	deadline := f.now().Add(defaultFanoutFlushTimeout)
	batch := make([]*models.WSEvent, 0, f.cfg.WriteBatch)
	for f.now().Before(deadline) {
		batch = f.fillBatch(batch[:0])
		if len(batch) == 0 {
			return
		}
		f.write(batch)
	}
	if n := len(f.queue); n > 0 {
		f.drop(fanoutDropReasonWriteFailed, int64(n))
		slog.Warn("WebSocket fan-out rows not written at shutdown", "rows", n)
	}
}

// write inserts one batch. A failed batch is dropped: the events are live
// updates, and a later retry would deliver them late and out of order.
func (f *Fanout) write(batch []*models.WSEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultFanoutQueryTimeout)
	defer cancel()
	if err := f.repo.Insert(ctx, batch); err != nil {
		f.drop(fanoutDropReasonWriteFailed, int64(len(batch)))
		if ok, suppressed := f.writeLog.allow(f.now()); ok {
			slog.Error("WebSocket fan-out write failed, rows dropped",
				"rows", len(batch), "error", err, "suppressed", suppressed)
		}
		return
	}
	f.written.Add(int64(len(batch)))
	fanoutMetrics.written.Add(context.Background(), int64(len(batch)))
}

// pollLoop polls at once and then every PollInterval until ctx is done.
func (f *Fanout) pollLoop(ctx context.Context) {
	defer f.wg.Done()
	f.poll(ctx)
	ticker := time.NewTicker(f.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.poll(ctx)
		}
	}
}

// poll reads the new rows and the missing IDs once. On a database error it
// logs (rate limited) and returns; the next tick tries again.
func (f *Fanout) poll(ctx context.Context) {
	if !f.started {
		qctx, cancel := context.WithTimeout(ctx, defaultFanoutQueryTimeout)
		maxID, err := f.repo.MaxID(qctx)
		cancel()
		if err != nil {
			f.pollFailed(err)
			return
		}
		if err := f.trackStartWindow(ctx, maxID); err != nil {
			f.pollFailed(err)
			return
		}
		f.lastID = maxID
		f.started = true
		close(f.ready)
	}

	f.recheckGaps(ctx)

	for page := 0; page < fanoutMaxPagesPerPoll && ctx.Err() == nil; page++ {
		qctx, cancel := context.WithTimeout(ctx, defaultFanoutQueryTimeout)
		rows, err := f.repo.ListAfter(qctx, f.lastID, f.cfg.PollBatch, f.cfg.Origin)
		cancel()
		if err != nil {
			f.pollFailed(err)
			return
		}
		if page == 0 && len(rows) == 0 {
			f.emptyPolls++
			if f.emptyPolls%fanoutRestartCheckEvery == 0 {
				f.checkIDRestart(ctx)
			}
			return
		}
		f.consume(rows)
		if len(rows) < f.cfg.PollBatch {
			return
		}
	}
}

// trackStartWindow records the IDs in (maxID - window, maxID] that are not
// visible at start as missing. A row of another replica can hold such an ID
// and commit just after the start (see fanoutStartReplicas); the gap
// handling then delivers it. Visible rows in the window are history and are
// not delivered.
func (f *Fanout) trackStartWindow(ctx context.Context, maxID int64) error {
	window := int64(f.cfg.WriteBatch) * fanoutStartReplicas
	from := max(maxID-window, 0)
	if maxID <= from {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, defaultFanoutQueryTimeout)
	ids, err := f.repo.IDsAfter(qctx, from, int(maxID-from))
	cancel()
	if err != nil {
		return err
	}
	visible := make(map[int64]bool, len(ids))
	for _, id := range ids {
		visible[id] = true
	}
	deadline := f.now().Add(f.cfg.GapTimeout)
	for id := from + 1; id <= maxID && len(f.missing) < fanoutMaxMissing; id++ {
		if !visible[id] {
			f.missing[id] = deadline
		}
	}
	return nil
}

// checkIDRestart runs on every fanoutRestartCheckEvery-th empty poll: when
// MAX(id) is above 0 but below the last ID, the IDs restarted (table
// recreated, or MySQL reset the auto-increment counter of an empty table at
// restart). The poller then reads from ID 0 again; the MaxAge filter drops
// the old rows. An empty table (MAX(id) = 0) after the cleanup changes
// nothing.
func (f *Fanout) checkIDRestart(ctx context.Context) {
	qctx, cancel := context.WithTimeout(ctx, defaultFanoutQueryTimeout)
	maxID, err := f.repo.MaxID(qctx)
	cancel()
	if err != nil {
		f.pollFailed(err)
		return
	}
	if maxID <= 0 || maxID >= f.lastID {
		return
	}
	slog.Warn("WebSocket fan-out IDs restarted, poller reads from the start again",
		"last_id", f.lastID, "max_id", maxID)
	f.lastID = 0
	clear(f.missing)
}

func (f *Fanout) pollFailed(err error) {
	if ok, suppressed := f.pollLog.allow(f.now()); ok {
		slog.Error("WebSocket fan-out poll failed, retry on next tick", "error", err, "suppressed", suppressed)
	}
	fanoutMetrics.pollErrors.Add(context.Background(), 1)
}

// consume delivers rows read in ID order and records the IDs it skipped
// over as missing.
func (f *Fanout) consume(rows []models.WSEvent) {
	for i := range rows {
		ev := &rows[i]
		if ev.ID <= f.lastID {
			continue
		}
		if ev.ID > f.lastID+1 {
			f.trackGap(f.lastID+1, ev.ID-1)
		}
		f.lastID = ev.ID
		f.deliver(ev)
	}
}

// trackGap records the IDs from..to as missing until GapTimeout.
func (f *Fanout) trackGap(from, to int64) {
	span := to - from + 1
	if span > fanoutMaxGapSpan {
		if ok, suppressed := f.gapLog.allow(f.now()); ok {
			slog.Warn("WebSocket fan-out ID gap too large to track", "from", from, "to", to, "suppressed", suppressed)
		}
		return
	}
	deadline := f.now().Add(f.cfg.GapTimeout)
	for id := from; id <= to; id++ {
		if len(f.missing) >= fanoutMaxMissing {
			if ok, suppressed := f.gapLog.allow(f.now()); ok {
				slog.Warn("WebSocket fan-out tracks too many missing IDs, gap not tracked",
					"from", id, "to", to, "suppressed", suppressed)
			}
			return
		}
		f.missing[id] = deadline
	}
}

// recheckGaps reads the missing IDs again, delivers the rows that became
// visible and forgets the IDs whose deadline passed.
func (f *Fanout) recheckGaps(ctx context.Context) {
	if len(f.missing) == 0 {
		return
	}
	ids := make([]int64, 0, len(f.missing))
	for id := range f.missing {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for start := 0; start < len(ids); start += fanoutIDsPerRecheck {
		end := min(start+fanoutIDsPerRecheck, len(ids))
		qctx, cancel := context.WithTimeout(ctx, defaultFanoutQueryTimeout)
		rows, err := f.repo.ListByIDs(qctx, ids[start:end], f.cfg.Origin)
		cancel()
		if err != nil {
			f.pollFailed(err)
			return
		}
		for i := range rows {
			if _, ok := f.missing[rows[i].ID]; !ok {
				continue
			}
			delete(f.missing, rows[i].ID)
			f.deliver(&rows[i])
		}
	}
	now := f.now()
	for id, deadline := range f.missing {
		if now.After(deadline) {
			delete(f.missing, id)
		}
	}
}

// deliver sends a row of another origin to the local clients.
func (f *Fanout) deliver(ev *models.WSEvent) {
	if ev.Origin == f.cfg.Origin {
		return
	}
	if !ev.CreatedAt.IsZero() && f.now().Sub(ev.CreatedAt) > f.cfg.MaxAge {
		f.skip(fanoutSkipReasonStale)
		return
	}
	msg := []byte(ev.Payload)
	switch {
	case strings.HasPrefix(ev.Target, models.WSEventTargetRevokeUserPrefix):
		f.hub.disconnectUserIssuedBefore(strings.TrimPrefix(ev.Target, models.WSEventTargetRevokeUserPrefix), ev.CreatedAt)
	case strings.HasPrefix(ev.Target, models.WSEventTargetRevokeTokenPrefix):
		f.hub.disconnectTokenLocal(strings.TrimPrefix(ev.Target, models.WSEventTargetRevokeTokenPrefix))
	case ev.Target == models.WSEventTargetAll:
		f.hub.deliverAll(msg)
	case strings.HasPrefix(ev.Target, models.WSEventTargetInstancePrefix):
		f.hub.deliverInstance(strings.TrimPrefix(ev.Target, models.WSEventTargetInstancePrefix), msg)
	case strings.HasPrefix(ev.Target, models.WSEventTargetUserPrefix):
		f.hub.deliverUser(strings.TrimPrefix(ev.Target, models.WSEventTargetUserPrefix), msg)
	default:
		slog.Debug("WebSocket fan-out row with unknown target skipped", "id", ev.ID, "target", ev.Target)
		f.skip(fanoutSkipReasonUnknown)
		return
	}
	f.delivered.Add(1)
	fanoutMetrics.delivered.Add(context.Background(), 1)
	if !ev.CreatedAt.IsZero() {
		fanoutMetrics.lag.Record(context.Background(), f.now().Sub(ev.CreatedAt).Seconds())
	}
}

// publish queues a row for the other replicas when fan-out is on.
func (h *Hub) publish(target string, message []byte) {
	if f := h.fanout.Load(); f != nil {
		f.publish(target, message)
	}
}

// EventCleaner deletes old ws_events rows. It is a leader-only worker.
type EventCleaner struct {
	repo      models.WSEventRepository
	retention time.Duration
	interval  time.Duration
	now       func() time.Time
}

// NewEventCleaner creates an EventCleaner. retention <= 0 uses
// DefaultFanoutRetention; interval <= 0 uses DefaultFanoutCleanupInterval.
func NewEventCleaner(repo models.WSEventRepository, retention, interval time.Duration) *EventCleaner {
	if retention <= 0 {
		retention = DefaultFanoutRetention
	}
	if interval <= 0 {
		interval = DefaultFanoutCleanupInterval
	}
	return &EventCleaner{repo: repo, retention: retention, interval: interval, now: time.Now}
}

// Run deletes old rows at once and then every interval until ctx is done.
// It can run again after it returned (leader worker).
func (c *EventCleaner) Run(ctx context.Context) {
	c.cleanup(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cleanup(ctx)
		}
	}
}

// cleanup deletes the rows older than the retention once.
func (c *EventCleaner) cleanup(ctx context.Context) {
	qctx, cancel := context.WithTimeout(ctx, fanoutCleanupTimeout)
	defer cancel()
	deleted, err := c.repo.DeleteOlderThan(qctx, c.now().UTC().Add(-c.retention), fanoutCleanupBatch)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("WebSocket fan-out cleanup failed", "error", err)
		}
		return
	}
	if deleted > 0 {
		slog.Debug("WebSocket fan-out cleanup", "deleted", deleted)
	}
}

// logLimiter allows one log line per interval and counts the suppressed
// lines in between.
type logLimiter struct {
	last       time.Time
	every      time.Duration
	suppressed int
	mu         sync.Mutex
}

// allow reports whether a line may be logged now, and how many lines were
// suppressed since the last logged line.
func (l *logLimiter) allow(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() && now.Sub(l.last) < l.every {
		l.suppressed++
		return false, 0
	}
	n := l.suppressed
	l.suppressed = 0
	l.last = now
	return true, n
}
