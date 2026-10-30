package podstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

// fakeStream is a PodPortStream. The test plays the pod through podData and
// podErr.
type fakeStream struct {
	sessData net.Conn // the session side of the data stream
	podData  net.Conn // the pod side
	errR     *io.PipeReader
	podErr   *io.PipeWriter
	closed   chan struct{}
	once     sync.Once
}

func newFakeStream() *fakeStream {
	a, b := net.Pipe()
	r, w := io.Pipe()
	return &fakeStream{sessData: a, podData: b, errR: r, podErr: w, closed: make(chan struct{})}
}

func (f *fakeStream) Data() io.ReadWriteCloser { return f.sessData }
func (f *fakeStream) Errors() io.Reader        { return f.errR }
func (f *fakeStream) Close() error {
	f.once.Do(func() {
		close(f.closed)
		_ = f.sessData.Close()
		_ = f.podData.Close()
		_ = f.errR.Close()
		_ = f.podErr.Close()
	})
	return nil
}

type harness struct {
	m      *Manager
	client *gorilla.Conn
	stream *fakeStream
	sess   *Session
	ended  chan Summary
	hub    *websocket.Hub
	srv    *httptest.Server
}

type harnessOpts struct {
	cfg       Config
	configure func(*httptest.Server)
	// postRegister is PortForwardOptions.PostRegister.
	postRegister func(context.Context) bool
	// stream replaces the default fake stream.
	stream k8s.PodPortStream
}

func startHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	h := &harness{m: NewManager(o.cfg), stream: newFakeStream(), ended: make(chan Summary, 1), hub: hub}
	t.Cleanup(func() { _ = h.m.Shutdown(context.Background()) })

	up := gorilla.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	started := make(chan struct{})
	h.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		slot, err := h.m.Acquire("u1")
		if err != nil {
			_ = conn.Close()
			return
		}
		var stream k8s.PodPortStream = h.stream
		if o.stream != nil {
			stream = o.stream
		}
		h.sess = h.m.StartPortForward(PortForwardOptions{
			Conn: conn, Stream: stream, Slot: slot, Hub: hub, PostRegister: o.postRegister,
			Identity: websocket.ClientIdentity{UserID: "u1"}, InstanceID: "inst1",
			OnEnd: func(s Summary) { h.ended <- s },
		})
		close(started)
	}))
	if o.configure != nil {
		o.configure(h.srv)
	}
	h.srv.Start()
	t.Cleanup(h.srv.Close)
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http")
	c, _, err := gorilla.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	h.client = c
	t.Cleanup(func() { _ = c.Close() })
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not start")
	}
	if o.postRegister == nil && o.stream == nil {
		// With a PostRegister check the session may already be gone.
		require.Eventually(t, func() bool { return hub.RevocableCount() == 1 }, 2*time.Second, 5*time.Millisecond)
	}
	return h
}

func (h *harness) waitEnd(t *testing.T) Summary {
	t.Helper()
	select {
	case s := <-h.ended:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
		return Summary{}
	}
}

// expectClose reads until the close frame and returns its code and text.
func expectClose(t *testing.T, c *gorilla.Conn) (int, string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue
		}
		var ce *gorilla.CloseError
		require.True(t, errors.As(err, &ce), "got %v", err)
		return ce.Code, ce.Text
	}
}

var fast = Timings{PingInterval: 50 * time.Millisecond, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second}

