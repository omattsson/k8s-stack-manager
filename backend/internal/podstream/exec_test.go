package podstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type execHarness struct {
	m      *Manager
	client *gorilla.Conn
	hub    *websocket.Hub
	ended  chan ExecSummary
	ran    atomic.Int32
	audits atomic.Int32
	sess   atomic.Pointer[ExecSession]
}

type execHarnessOpts struct {
	cfg          Config
	run          ExecRunFunc
	beforeStart  func(context.Context, []string) error
	postRegister func(context.Context) bool
	startTimeout time.Duration
	maxLen       int
}

func startExecHarness(t *testing.T, o execHarnessOpts) *execHarness {
	t.Helper()
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	o.cfg.Kind = KindExec
	o.cfg.NoIdleTimeout = true
	if o.cfg.Timings == (Timings{}) {
		o.cfg.Timings = fast
	}
	h := &execHarness{m: NewManager(o.cfg), hub: hub, ended: make(chan ExecSummary, 1)}
	t.Cleanup(func() { _ = h.m.Shutdown(context.Background()) })
	if o.run == nil {
		o.run = func(context.Context, []string, io.Writer, io.Writer) (int, error) { return 0, nil }
	}

	up := gorilla.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		slot, err := h.m.Acquire("u1")
		if err != nil {
			_ = conn.Close()
			return
		}
		h.sess.Store(h.m.StartExec(ExecOptions{
			Conn: conn, Slot: slot, Hub: hub, PostRegister: o.postRegister,
			Identity: websocket.ClientIdentity{UserID: "u1"}, InstanceID: "inst1",
			Pod: "pod-1", Container: "web", MaxCommandLength: o.maxLen, StartTimeout: o.startTimeout,
			Run: func(ctx context.Context, cmd []string, so, se io.Writer) (int, error) {
				h.ran.Add(1)
				return o.run(ctx, cmd, so, se)
			},
			BeforeStart: func(ctx context.Context, cmd []string) error {
				h.audits.Add(1)
				if o.beforeStart != nil {
					return o.beforeStart(ctx, cmd)
				}
				return nil
			},
			OnEnd: func(s ExecSummary) { h.ended <- s },
		}))
		close(started)
	}))
	t.Cleanup(srv.Close)
	c, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	h.client = c
	t.Cleanup(func() { _ = c.Close() })
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not start")
	}
	return h
}

func (h *execHarness) sendStart(t *testing.T, cmd ...string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "start", "command": cmd})
	require.NoError(t, err)
	require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, b))
}

func (h *execHarness) waitEnd(t *testing.T) ExecSummary {
	t.Helper()
	select {
	case s := <-h.ended:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
		return ExecSummary{}
	}
}

type execFrame struct {
	text   map[string]any
	data   []byte
	stream byte
}

// readFrames reads until the close frame.
func readFrames(t *testing.T, c *gorilla.Conn) ([]execFrame, int, string) {
	t.Helper()
	var out []execFrame
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			var ce *gorilla.CloseError
			require.True(t, errors.As(err, &ce), "got %v", err)
			return out, ce.Code, ce.Text
		}
		switch mt {
		case gorilla.TextMessage:
			var m map[string]any
			require.NoError(t, json.Unmarshal(data, &m))
			out = append(out, execFrame{text: m})
		case gorilla.BinaryMessage:
			require.NotEmpty(t, data)
			out = append(out, execFrame{stream: data[0], data: append([]byte(nil), data[1:]...)})
		}
	}
}

