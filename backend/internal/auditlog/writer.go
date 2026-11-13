// Package auditlog holds the audit log writer and the change-set helpers.
package auditlog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// WriterOptions configures a Writer.
type WriterOptions struct {
	// Out gets one JSON line per entry (AUDIT_LOG_STDOUT). Nil: no lines.
	Out io.Writer
}

// Writer wraps an AuditLogRepository. It counts failed writes and can copy
// each entry to a log stream for a SIEM (#789).
type Writer struct {
	next   models.AuditLogRepository
	failed metric.Int64Counter
	out    io.Writer
	mu     sync.Mutex
}

// NewWriter returns a Writer that stores the entries through next.
func NewWriter(next models.AuditLogRepository, opts WriterOptions) *Writer {
	return &Writer{next: next, failed: writeFailed, out: opts.Out}
}

// stdoutLine is the JSON line of one entry.
type stdoutLine struct {
	LogType string `json:"log_type"`
	*models.AuditLog
}

// Create writes the log line first, so that the line exists also when the
// database write fails.
func (w *Writer) Create(ctx context.Context, e *models.AuditLog) error {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if w.out != nil {
		line, err := json.Marshal(stdoutLine{LogType: "audit", AuditLog: e})
		if err == nil {
			w.mu.Lock()
			_, err = w.out.Write(append(line, '\n'))
			w.mu.Unlock()
		}
		if err != nil {
			slog.Error("failed to write audit log line", "action", e.Action, "error", err)
		}
	}
	err := w.next.Create(ctx, e)
	if err != nil && w.failed != nil {
		w.failed.Add(ctx, 1, metric.WithAttributes(attribute.String("action", e.Action)))
	}
	return err
}

// List passes through to the wrapped repository.
func (w *Writer) List(ctx context.Context, filters models.AuditLogFilters) (*models.AuditLogResult, error) {
	return w.next.List(ctx, filters)
}
