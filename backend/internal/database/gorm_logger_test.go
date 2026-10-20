package database

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNewGormLoggerTo_Trace(t *testing.T) {
	t.Parallel()

	const query = "SELECT * FROM `stack_instances` WHERE namespace = 'stack-x' LIMIT 1"
	errSQL := errors.New("Error 1146: Table 'app.missing' doesn't exist")

	tests := []struct {
		name       string
		debug      bool
		err        error
		elapsed    time.Duration
		wantLogged bool
		wantText   []string
		denyText   []string
	}{
		{name: "release: record not found is not logged", debug: false, err: gorm.ErrRecordNotFound, wantLogged: false},
		{name: "debug: record not found is not logged as an error", debug: true, err: gorm.ErrRecordNotFound, wantLogged: true, denyText: []string{"record not found"}},
		{name: "release: wrapped record not found is not logged", debug: false, err: errors.Join(gorm.ErrRecordNotFound), wantLogged: false},
		{name: "release: real SQL error is logged", debug: false, err: errSQL, wantLogged: true, wantText: []string{"Error 1146", "stack_instances"}},
		{name: "debug: real SQL error is logged", debug: true, err: errSQL, wantLogged: true, wantText: []string{"Error 1146", "stack-x"}},
		{name: "release: fast query is not logged", debug: false, wantLogged: false},
		{name: "debug: fast query is logged", debug: true, wantLogged: true, wantText: []string{"stack_instances"}},
		{name: "release: slow query is logged", debug: false, elapsed: time.Second, wantLogged: true, wantText: []string{"SLOW SQL", "stack_instances"}},
		{name: "release: query just under the threshold is not logged", debug: false, elapsed: gormSlowQueryThreshold / 2, wantLogged: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			l := newGormLoggerTo(&buf, tt.debug)

			begin := time.Now().Add(-tt.elapsed)
			l.Trace(context.Background(), begin, func() (string, int64) {
				return query, 0
			}, tt.err)

			out := buf.String()
			if !tt.wantLogged {
				assert.Empty(t, out)
				return
			}
			assert.NotEmpty(t, out)
			assert.Equal(t, 1, strings.Count(out, "gorm_logger_test.go:"), "one log entry")
			assert.NotContains(t, out, "\x1b[", "no ANSI color codes")
			for _, s := range tt.wantText {
				assert.Contains(t, out, s)
			}
			for _, s := range tt.denyText {
				assert.NotContains(t, out, s)
			}
		})
	}
}

func TestNewGormLoggerTo_Messages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		debug     bool
		wantInfo  bool
		wantWarn  bool
		wantError bool
	}{
		{name: "debug logs info, warn and error", debug: true, wantInfo: true, wantWarn: true, wantError: true},
		{name: "release logs warn and error only", debug: false, wantInfo: false, wantWarn: true, wantError: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			l := newGormLoggerTo(&buf, tt.debug)
			ctx := context.Background()

			l.Info(ctx, "info-message")
			l.Warn(ctx, "warn-message")
			l.Error(ctx, "error-message")

			out := buf.String()
			assert.Equal(t, tt.wantInfo, bytes.Contains(buf.Bytes(), []byte("info-message")))
			assert.Equal(t, tt.wantWarn, bytes.Contains(buf.Bytes(), []byte("warn-message")))
			assert.Equal(t, tt.wantError, bytes.Contains(buf.Bytes(), []byte("error-message")))
			assert.NotContains(t, out, "\x1b[", "no ANSI color codes")
		})
	}
}

func TestNewGormLogger_ReturnsLogger(t *testing.T) {
	t.Parallel()
	assert.NotNil(t, newGormLogger(false))
	assert.NotNil(t, newGormLogger(true))
}

// TestNewGormLoggerTo_WithDB runs real queries through GORM (SQLite in
// memory) with the release and the debug logger.
func TestNewGormLoggerTo_WithDB(t *testing.T) {
	t.Parallel()

	type row struct {
		ID   uint
		Name string
	}

	tests := []struct {
		name     string
		debug    bool
		run      func(db *gorm.DB) error
		wantErr  bool
		wantText []string
		denyText []string
	}{
		{
			name: "release: First without a row writes nothing",
			run: func(db *gorm.DB) error {
				var r row
				return db.Where("name = ?", "absent").First(&r).Error
			},
			wantErr: true,
		},
		{
			name: "release: SQL error is logged with placeholders, not values",
			run: func(db *gorm.DB) error {
				var r row
				return db.Table("missing_table").Where("name = ?", "secret-value").Find(&r).Error
			},
			wantErr:  true,
			wantText: []string{"missing_table", "name = ?"},
			denyText: []string{"secret-value"},
		},
		{
			name:  "debug: SQL error is logged with values",
			debug: true,
			run: func(db *gorm.DB) error {
				var r row
				return db.Table("missing_table").Where("name = ?", "debug-value").Find(&r).Error
			},
			wantErr:  true,
			wantText: []string{"missing_table", "debug-value"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{SkipDefaultTransaction: true})
			if !assert.NoError(t, err) {
				return
			}
			if !assert.NoError(t, db.AutoMigrate(&row{})) {
				return
			}

			var buf bytes.Buffer
			db = db.Session(&gorm.Session{Logger: newGormLoggerTo(&buf, tt.debug)})

			err = tt.run(db)
			assert.Equal(t, tt.wantErr, err != nil)

			out := buf.String()
			if !tt.debug && len(tt.wantText) == 0 {
				assert.Empty(t, out)
			}
			assert.NotContains(t, out, "\x1b[", "no ANSI color codes")
			assert.NotContains(t, out, "record not found")
			for _, s := range tt.wantText {
				assert.Contains(t, out, s)
			}
			for _, s := range tt.denyText {
				assert.NotContains(t, out, s)
			}
		})
	}
}