func TestValidateExecCommand(t *testing.T) {
	t.Parallel()
	many := make([]string, MaxExecArgs+1)
	for i := range many {
		many[i] = "a"
	}
	tests := []struct {
		name    string
		cmd     []string
		maxLen  int
		wantErr string
	}{
		{name: "ok", cmd: []string{"bin/console", "cache:clear"}, maxLen: 100},
		{name: "empty", cmd: nil, maxLen: 100, wantErr: "empty"},
		{name: "first empty", cmd: []string{"", "x"}, maxLen: 100, wantErr: "first argument"},
		{name: "later empty argument is fine", cmd: []string{"echo", ""}, maxLen: 100},
		{name: "NUL", cmd: []string{"ls", "a\x00b"}, maxLen: 100, wantErr: "NUL"},
		{name: "too long", cmd: []string{"echo", strings.Repeat("a", 10)}, maxLen: 12, wantErr: "longer"},
		{name: "exactly the limit", cmd: []string{"echo", strings.Repeat("a", 8)}, maxLen: 12},
		{name: "too many arguments", cmd: many, maxLen: 10000, wantErr: "arguments"},
		{name: "256 arguments", cmd: many[:MaxExecArgs], maxLen: 10000},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateExecCommand(tt.cmd, tt.maxLen)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestExec_ExitCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		code       int
		wantResult string
	}{
		{name: "zero", code: 0, wantResult: ExecResultZero},
		{name: "three", code: 3, wantResult: ExecResultNonzero},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			h := startExecHarness(t, execHarnessOpts{run: func(_ context.Context, cmd []string, so, se io.Writer) (int, error) {
				got = cmd
				_, _ = so.Write([]byte("hello "))
				_, _ = se.Write([]byte("oops"))
				_, _ = so.Write([]byte("world"))
				return tt.code, nil
			}})
			h.sendStart(t, "bin/console", "cache:clear")
			frames, code, _ := readFrames(t, h.client)
			assert.Equal(t, CloseNormal, code)
			require.GreaterOrEqual(t, len(frames), 5)
			assert.Equal(t, map[string]any{"type": "started", "pod": "pod-1", "container": "web"}, frames[0].text)
			var so, se strings.Builder
			for _, f := range frames[1 : len(frames)-1] {
				switch f.stream {
				case ExecStreamStdout:
					so.Write(f.data)
				case ExecStreamStderr:
					se.Write(f.data)
				default:
					t.Fatalf("unexpected frame %+v", f)
				}
			}
			assert.Equal(t, "hello world", so.String())
			assert.Equal(t, "oops", se.String())
			assert.Equal(t, map[string]any{"type": "exit", "exit_code": float64(tt.code)}, frames[len(frames)-1].text)
			assert.Equal(t, []string{"bin/console", "cache:clear"}, got)

			sum := h.waitEnd(t)
			assert.True(t, sum.Started)
			require.NotNil(t, sum.ExitCode)
			assert.Equal(t, tt.code, *sum.ExitCode)
			assert.Equal(t, tt.wantResult, sum.Result)
			assert.Equal(t, int64(11), sum.BytesStdout)
			assert.Equal(t, int64(4), sum.BytesStderr)
			assert.Equal(t, CloseNormal, sum.CloseCode)
			assert.Equal(t, 0, h.m.Active())
		})
	}
}

func TestExec_StartMessageErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		send     func(t *testing.T, h *execHarness)
		opts     execHarnessOpts
		wantCode int
	}{
		{name: "no message in time", opts: execHarnessOpts{startTimeout: 100 * time.Millisecond}, send: func(*testing.T, *execHarness) {}, wantCode: CloseStartTimeout},
		{name: "pings do not extend the start window", opts: execHarnessOpts{startTimeout: 200 * time.Millisecond}, send: func(t *testing.T, h *execHarness) {
			for i := 0; i < 5; i++ {
				_ = h.client.WriteControl(gorilla.PingMessage, nil, time.Now().Add(time.Second))
				time.Sleep(60 * time.Millisecond)
			}
		}, wantCode: CloseStartTimeout},
		{name: "binary frame", send: func(t *testing.T, h *execHarness) {
			require.NoError(t, h.client.WriteMessage(gorilla.BinaryMessage, []byte{1, 2}))
		}, wantCode: CloseBadStart},
		{name: "bad JSON", send: func(t *testing.T, h *execHarness) {
			require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte("{nope")))
		}, wantCode: CloseBadStart},
		{name: "wrong type", send: func(t *testing.T, h *execHarness) {
			require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte(`{"type":"run","command":["ls"]}`)))
		}, wantCode: CloseBadStart},
		{name: "empty command", send: func(t *testing.T, h *execHarness) {
			require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":[]}`)))
		}, wantCode: CloseBadStart},
		{name: "command not an array", send: func(t *testing.T, h *execHarness) {
			require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":"ls -la"}`)))
		}, wantCode: CloseBadStart},
		{name: "command too long", opts: execHarnessOpts{maxLen: 5}, send: func(t *testing.T, h *execHarness) {
			h.sendStart(t, "echo", "toolong")
		}, wantCode: CloseBadStart},
		{name: "frame above the read limit", send: func(t *testing.T, h *execHarness) {
			require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte(strings.Repeat("x", DefaultReadLimit+10))))
		}, wantCode: CloseMessageTooBig},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := startExecHarness(t, tt.opts)
			tt.send(t, h)
			frames, code, _ := readFrames(t, h.client)
			assert.Empty(t, frames)
			assert.Equal(t, tt.wantCode, code)
			sum := h.waitEnd(t)
			assert.False(t, sum.Started)
			assert.Equal(t, tt.wantCode, sum.CloseCode)
			assert.Zero(t, h.audits.Load(), "no audit before a valid start message")
			assert.Zero(t, h.ran.Load(), "the command must not run")
		})
	}
}