func TestSession_CloseCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      Config
		trigger  func(t *testing.T, h *harness)
		wantCode int
		wantText string
	}{
		{
			name: "max duration",
			cfg:  Config{Timings: fast, MaxDuration: 150 * time.Millisecond, IdleTimeout: time.Minute},
			trigger: func(*testing.T, *harness) {
			},
			wantCode: CloseMaxDuration,
		},
		{
			name: "idle timeout",
			cfg:  Config{Timings: fast, MaxDuration: time.Minute, IdleTimeout: 150 * time.Millisecond},
			trigger: func(*testing.T, *harness) {
			},
			wantCode: CloseIdleTimeout,
		},
		{
			name: "revoke",
			cfg:  Config{Timings: fast},
			trigger: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.hub.DisconnectInstance("inst1"))
			},
			wantCode: 1008,
			wantText: websocket.CloseReasonAccessLost,
		},
		{
			name: "shutdown",
			cfg:  Config{Timings: fast},
			trigger: func(t *testing.T, h *harness) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, h.m.Shutdown(ctx))
			},
			wantCode: 1001,
		},
		{
			name: "text frame",
			cfg:  Config{Timings: fast},
			trigger: func(t *testing.T, h *harness) {
				require.NoError(t, h.client.WriteMessage(gorilla.TextMessage, []byte("hi")))
			},
			wantCode: 1003,
		},
		{
			name: "pod error",
			cfg:  Config{Timings: fast},
			trigger: func(t *testing.T, h *harness) {
				_, err := h.stream.podErr.Write([]byte("connection refused"))
				require.NoError(t, err)
			},
			wantCode: ClosePodError,
			wantText: "connection refused",
		},
		{
			name: "pod EOF",
			cfg:  Config{Timings: fast},
			trigger: func(t *testing.T, h *harness) {
				require.NoError(t, h.stream.podData.Close())
			},
			wantCode: 1000,
		},
		{
			name: "client close",
			cfg:  Config{Timings: fast},
			trigger: func(t *testing.T, h *harness) {
				require.NoError(t, h.client.WriteControl(gorilla.CloseMessage,
					gorilla.FormatCloseMessage(1000, ""), time.Now().Add(time.Second)))
			},
			wantCode: 1000,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := startHarness(t, harnessOpts{cfg: tt.cfg})
			tt.trigger(t, h)
			code, text := expectClose(t, h.client)
			assert.Equal(t, tt.wantCode, code)
			if tt.wantText != "" {
				assert.Equal(t, tt.wantText, text)
			}
			sum := h.waitEnd(t)
			assert.Equal(t, tt.wantCode, sum.CloseCode)
			assert.Eventually(t, func() bool { return h.m.Active() == 0 }, 2*time.Second, 10*time.Millisecond, "slot released")
			select {
			case <-h.stream.closed:
			case <-time.After(2 * time.Second):
				t.Fatal("pod stream not closed")
			}
		})
	}
}

func TestSession_BytesCountedAndRoundTrip(t *testing.T) {
	t.Parallel()
	h := startHarness(t, harnessOpts{cfg: Config{Timings: fast}})

	require.NoError(t, h.client.WriteMessage(gorilla.BinaryMessage, []byte("hello pod")))
	buf := make([]byte, 9)
	_ = h.stream.podData.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := io.ReadFull(h.stream.podData, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello pod", string(buf))

	_, err = h.stream.podData.Write([]byte("hi client!!"))
	require.NoError(t, err)
	_ = h.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err := h.client.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, gorilla.BinaryMessage, mt)
	assert.Equal(t, "hi client!!", string(data))

	require.NoError(t, h.client.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(1000, ""), time.Now().Add(time.Second)))
	sum := h.waitEnd(t)
	assert.Equal(t, int64(9), sum.BytesIn)
	assert.Equal(t, int64(11), sum.BytesOut)
	assert.Positive(t, sum.Duration)
}

func TestSession_PingsAreNotActivity(t *testing.T) {
	t.Parallel()
	h := startHarness(t, harnessOpts{cfg: Config{Timings: fast, IdleTimeout: 300 * time.Millisecond, MaxDuration: time.Minute}})
	// The client answers the server pings (the read loop does it) and sends
	// its own pings and pongs; none of this is data.
	go func() {
		for {
			if _, _, err := h.client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tk := time.NewTicker(40 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				_ = h.client.WriteControl(gorilla.PingMessage, nil, time.Now().Add(time.Second))
			case <-stop:
				return
			}
		}
	}()
	sum := h.waitEnd(t)
	assert.Equal(t, CloseIdleTimeout, sum.CloseCode)
}

