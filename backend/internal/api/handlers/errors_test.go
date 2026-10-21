package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
)

func TestMapError_RequestContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "cancelled", err: context.Canceled, wantCode: statusClientClosedRequest, wantMsg: msgClientClosedRequest},
		{
			name: "cancelled in a database error", err: dberrors.NewDatabaseError("find_by_prefix", context.Canceled),
			wantCode: statusClientClosedRequest, wantMsg: msgClientClosedRequest,
		},
		{
			name: "cancelled before the string fallbacks", err: fmt.Errorf("record not found: %w", context.Canceled),
			wantCode: statusClientClosedRequest, wantMsg: msgClientClosedRequest,
		},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantCode: http.StatusInternalServerError, wantMsg: msgInternalServerError},
		{
			name: "deadline exceeded in a database error", err: dberrors.NewDatabaseError("update_last_used", context.DeadlineExceeded),
			wantCode: http.StatusInternalServerError, wantMsg: msgInternalServerError,
		},
		{name: "other error", err: errors.New("connection refused"), wantCode: http.StatusInternalServerError, wantMsg: msgInternalServerError},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, msg := mapError(tt.err, "API key")
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantMsg, msg)
		})
	}
}

// TestCancelledRequestError_LogLevel checks that a cancelled request logs one
// debug line and a passed deadline logs nothing in mapError and handleDBError.
// Not parallel: it replaces the default slog logger.
func TestCancelledRequestError_LogLevel(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	mappers := map[string]func(error) (int, string){
		"mapError":      func(err error) (int, string) { return mapError(err, "API key") },
		"handleDBError": handleDBError,
	}
	tests := []struct {
		name    string
		err     error
		wantLog bool
	}{
		{name: "cancelled", err: dberrors.NewDatabaseError("list_by_user", context.Canceled), wantLog: true},
		{name: "deadline exceeded", err: dberrors.NewDatabaseError("list_by_user", context.DeadlineExceeded)},
	}

	for _, tt := range tests {
		for mapperName, mapper := range mappers {
			t.Run(tt.name+"/"+mapperName, func(t *testing.T) {
				buf.Reset()
				mapper(tt.err)
				if tt.wantLog {
					assert.Contains(t, buf.String(), `level=DEBUG msg="Request cancelled by the client"`)
					assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "one log line")
				} else {
					assert.Empty(t, buf.String())
				}
			})
		}
	}
}