func TestExec_MaxDurationHasNoExitFrame(t *testing.T) {
	t.Parallel()
	h := startExecHarness(t, execHarnessOpts{
		cfg: Config{MaxDuration: 300 * time.Millisecond},
		run: func(ctx context.Context, _ []string, so, _ io.Writer) (int, error) {
			_, _ = so.Write([]byte("partial"))
			<-ctx.Done()
			return -1, ctx.Err()
		},
	})
	h.sendStart(t, "sleep", "1000")
	frames, code, _ := readFrames(t, h.client)
	assert.Equal(t, CloseMaxDuration, code)
	for _, f := range frames {
		if f.text != nil {
			assert.NotContains(t, []any{"exit", "error"}, f.text["type"], "no exit or error frame at the maximum duration")
		}
	}
	sum := h.waitEnd(t)
	assert.Equal(t, ExecResultTimeout, sum.Result)
	assert.Nil(t, sum.ExitCode)
	assert.True(t, sum.Started)
}

func TestExec_RunErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		err         error
		wantCode    int
		wantMessage string
	}{
		{name: "kubernetes error", err: errors.New("pods \"x\" not found"), wantCode: ClosePodError, wantMessage: `pods "x" not found`},
		{name: "long message is cut", err: errors.New(strings.Repeat("e", 500)), wantCode: ClosePodError, wantMessage: strings.Repeat("e", 200)},
		{name: "internal", err: fmt.Errorf("%w: no rest config", ErrExecInternal), wantCode: CloseInternalError, wantMessage: "Internal server error"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := startExecHarness(t, execHarnessOpts{run: func(context.Context, []string, io.Writer, io.Writer) (int, error) {
				return -1, tt.err
			}})
			h.sendStart(t, "ls")
			frames, code, _ := readFrames(t, h.client)
			assert.Equal(t, tt.wantCode, code)
			last := frames[len(frames)-1]
			assert.Equal(t, "error", last.text["type"])
			assert.Equal(t, tt.wantMessage, last.text["message"])
			sum := h.waitEnd(t)
			assert.Equal(t, ExecResultError, sum.Result)
			assert.Nil(t, sum.ExitCode)
		})
	}
}

func TestExec_AuditVeto(t *testing.T) {
	t.Parallel()
	h := startExecHarness(t, execHarnessOpts{beforeStart: func(context.Context, []string) error {
		return ErrExecAuditUnavailable
	}})
	h.sendStart(t, "ls")
	frames, code, _ := readFrames(t, h.client)
	assert.Equal(t, CloseInternalError, code)
	require.Len(t, frames, 1, "no started frame")
	assert.Equal(t, map[string]any{"type": "error", "message": "audit unavailable"}, frames[0].text)
	assert.Zero(t, h.ran.Load(), "the command never runs")
	sum := h.waitEnd(t)
	assert.False(t, sum.Started)
}

func TestExec_PostRegisterRevokedSkipsEverything(t *testing.T) {
	t.Parallel()
	h := startExecHarness(t, execHarnessOpts{postRegister: func(context.Context) bool { return true }})
	_ = h.client.WriteMessage(gorilla.TextMessage, []byte(`{"type":"start","command":["ls"]}`))
	_, code, reason := readFrames(t, h.client)
	assert.Equal(t, ClosePolicy, code)
	assert.Equal(t, reasonSessionRevoked, reason)
	sum := h.waitEnd(t)
	assert.Equal(t, ExecResultRevoked, sum.Result)
	assert.Zero(t, h.audits.Load())
	assert.Zero(t, h.ran.Load())
}