func TestSession_DataKeepsTunnelAlive(t *testing.T) {
	t.Parallel()
	// The idle timeout is far above the write interval (1 s against 100 ms),
	// so a slow -race run cannot make a gap look idle; the writes last longer
	// than the idle timeout, so only the data keeps the tunnel open.
	h := startHarness(t, harnessOpts{cfg: Config{Timings: fast, IdleTimeout: time.Second, MaxDuration: time.Minute}})
	go func() { _, _ = io.Copy(io.Discard, h.stream.podData) }()
	for i := 0; i < 15; i++ {
		require.NoError(t, h.client.WriteMessage(gorilla.BinaryMessage, []byte("x")))
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case s := <-h.ended:
		t.Fatalf("tunnel closed with %d", s.CloseCode)
	default:
	}
}

func TestSession_SurvivesServerTimeouts(t *testing.T) {
	t.Parallel()
	h := startHarness(t, harnessOpts{
		cfg: Config{Timings: fast},
		configure: func(s *httptest.Server) {
			s.Config.ReadTimeout = 150 * time.Millisecond
			s.Config.WriteTimeout = 150 * time.Millisecond
		},
	})
	go func() {
		for {
			if _, _, err := h.client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	time.Sleep(600 * time.Millisecond) // four times the server timeouts
	select {
	case s := <-h.ended:
		t.Fatalf("tunnel closed by a server timeout with %d %q", s.CloseCode, s.CloseReason)
	default:
	}
	// Still carries data after the timeouts.
	_, err := h.stream.podData.Write([]byte("late"))
	require.NoError(t, err)
}

func TestManager_Slots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     Config
		users   []string
		wantErr []bool
	}{
		{"per user limit", Config{MaxPerUser: 2}, []string{"a", "a", "a", "b"}, []bool{false, false, true, false}},
		{"process cap", Config{MaxPerUser: 5, ProcessCap: 2}, []string{"a", "b", "c"}, []bool{false, false, true}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewManager(tt.cfg)
			defer func() { _ = m.Shutdown(context.Background()) }()
			var slots []*Slot
			for i, u := range tt.users {
				s, err := m.Acquire(u)
				if tt.wantErr[i] {
					assert.ErrorIs(t, err, ErrLimitReached)
					continue
				}
				require.NoError(t, err)
				slots = append(slots, s)
			}
			for _, s := range slots {
				s.Release()
				s.Release() // idempotent
			}
			assert.Equal(t, 0, m.Active())
			_, err := m.Acquire(tt.users[0])
			assert.NoError(t, err)
		})
	}
}

func TestManager_AcquireAfterShutdown(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{})
	require.NoError(t, m.Shutdown(context.Background()))
	_, err := m.Acquire("a")
	assert.ErrorIs(t, err, ErrShutdown)
}

func TestTruncateReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{"short", "ok"},
		{"long ascii", strings.Repeat("a", 500)},
		{"multibyte at the cut", strings.Repeat("é", 100)},
		{"invalid utf8", "a\xffb"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateReason(tt.in)
			assert.LessOrEqual(t, len(got), 123)
			assert.True(t, strings.ToValidUTF8(got, "") == got)
		})
	}
}

