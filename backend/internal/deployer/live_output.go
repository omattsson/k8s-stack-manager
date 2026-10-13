package deployer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"backend/internal/models"
)

// liveOutputFlushInterval is how often the streamed lines of the running
// operations go to deployment_logs.output. A page that loads the deploy log
// during the operation shows the lines up to the last write.
const liveOutputFlushInterval = 5 * time.Second

// liveOutputWriteTimeout limits one write of the live output.
const liveOutputWriteTimeout = 5 * time.Second

// liveOutput holds the streamed lines of one running operation.
type liveOutput struct {
	buf   strings.Builder
	full  bool // maxOutputLen reached; later lines are not kept
	dirty bool // changed since the last write
}

// liveOutputs holds the live output of the running operations of this
// replica, by deployment log ID. The zero value is ready to use.
type liveOutputs struct {
	mu       sync.Mutex
	byLog    map[string]*liveOutput
	flushing bool // the flusher goroutine runs
}

// appendLiveOutput adds a streamed line to the live output of a running
// operation and starts the flusher goroutine when it does not run. It does
// nothing when the deployment log repository cannot write the live output.
// The final write of the operation replaces the live output with the full
// output (truncated to maxOutputLen).
func (m *Manager) appendLiveOutput(logID, line string) {
	if logID == "" {
		return
	}
	if _, ok := m.logRepo.(models.DeploymentLogOutputWriter); !ok {
		return
	}

	m.liveOut.mu.Lock()
	defer m.liveOut.mu.Unlock()
	if m.liveOut.byLog == nil {
		m.liveOut.byLog = make(map[string]*liveOutput)
	}
	lo := m.liveOut.byLog[logID]
	if lo == nil {
		lo = &liveOutput{}
		m.liveOut.byLog[logID] = lo
	}
	if lo.full {
		return
	}
	if lo.buf.Len()+len(line)+1 > maxOutputLen {
		lo.full = true
		return
	}
	lo.buf.WriteString(line)
	lo.buf.WriteByte('\n')
	lo.dirty = true

	if !m.liveOut.flushing {
		m.liveOut.flushing = true
		go m.runLiveOutputFlusher()
	}
}

// dropLiveOutput removes the live output of an operation at its end. The
// operation writes its full output itself.
func (m *Manager) dropLiveOutput(logID string) {
	m.liveOut.mu.Lock()
	delete(m.liveOut.byLog, logID)
	m.liveOut.mu.Unlock()
}

// runLiveOutputFlusher writes the changed live outputs every
// liveOutputFlushInterval. It stops when no operation has a live output, and
// at shutdown. It is not part of the shutdown WaitGroup: a write that fails
// (for example while the database is down) must not block the shutdown.
func (m *Manager) runLiveOutputFlusher() {
	interval := m.liveOutputInterval
	if interval <= 0 {
		interval = liveOutputFlushInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.shutdownCtx.Done():
			m.liveOut.mu.Lock()
			m.liveOut.flushing = false
			m.liveOut.mu.Unlock()
			return
		case <-ticker.C:
		}
		if !m.flushLiveOutputs() {
			return
		}
	}
}

// flushLiveOutputs writes each changed live output to its deployment log,
// only while the log is running. A log that is no longer running loses its
// live output. It returns false (and marks the flusher as stopped) when no
// live output is left.
func (m *Manager) flushLiveOutputs() bool {
	writer, ok := m.logRepo.(models.DeploymentLogOutputWriter)
	if !ok {
		return false
	}

	type pending struct {
		logID  string
		output string
	}
	var todo []pending
	m.liveOut.mu.Lock()
	for id, lo := range m.liveOut.byLog {
		if lo.dirty {
			todo = append(todo, pending{logID: id, output: lo.buf.String()})
			lo.dirty = false
		}
	}
	m.liveOut.mu.Unlock()

	for _, p := range todo {
		ctx, cancel := context.WithTimeout(m.shutdownCtx, liveOutputWriteTimeout)
		err := writer.UpdateRunningOutput(ctx, p.logID, p.output)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, models.ErrDeployLogNotRunning):
			m.dropLiveOutput(p.logID)
		default:
			slog.Warn("Failed to write the live deploy output", "log_id", p.logID, "error", err)
			m.liveOut.mu.Lock()
			if lo := m.liveOut.byLog[p.logID]; lo != nil {
				lo.dirty = true
			}
			m.liveOut.mu.Unlock()
		}
	}

	m.liveOut.mu.Lock()
	defer m.liveOut.mu.Unlock()
	if len(m.liveOut.byLog) == 0 {
		m.liveOut.flushing = false
		return false
	}
	return true
}