func TestExec_RevokeDuringCommand(t *testing.T) {
	t.Parallel()
	running := make(chan struct{})
	h := startExecHarness(t, execHarnessOpts{run: func(ctx context.Context, _ []string, _, _ io.Writer) (int, error) {
		close(running)
		<-ctx.Done()
		return -1, ctx.Err()
	}})
	h.sendStart(t, "sleep", "100")
	<-running
	require.Eventually(t, func() bool { return h.hub.RevocableCount() == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, h.hub.DisconnectInstance("inst1"))
	_, code, reason := readFrames(t, h.client)
	assert.Equal(t, ClosePolicy, code)
	assert.Equal(t, websocket.CloseReasonAccessLost, reason)
	sum := h.waitEnd(t)
	assert.Equal(t, ExecResultRevoked, sum.Result)
	assert.True(t, sum.Started)
	assert.Equal(t, KindExec, h.sess.Load().Kind())
}

func TestExec_ShutdownAndClientGone(t *testing.T) {
	t.Parallel()
	block := func(ctx context.Context, _ []string, _, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	t.Run("shutdown", func(t *testing.T) {
		t.Parallel()
		h := startExecHarness(t, execHarnessOpts{run: block})
		h.sendStart(t, "sleep", "100")
		require.Eventually(t, func() bool { return h.ran.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, h.m.Shutdown(ctx))
		_, code, _ := readFrames(t, h.client)
		assert.Equal(t, CloseGoingAway, code)
		assert.Equal(t, ExecResultShutdown, h.waitEnd(t).Result)
	})
	t.Run("client goes away", func(t *testing.T) {
		t.Parallel()
		h := startExecHarness(t, execHarnessOpts{run: block})
		h.sendStart(t, "sleep", "100")
		require.Eventually(t, func() bool { return h.ran.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
		_ = h.client.Close()
		sum := h.waitEnd(t)
		assert.Equal(t, ExecResultClientClosed, sum.Result)
		assert.Equal(t, 1006, sum.CloseCode)
	})
}

// The output of both streams is written as frames of at most 32 KiB, from two
// goroutines at once, without a torn or lost byte.
func TestExec_FramesAreBoundedAndSerialized(t *testing.T) {
	t.Parallel()
	const size = 200_000
	h := startExecHarness(t, execHarnessOpts{run: func(_ context.Context, _ []string, so, se io.Writer) (int, error) {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = so.Write([]byte(strings.Repeat("o", size))) }()
		go func() { defer wg.Done(); _, _ = se.Write([]byte(strings.Repeat("e", size))) }()
		wg.Wait()
		return 0, nil
	}})
	h.sendStart(t, "yes")
	frames, code, _ := readFrames(t, h.client)
	assert.Equal(t, CloseNormal, code)
	var so, se int
	for _, f := range frames {
		if f.text != nil {
			continue
		}
		assert.LessOrEqual(t, len(f.data)+1, ExecFrameMax)
		switch f.stream {
		case ExecStreamStdout:
			so += len(f.data)
			assert.Equal(t, strings.Repeat("o", len(f.data)), string(f.data))
		case ExecStreamStderr:
			se += len(f.data)
			assert.Equal(t, strings.Repeat("e", len(f.data)), string(f.data))
		}
	}
	assert.Equal(t, size, so)
	assert.Equal(t, size, se)
	sum := h.waitEnd(t)
	assert.Equal(t, int64(size), sum.BytesStdout)
	assert.Equal(t, int64(size), sum.BytesStderr)
}

func TestExec_ClientFramesAfterStartAreIgnored(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := startExecHarness(t, execHarnessOpts{run: func(ctx context.Context, _ []string, _, _ io.Writer) (int, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return 0, nil
	}})
	h.sendStart(t, "cat")
	require.Eventually(t, func() bool { return h.ran.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	require.NoError(t, h.client.WriteMessage(gorilla.BinaryMessage, []byte("stdin?")))
	require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte("whatever")))
	close(release)
	frames, code, _ := readFrames(t, h.client)
	assert.Equal(t, CloseNormal, code)
	assert.Equal(t, "exit", frames[len(frames)-1].text["type"])
}

func TestExecResult(t *testing.T) {
	t.Parallel()
	zero, three := 0, 3
	tests := []struct {
		code int
		exit *int
		want string
	}{
		{CloseNormal, &zero, ExecResultZero}, {CloseNormal, &three, ExecResultNonzero}, {CloseNormal, nil, ExecResultZero},
		{CloseMaxDuration, nil, ExecResultTimeout}, {ClosePolicy, nil, ExecResultRevoked},
		{CloseGoingAway, nil, ExecResultShutdown}, {closeAbnormalOnly, nil, ExecResultClientClosed},
		{ClosePodError, nil, ExecResultError}, {CloseInternalError, nil, ExecResultError}, {CloseBadStart, nil, ExecResultError},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(fmt.Sprint(tt.code, tt.want), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, execResult(tt.code, tt.exit))
		})
	}
}

func TestTruncateMessage(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "short", truncateMessage("short"))
	long := strings.Repeat("é", 150) // 300 bytes
	got := truncateMessage(long)
	assert.LessOrEqual(t, len(got), maxExecErrorMessage)
	assert.True(t, strings.HasPrefix(long, got))
}

func TestManager_KindAndLimits(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{Kind: KindExec, MaxPerUser: 1})
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	assert.Equal(t, KindExec, m.Kind())
	assert.Equal(t, KindPortForward, NewManager(Config{}).Kind())
	s, err := m.Acquire("u")
	require.NoError(t, err)
	_, err = m.Acquire("u")
	assert.ErrorIs(t, err, ErrLimitReached)
	s.Release()
}