func TestSession_PostRegister(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		postRegister func(context.Context) bool
		wantCode     int
		wantText     string
		wantRevoked  bool
	}{
		{
			name:         "revoked after the registration",
			postRegister: func(context.Context) bool { return true },
			wantCode:     ClosePolicy, wantText: "session revoked", wantRevoked: true,
		},
		{
			name:         "panic in the check closes with 1011 and tears down",
			postRegister: func(context.Context) bool { panic("boom") },
			wantCode:     CloseInternalError, wantText: "internal error",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := startHarness(t, harnessOpts{cfg: Config{Timings: fast}, postRegister: tt.postRegister})
			code, text := expectClose(t, h.client)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantText, text)
			sum := h.waitEnd(t)
			assert.Equal(t, tt.wantCode, sum.CloseCode)
			assert.Eventually(t, func() bool { return h.m.Active() == 0 }, 2*time.Second, 10*time.Millisecond, "slot released")
			select {
			case <-h.stream.closed:
			case <-time.After(2 * time.Second):
				t.Fatal("pod stream not closed")
			}
			assert.Eventually(t, func() bool { return h.hub.RevocableCount() == 0 }, 2*time.Second, 10*time.Millisecond, "unregistered")
		})
	}

	t.Run("not revoked passes data", func(t *testing.T) {
		t.Parallel()
		var calls int
		h := startHarness(t, harnessOpts{cfg: Config{Timings: fast}, postRegister: func(context.Context) bool { calls++; return false }})
		require.NoError(t, h.client.WriteMessage(gorilla.BinaryMessage, []byte("ok")))
		buf := make([]byte, 2)
		_ = h.stream.podData.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err := io.ReadFull(h.stream.podData, buf)
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
	})
}

func TestSession_Kind(t *testing.T) {
	t.Parallel()
	assert.Equal(t, KindPortForward, (&Session{}).Kind())
}

func TestSession_MessageTooBig(t *testing.T) {
	t.Parallel()
	h := startHarness(t, harnessOpts{cfg: Config{Timings: Timings{PingInterval: 50 * time.Millisecond, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second, ReadLimit: 1024}}})
	require.NoError(t, h.client.WriteMessage(gorilla.BinaryMessage, make([]byte, 4096)))
	code, _ := expectClose(t, h.client)
	assert.Equal(t, CloseMessageTooBig, code)
	sum := h.waitEnd(t)
	assert.Equal(t, CloseMessageTooBig, sum.CloseCode)
	assert.Equal(t, "message_too_big", closeReasonLabel(sum.CloseCode))
}

// fakeConn is an httpstream.Connection whose error stream yields errText and
// whose data stream blocks until the connection is closed.
type fakeConn struct {
	errText string
	data    *io.PipeReader
	dataW   *io.PipeWriter
}

type connStream struct {
	r io.Reader
}

func (s *connStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *connStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *connStream) Close() error                { return nil }
func (s *connStream) Reset() error                { return nil }
func (s *connStream) Headers() http.Header        { return http.Header{} }
func (s *connStream) Identifier() uint32          { return 0 }

func (c *fakeConn) CreateStream(h http.Header) (httpstream.Stream, error) {
	if h.Get("streamType") == "error" {
		return &connStream{r: strings.NewReader(c.errText)}, nil
	}
	return &connStream{r: c.data}, nil
}
func (c *fakeConn) Close() error                       { _ = c.dataW.Close(); return nil }
func (c *fakeConn) CloseChan() <-chan bool             { return make(chan bool) }
func (c *fakeConn) SetIdleTimeout(time.Duration)       {}
func (c *fakeConn) RemoveStreams(...httpstream.Stream) {}

// The error text of the kubelet, read from the stream that k8s.OpenPodPortStream
// creates, ends the session with 4502 and the text as the reason.
func TestSession_RealStreamErrorText(t *testing.T) {
	t.Parallel()
	r, w := io.Pipe()
	conn := &fakeConn{errText: "an error occurred forwarding 8080 -> 80: connection refused", data: r, dataW: w}
	ps, err := k8s.OpenPodPortStream(conn, 80)
	require.NoError(t, err)
	h := startHarness(t, harnessOpts{cfg: Config{Timings: fast}, stream: ps})
	code, text := expectClose(t, h.client)
	assert.Equal(t, ClosePodError, code)
	assert.Contains(t, text, "connection refused")
	sum := h.waitEnd(t)
	assert.Equal(t, ClosePodError, sum.CloseCode)
}
