package k8s

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	cgportforward "k8s.io/client-go/tools/portforward"
)

type fakeStream struct {
	headers http.Header
	r       io.Reader
	mu      sync.Mutex
	closed  bool
	reset   bool
}

func (s *fakeStream) Read(p []byte) (int, error) {
	if s.r == nil {
		return 0, io.EOF
	}
	return s.r.Read(p)
}
func (s *fakeStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *fakeStream) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset = true
	return nil
}
func (s *fakeStream) Headers() http.Header { return s.headers }
func (s *fakeStream) Identifier() uint32   { return 0 }

func (s *fakeStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type fakeConn struct {
	errText    string
	failOn     int // 1-based CreateStream call that fails; 0 = none
	created    []*fakeStream
	closeCalls atomic.Int32
}

func (c *fakeConn) CreateStream(h http.Header) (httpstream.Stream, error) {
	if c.failOn == len(c.created)+1 {
		return nil, errors.New("boom")
	}
	st := &fakeStream{headers: h.Clone()}
	if h.Get(corev1.StreamType) == corev1.StreamTypeError {
		st.r = strings.NewReader(c.errText)
	}
	c.created = append(c.created, st)
	return st, nil
}
func (c *fakeConn) Close() error                       { c.closeCalls.Add(1); return nil }
func (c *fakeConn) CloseChan() <-chan bool             { return make(chan bool) }
func (c *fakeConn) SetIdleTimeout(time.Duration)       {}
func (c *fakeConn) RemoveStreams(...httpstream.Stream) {}

func TestOpenPodPortStream(t *testing.T) {
	t.Parallel()
	t.Run("creates error then data stream with the port headers", func(t *testing.T) {
		t.Parallel()
		conn := &fakeConn{errText: "connection refused"}
		ps, err := OpenPodPortStream(conn, 8080)
		require.NoError(t, err)
		require.Len(t, conn.created, 2)
		errH, dataH := conn.created[0].headers, conn.created[1].headers
		assert.Equal(t, corev1.StreamTypeError, errH.Get(corev1.StreamType))
		assert.Equal(t, corev1.StreamTypeData, dataH.Get(corev1.StreamType))
		for _, h := range []http.Header{errH, dataH} {
			assert.Equal(t, "8080", h.Get(corev1.PortHeader))
			assert.Equal(t, "0", h.Get(corev1.PortForwardRequestIDHeader))
		}
		assert.True(t, conn.created[0].isClosed(), "the error stream is closed for writing")
		assert.False(t, conn.created[1].isClosed(), "the data stream stays open")

		text, err := io.ReadAll(ps.Errors())
		require.NoError(t, err)
		assert.Equal(t, "connection refused", string(text))

		require.NoError(t, ps.Close())
		require.NoError(t, ps.Close())
		assert.EqualValues(t, 1, conn.closeCalls.Load(), "Close is idempotent")
		assert.True(t, conn.created[0].reset)
		assert.True(t, conn.created[1].reset)
	})
	tests := []struct {
		name   string
		failOn int
	}{
		{"error stream fails", 1},
		{"data stream fails", 2},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := &fakeConn{failOn: tt.failOn}
			ps, err := OpenPodPortStream(conn, 80)
			assert.Nil(t, ps)
			assert.ErrorIs(t, err, ErrDialFailed)
			assert.EqualValues(t, 1, conn.closeCalls.Load(), "the connection is closed")
		})
	}
}

func TestDialPodConn(t *testing.T) {
	t.Parallel()
	v1 := cgportforward.PortForwardProtocolV1Name

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		conn := &fakeConn{}
		got, err := dialPodConn(context.Background(), func(context.Context, ...string) (httpstream.Connection, string, error) {
			return conn, v1, nil
		})
		require.NoError(t, err)
		assert.Same(t, conn, got)
	})
	t.Run("dial error", func(t *testing.T) {
		t.Parallel()
		_, err := dialPodConn(context.Background(), func(context.Context, ...string) (httpstream.Connection, string, error) {
			return nil, "", errors.New("refused")
		})
		assert.ErrorIs(t, err, ErrDialFailed)
	})
	t.Run("wrong protocol closes the connection", func(t *testing.T) {
		t.Parallel()
		conn := &fakeConn{}
		_, err := dialPodConn(context.Background(), func(context.Context, ...string) (httpstream.Connection, string, error) {
			return conn, "other", nil
		})
		assert.ErrorIs(t, err, ErrDialFailed)
		assert.EqualValues(t, 1, conn.closeCalls.Load())
	})
	t.Run("late connection after the caller left is closed", func(t *testing.T) {
		t.Parallel()
		conn := &fakeConn{}
		release := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(50 * time.Millisecond); cancel() }()
		_, err := dialPodConn(ctx, func(context.Context, ...string) (httpstream.Connection, string, error) {
			<-release
			return conn, v1, nil
		})
		assert.ErrorIs(t, err, context.Canceled)
		close(release)
		assert.Eventually(t, func() bool { return conn.closeCalls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	})
}

// stalledServer accepts connections and never answers. It reports when the
// client closed a connection.
func stalledServer(t *testing.T) (addr string, closed *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	closed = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(io.Discard, c) // returns when the client closes
				closed.Add(1)
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().String(), closed
}

// A stalled API server must not hold the dial goroutine or its connection:
// both dialers end with the context and the connection is closed.
func TestPodDialers_StalledServer(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"websocket", "spdy"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			addr, closed := stalledServer(t)
			cfg := &rest.Config{Host: "http://" + addr}
			u, err := url.Parse("http://" + addr + "/api/v1/namespaces/ns/pods/p/portforward")
			require.NoError(t, err)
			var dial podConnDialer
			if kind == "websocket" {
				dial = wsTunnelDialer(cfg, u)
			} else {
				dial, err = spdyPodDialer(cfg, u)
				require.NoError(t, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := dial(ctx, cgportforward.PortForwardProtocolV1Name)
				done <- err
			}()
			select {
			case err := <-done:
				assert.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("the dial goroutine did not end with the context")
			}
			assert.Eventually(t, func() bool { return closed.Load() >= 1 }, 3*time.Second, 20*time.Millisecond,
				"the connection to the stalled server is closed")
		})
	}
}

func TestResolveTargetPort_NamedPort(t *testing.T) {
	t.Parallel()
	always := corev1.ContainerRestartPolicyAlways
	named := func(port string) *corev1.ServicePort {
		return &corev1.ServicePort{Port: 80, TargetPort: intstr.FromString(port)}
	}
	ctr := func(name string, p int32, rp *corev1.ContainerRestartPolicy) corev1.Container {
		return corev1.Container{Name: name, RestartPolicy: rp, Ports: []corev1.ContainerPort{{Name: name + "-port", ContainerPort: p}}}
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers:     []corev1.Container{ctr("app", 8080, nil)},
		InitContainers: []corev1.Container{ctr("classic", 9000, nil), ctr("sidecar", 9100, &always)},
	}}
	tests := []struct {
		name    string
		port    string
		want    int32
		wantErr bool
	}{
		{"regular container", "app-port", 8080, false},
		{"native sidecar", "sidecar-port", 9100, false},
		{"classic init container is ignored", "classic-port", 0, true},
		{"unknown name", "nope", 0, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveTargetPort(named(tt.port), pod)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrPortNotFound)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
